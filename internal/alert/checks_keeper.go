package alert

import (
	"sort"
	"strings"
	"time"

	"clickhouse-diagnostic/internal/bundle"
)

// Keeper、后台池和 part 传播相关的 check。onprem 和 gov 读取单主机的
// 每小时 metric_log_7_days；cloud 读取按主机划分的 metric_log_by_host_3_days，
// 因此 cloud 的基线跨度是 3 天而不是 7 天。

func init() {
	RegisterCheck("keeper_health", func(b *bundle.Bundle, mode string) ([]row, error) { return checkKeeperHours(b, mode, true) })
	RegisterCheck("keeper_connection_blips", func(b *bundle.Bundle, mode string) ([]row, error) { return checkKeeperHours(b, mode, false) })
	RegisterCheck("fetch_pool_saturated", func(b *bundle.Bundle, mode string) ([]row, error) {
		return checkPoolSaturated(b, mode, "avg_fetch_pool_tasks", "background_fetches_pool_size", "max(CurrentMetric_BackgroundFetchesPoolSize)", 0.9)
	})
	RegisterCheck("schedule_pool_saturated", func(b *bundle.Bundle, mode string) ([]row, error) {
		return checkPoolSaturated(b, mode, "avg_schedule_pool_tasks", "background_schedule_pool_size", "max(CurrentMetric_BackgroundSchedulePoolSize)", 0.95)
	})
	RegisterCheck("keeper_xid_renewal_due", checkKeeperXid)
	RegisterCheck("parts_propagation_lag", checkPartsPropagationLag)
}

// hostHour 是一台主机的一个每小时 metric_log 桶。
type hostHour struct {
	host string
	hour time.Time
	r    bundle.Row
}

// metricHours 返回窗口内每台主机的每小时 metric_log 桶：
// cloud 来自按主机划分的文件，onprem/gov 来自单主机文件。
func metricHours(b *bundle.Bundle, mode string, window time.Duration) ([]hostHour, error) {
	collector := "system.metric_log_7_days"
	if mode == "cloud" {
		collector = "system.metric_log_by_host_3_days"
	}
	rows, err := b.Rows(collector)
	if err != nil {
		return nil, err
	}
	now, loc, err := serverClock(b)
	if err != nil {
		return nil, err
	}
	host := serverHost(b)
	var out []hostHour
	for _, r := range rows {
		t, ok := inWindow(r, "time", now, loc, window)
		if !ok {
			continue
		}
		h := host
		if mode == "cloud" {
			h = r.Str("hostname")
		}
		out = append(out, hostHour{h, t, r})
	}
	return out, nil
}

