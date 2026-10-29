package alert

import (
	"sort"
	"strings"
	"time"

	"clickhouse-diagnostic/internal/bundle"
)

// Keeper, background-pool and part-propagation checks. Onprem and gov read
// the single-host hourly metric_log_7_days; cloud reads the per-host
// metric_log_by_host_3_days, so a cloud baseline spans 3 days, not 7.

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

// hostHour is one hourly metric_log bucket of one host.
type hostHour struct {
	host string
	hour time.Time
	r    bundle.Row
}

// metricHours returns the hourly metric_log buckets inside the window, per
// host: cloud from the per-host file, onprem/gov from the single-host one.
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

// keeper_health (degraded) / keeper_connection_blips: hours with > 1000
// Keeper hardware exceptions, split by whether the host's transactions fell
// under half of its usual (median) hour.
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

// poolSizes is each host's configured pool size. Cloud: the live limit in
// the per-host metric file (max over the window), then server_settings.
// Onprem/gov: server_settings (23.3+), else settings (older servers).
func poolSizes(b *bundle.Bundle, mode string, hours []hostHour, setting, sizeCol string) map[string]float64 {
	sizes := map[string]float64{}
	if mode == "cloud" {
		for _, h := range hours {
			if v := h.r.Float(sizeCol); v > sizes[h.host] {
				sizes[h.host] = v
			}
		}
	}
	fromSettings := 0.0
	for _, c := range []string{"system.server_settings", "system.settings"} {
		rows, err := b.Rows(c)
		if err != nil {
			continue
		}
		found := false
		for _, r := range rows {
			if r.Str("name") == setting {
				found = true
				if v := r.Float("value"); v > fromSettings {
					fromSettings = v
				}
			}
		}
		if found {
			break
		}
	}
	for _, h := range hours {
		if sizes[h.host] == 0 {
			sizes[h.host] = fromSettings
		}
	}
	return sizes
}

// fetch_pool_saturated / schedule_pool_saturated: hosts whose pool averaged
// at least frac of its size for 3 or more of the last 24 hours.
func checkPoolSaturated(b *bundle.Bundle, mode, taskCol, setting, sizeCol string, frac float64) ([]row, error) {
	hours, err := metricHours(b, mode, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	sizes := poolSizes(b, mode, hours, setting, sizeCol)
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

// keeper_xid_renewal_due: a single live Keeper session whose xid, at the
// Keeper request rate of the last 24 hours, reaches 2^31 within a day.
// The covered time is the sum of the hourly buckets' sample spans.
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
	type rate struct{ tx, covered float64 }
	rs := map[string]*rate{}
	for _, h := range hours {
		r := rs[h.host]
		if r == nil {
			r = &rate{}
			rs[h.host] = r
		}
		r.tx += h.r.Float("zk_transactions")
		r.covered += h.r.Float("span_s")
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

// parts_propagation_lag (cloud): replicas ≥ 32 blocks behind the newest
// replica on a partition, or missing a partition written in the last day.
// system.parts_max_block_by_replica holds every lagging (host, partition)
// row, with the partition's replicas_missing_partition on each.
func checkPartsPropagationLag(b *bundle.Bundle, mode string) ([]row, error) {
	rows, err := b.Rows("system.parts_max_block_by_replica")
	if err != nil {
		return nil, err
	}
	now, loc, err := serverClock(b)
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