// keeper_health（degraded）/ keeper_connection_blips：Keeper 硬件异常
// 超过 1000 的小时，按该主机的事务量是否低于其通常（中位数）小时的一半
// 来区分。
func checkKeeperHours(b *bundle.Bundle, mode string, degraded bool) ([]row, error) {
	hours, err := metricHours(b, mode, 7*24*time.Hour)
	if err != nil {
		return nil, err
	}
	tx := map[string][]float64{}
	for _, h := range hours {
		tx[h.host] = append(tx[h.host], h.r.Float("zk_transactions"))
	}
	usual := map[string]float64{}
	for host, xs := range tx {
		usual[host] = medianHigh(xs)
	}
	var out []row
	perHost := map[string]int{}
	sort.SliceStable(hours, func(i, j int) bool {
		if !hours[i].hour.Equal(hours[j].hour) {
			return hours[i].hour.Before(hours[j].hour)
		}
		return hours[i].host < hours[j].host
	})
	for _, h := range hours {
		u := usual[h.host]
		hw, t := h.r.Int("zk_hw_exceptions"), h.r.Float("zk_transactions")
		if u <= 0 || hw <= 1000 || (t < 0.5*u) != degraded || perHost[h.host] == 168 {
			continue
		}
		perHost[h.host]++
		res := row{"hostname": h.host, "hour": fmtHour(h.hour), "hw_exceptions": hw, "transactions": h.r.Int("zk_transactions"),
			"pct_of_usual": roundTo(100*t/maxf(u, 1), 0)}
		if degraded {
			res["usual_transactions"] = roundTo(u, 0)
		}
		out = append(out, res)
	}
	return out, nil
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// poolSizes 是每台主机配置的池大小。Cloud：按主机指标文件中的实时上限
// （窗口内的最大值）；缺少该值的主机回退到 server_settings。Onprem/gov：
// 23.3 起用 server_settings，因为池大小在那里迁移了 —— 那里的
// system.settings 仍以旧的会话默认值列出这些名称，而那是错误的大小 ——
// 23.3 以下则用 system.settings。源文件缺失会得到 NotCollected，
// 绝不会得到大小为 0。
func poolSizes(b *bundle.Bundle, mode string, hours []hostHour, setting, sizeCol string) (map[string]float64, error) {
	sizes := map[string]float64{}
	for _, h := range hours {
		sizes[h.host] += 0
		if mode == "cloud" {
			if v := h.r.Float(sizeCol); v > sizes[h.host] {
				sizes[h.host] = v
			}
		}
	}
	// 等所有桶都折叠完再看：如果一台主机的第一个小时缺少池大小、
	// 但后面的小时有，则无需回退。
	need := false
	for _, s := range sizes {
		if s == 0 {
			need = true
		}
	}
	if !need {
		return sizes, nil
	}
	source := "system.server_settings"
	if mode != "cloud" && !serverAtLeast(b, 23, 3) {
		source = "system.settings"
	}
	rows, err := b.Rows(source)
	if err != nil {
		return nil, err
	}
	configured := 0.0
	for _, r := range rows {
		if r.Str("name") == setting {
			if v := r.Float("value"); v > configured {
				configured = v
			}
		}
	}
	for _, h := range hours {
		if sizes[h.host] == 0 {
			sizes[h.host] = configured
		}
	}
	return sizes, nil
}

// fetch_pool_saturated / schedule_pool_saturated：在最近 24 小时中，
// 池的平均占用至少有 3 个小时达到其大小的 frac 倍的主机。
func checkPoolSaturated(b *bundle.Bundle, mode, taskCol, setting, sizeCol string, frac float64) ([]row, error) {
	hours, err := metricHours(b, mode, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	sizes, err := poolSizes(b, mode, hours, setting, sizeCol)
	if err != nil {
		return nil, err
	}
	type agg struct {
		n   int64
		sum float64
	}
	m := map[string]*agg{}
	for _, h := range hours {
		size, avg := sizes[h.host], h.r.Float(taskCol)
		if size <= 0 || avg < frac*size {
			continue
		}
		a := m[h.host]
		if a == nil {
			a = &agg{}
			m[h.host] = a
		}
		a.n++
		a.sum += avg
	}
	var out []row
	for host, a := range m {
		if a.n < 3 {
			continue
		}
		avg := a.sum / float64(a.n)
		out = append(out, row{"hostname": host, "hours_saturated": a.n, "avg_tasks_in_pool": roundTo(avg, 1),
			"pool_size": int64(sizes[host]), "pct_of_size": roundTo(100*avg/maxf(sizes[host], 1), 0)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["hours_saturated"] != out[j]["hours_saturated"] {
			return out[i]["hours_saturated"].(int64) > out[j]["hours_saturated"].(int64)
		}
		return out[i]["hostname"].(string) < out[j]["hostname"].(string)
	})
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

const xidMax = 2147483647

// keeper_xid_renewal_due：某个存活的单个 Keeper 会话，其 xid 按最近 24 小时
// 的 Keeper 请求速率计算，会在一天内达到 2^31。
func checkKeeperXid(b *bundle.Bundle, mode string) ([]row, error) {
	conns, err := b.Rows("system.zookeeper_connection")
	if err != nil {
		return nil, err
	}
	hours, err := metricHours(b, mode, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	type conn struct {
		sessions   int
		name, host string
		xid        int64
	}
	host := serverHost(b)
	cs := map[string]*conn{}
	for _, r := range conns {
		if r.Int("is_expired") != 0 {
			continue
		}
		h := host
		if mode == "cloud" {
			h = r.Str("hostname")
		}
		c := cs[h]
		if c == nil {
			c = &conn{name: r.Str("name"), host: r.Str("host")}
			cs[h] = c
		}
		c.sessions++
		if x := r.Int("xid"); x > c.xid {
			c.xid = x
		}
	}
	// 覆盖时长是 SQL 的 dateDiff(min(event_time), max(event_time))：
	// 从最早桶的第一个采样到最晚桶的最后一个采样，包含桶之间的间隙。
	_, loc, err := collectedAt(b)
	if err != nil {
		return nil, err
	}
	type rate struct {
		tx          float64
		first, last time.Time
		covered     float64
	}
	rs := map[string]*rate{}
	for _, h := range hours {
		r := rs[h.host]
		if r == nil {
			r = &rate{}
			rs[h.host] = r
		}
		r.tx += h.r.Float("zk_transactions")
		if f, ok := parseTime(h.r.Str("first_sample"), loc); ok && (r.first.IsZero() || f.Before(r.first)) {
			r.first = f
		}
		if l, ok := parseTime(h.r.Str("last_sample"), loc); ok && l.After(r.last) {
			r.last = l
		}
	}
	for _, r := range rs {
		if !r.first.IsZero() && r.last.After(r.first) {
			r.covered = r.last.Sub(r.first).Seconds()
		}
	}
	var out []row
	for h, c := range cs {
		r := rs[h]
		if r == nil || c.sessions != 1 || c.xid <= 0 || c.xid >= xidMax || r.covered < 7200 || r.tx <= 0 {
			continue
		}
		perS := r.tx / r.covered
		left := float64(xidMax-c.xid) / perS
		if left >= 86400 {
			continue
		}
		out = append(out, row{"hostname": h, "connection_name": c.name, "keeper_host": c.host, "xid": c.xid,
			"tx_per_s": roundTo(perS, 2), "measured_over_h": roundTo(r.covered/3600, 1),
			"headroom_h": roundTo(left/3600, 1), "period_at_this_rate_h": roundTo(xidMax/perS/3600, 1)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["headroom_h"].(float64) < out[j]["headroom_h"].(float64) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}

// parts_propagation_lag（cloud）：在某个分区上落后最新 replica 至少 32 个块，
// 或缺少最近一天写入的某个分区的 replica。
// system.parts_max_block_by_replica 保存每个落后的 (host, partition) 行，
// 并逐行带上该分区的 replicas_missing_partition。
func checkPartsPropagationLag(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.parts_max_block_by_replica")
	if err != nil {
		return nil, err
	}
	now, loc, err := collectedAt(b) // 实时的 parts 快照：-to 不会移动它
	if err != nil {
		return nil, err
	}
	type agg struct {
		partitions, notFetched int64
		maxBehind, maxSkew     int64
		worst                  string
		seen                   map[string]bool
	}
	m := map[string]*agg{}
	add := func(host, part string, behind, skew int64, missing bool) {
		a := m[host]
		if a == nil {
			a = &agg{maxBehind: -1, seen: map[string]bool{}}
			m[host] = a
		}
		if a.seen[part] {
			return
		}
		a.seen[part] = true
		a.partitions++
		if missing {
			a.notFetched++
		}
		if behind > a.maxBehind {
			a.maxBehind, a.worst = behind, part
		}
		if skew > a.maxSkew {
			a.maxSkew = skew
		}
	}
	missingDone := map[string]bool{}
	for _, r := range rows {
		part := r.Str("database") + "." + r.Str("table") + " / " + r.Str("partition_id")
		if behind := r.Int("blocks_behind"); behind >= 32 {
			add(r.Str("hostname"), part, behind, r.Int("newest_part_skew_s"), false)
		}
		if missingDone[part] {
			continue
		}
		missingDone[part] = true
		last, ok := parseTime(r.Str("cluster_last_part_time"), loc)
		if !ok || !last.After(now.Add(-24*time.Hour)) {
			continue
		}
		if arr, ok := r["replicas_missing_partition"].([]any); ok {
			for _, h := range arr {
				if s, ok := h.(string); ok && strings.TrimSpace(s) != "" {
					add(s, part, r.Int("cluster_max_block"), 0, true)
				}
			}
		}
	}
	var out []row
	for host, a := range m {
		out = append(out, row{"hostname": host, "partitions_affected": a.partitions, "partitions_not_fetched": a.notFetched,
			"max_blocks_behind": a.maxBehind, "worst_partition": a.worst, "max_newest_part_skew_s": a.maxSkew})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i]["max_blocks_behind"] != out[j]["max_blocks_behind"] {
			return out[i]["max_blocks_behind"].(int64) > out[j]["max_blocks_behind"].(int64)
		}
		return out[i]["hostname"].(string) < out[j]["hostname"].(string)
	})
	if len(out) > 100 {
		out = out[:100]
	}
	return out, nil
}
