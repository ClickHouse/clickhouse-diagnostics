package dashboard

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/bundle"
)

// 从采集器已经写好的文件构建dashboard：采集完成后不再向服务器
// 发送任何查询。每个面板都保留其实时查询的key、列与顺序（见
// collect()），因此页面模板保持不变。源采集器被跳过或失败的面板
// 会在横幅中连同原因一起列出；源在当前服务器上不存在的面板
// （版本不支持或日志表被禁用）则直接为空，与实时查询
// 找不到表时一样。

// WithBundle让生成器改为读取运行目录，而不是查询服务器。不启用它时
// （-output-format为native或tsv、单元测试），面板使用实时查询构建。
func (g *Generator) WithBundle(b *bundle.Bundle) *Generator {
	g.bundle = b
	return g
}

type rows = []map[string]interface{}

// filesRows返回某个采集器的行。被跳过或失败的采集器会在横幅中
// 点名相应面板；从未采集的采集器（当前版本没有、表被禁用）则直接为空。

// stateLabel为横幅渲染采集器状态。存储的状态仍是bundle自己的枚举
// （skipped/failed）；只有显示的词会改变。
func stateLabel(state string) string {
	switch state {
	case "skipped":
		return "跳过"
	case "failed":
		return "失败"
	}
	return state
}

func (g *Generator) filesRows(panel, collector string) ([]bundle.Row, bool) {
	rs, err := g.bundle.Rows(collector)
	if err == nil {
		return rs, true
	}
	var nc *bundle.NotCollected
	if errors.As(err, &nc) {
		if nc.State == "skipped" || nc.State == "failed" {
			g.skippedPanels = append(g.skippedPanels, fmt.Sprintf("%s（%s %s）", panel, collector, stateLabel(nc.State)))
		}
		return nil, false
	}
	fmt.Printf("  [dashboard] %s：%v\n", panel, err)
	return nil, false
}

var notSystemDB = func(db string) bool {
	return db != "system" && db != "information_schema" && db != "INFORMATION_SCHEMA"
}

func (g *Generator) collectFromFiles() map[string]interface{} {
	p := map[string]interface{}{
		"generated_at":    time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		"mode":            g.mode,
		"version":         "",
		"uptime":          g.nodeUptime,
		"node":            g.node,
		"collection":      g.collection,
		"total_databases": 0,
		"total_tables":    0,
		"active_parts":    0,
		"total_size":      "N/A",
	}
	host, collectedAt := "", ""
	if v, ok := g.filesRows("版本", "system.version"); ok && len(v) > 0 {
		p["version"] = v[0].Str("version")
		host = v[0].Str("hostname")
		collectedAt = windowEnd(v[0].Str("collected_at"), v[0].Str("timezone"), g.bundle.WindowEnd())
	}

	tables, haveTables := g.filesRows("表与 part 计数", "system.tables")
	var userTables []bundle.Row
	for _, t := range tables {
		if notSystemDB(t.Str("database")) {
			userTables = append(userTables, t)
		}
	}
	byTable, haveParts := g.filesRows("存储", "system.parts_by_table")

	if haveTables {
		dbs := map[string]bool{}
		for _, t := range userTables {
			dbs[t.Str("database")] = true
		}
		p["total_databases"], p["total_tables"] = int64(len(dbs)), int64(len(userTables))
	}
	if haveParts {
		var parts int64
		var bytes float64
		for _, r := range byTable {
			parts += r.Int("parts")
			bytes += r.Float("bytes_on_disk")
		}
		p["active_parts"], p["total_size"] = parts, readableSize(bytes)
	}

	// ── 存储 ──
	p["storage_by_db"] = storageByDB(byTable)
	p["engines_dist"] = enginesDist(userTables)
	p["tables_list"] = tablesList(userTables, byTable, haveTables, haveParts)
	p["top_tables"] = topTables(byTable)

	// ── 查询日志 ──
	byHour, _ := g.filesRows("查询活动", "system.query_log_by_hour_7_days")
	byPattern, _ := g.filesRows("查询模式", "system.query_log_by_pattern_7_days")
	p["query_by_time"] = queryByTime(byHour)
	p["query_by_kind"] = queryByKind(byHour)
	p["query_slow"] = querySlow(byPattern)
	p["query_heavy"] = queryHeavy(byPattern)
	p["query_by_user"] = queryByUser(byPattern)
	p["exceptions"] = exceptionsByCode(byHour)

	if g.hostInfo != nil {
		g.addHostInfo(p)
	}

	// ── 日志 ──
	if tl, ok := g.filesRows("日志", "system.text_log"); ok {
		p["text_log"] = textLogPanel(tl, host)
		p["text_log_row_cap"] = textLogRowCap
	}

	// ── part日志（3天：采集器的窗口） ──
	partLog, _ := g.filesRows("part 日志", "system.part_log_3_days")
	p["part_log_by_time"], p["part_log_by_type"], p["ttl_activity"] = partLogPanels(partLog)

	// ── 字典 ──
	dicts, _ := g.filesRows("字典", "system.dictionaries")
	p["dictionaries"] = dictionariesPanel(dicts, host)
	redactDictionaryPanel(p["dictionaries"].([]map[string]interface{}))

	// ── 崩溃日志 ──
	crashes, _ := g.filesRows("崩溃日志", "system.crash_log")
	p["crash_log"] = crashLogPanel(crashes)

	// ── 待处理工作 ──
	muts, _ := g.filesRows("mutation", "system.mutations")
	p["mutations"] = mutationsPanel(muts)
	detached, _ := g.filesRows("分离 part", "system.detached_parts")
	p["detached"] = detachedPanel(detached, g.mode)
	if g.mode == "cloud" {
		cl, _ := g.filesRows("集群", "system.clusters")
		p["clusters"] = clustersPanel(cl)
	} else {
		p["clusters"] = rows{}
	}
	rq, _ := g.filesRows("复制队列", "system.replication_queue")
	p["replication_queue"] = replicationQueuePanel(rq, g.mode, host)
	reps, _ := g.filesRows("副本", "system.replicas")
	problem, _ := g.filesRows("副本计数", "system.replicas_problem_tables") // 由Keeper支撑的active/total
	p["replicas"] = replicasPanel(reps, problem, g.mode, host)

	// ── Keeper健康与池 ──
	ml, haveML := g.filesRows("Keeper 与池", "system.metric_log_7_days")
	p["keeper_metric_hourly"], p["keeper_wait_available"], p["keeper_session_available"] = keeperMetricPanel(ml, haveML)
	p["pools_hourly"] = poolsPanel(ml)
	p["pool_sizes"] = g.poolSizesFromFiles()
	p["pools_now"] = collectedAt

	if el, ok := g.filesRows("Keeper 错误", "system.error_log_7_days"); ok {
		p["keeper_errors_hourly"] = keeperErrorsFromErrorLog(el)
		p["keeper_errors_source"] = "system.error_log — 包含所有线程，包括后台 merge 和 fetch"
	} else {
		p["keeper_errors_hourly"] = keeperErrorsFromQueryLog(byHour)
		p["keeper_errors_source"] = "system.query_log — 仅查询；后台 merge、fetch 和 DDL worker 未计入（system.error_log 需要 24.8+）"
	}
	zk, _ := g.filesRows("Keeper 连接", "system.zookeeper_connection")
	p["keeper_connection"] = keeperConnectionPanel(zk, g.mode)

	// ── 磁盘、错误、part计数、异步插入 ──
	disks, _ := g.filesRows("磁盘", "system.disks")
	p["disks"] = disksPanel(disks, host)
	errs, _ := g.filesRows("服务器错误", "system.errors")
	p["server_errors"] = serverErrorsPanel(errs)
	pbp, _ := g.filesRows("part 数过多", "system.parts_by_partition")
	p["high_part_count"] = highPartCount(pbp)
	if g.mode != "gov" {
		ai, _ := g.filesRows("异步插入", "system.asynchronous_insert_log_7_days")
		p["async_inserts"] = asyncInsertsPanel(ai, collectedAt)
	} else {
		p["async_inserts"] = rows{}
	}

	p["windows"] = map[string]string{"7d": g.windowLabel("最近 7 天"), "3d": g.windowLabel("最近 3 天")}

	g.collectAnalysisFromFiles(p)
	return p
}

// WithWindow记录显式的-from/-to，用于面板标题。
func (g *Generator) WithWindow(from, to time.Time) *Generator {
	g.windowFrom, g.windowTo = from, to
	return g
}

// windowLabel描述使用采集器默认回溯定义构建的面板实际
// 覆盖的范围：显式的-from/-to窗口、essential运行把每个窗口
// 截断到的1天，或默认值。
func (g *Generator) windowLabel(def string) string {
	const f = "2006-01-02 15:04 UTC"
	switch {
	case !g.windowFrom.IsZero() && !g.windowTo.IsZero():
		return g.windowFrom.UTC().Format(f) + " – " + g.windowTo.UTC().Format(f)
	case !g.windowFrom.IsZero():
		return "自 " + g.windowFrom.UTC().Format(f)
	case !g.windowTo.IsZero():
		return "截至 " + g.windowTo.UTC().Format(f)
	case g.essential():
		return "最近一天（essential 运行）"
	}
	return def
}

// ── 辅助函数 ──────────────────────────────────────────────────────────────

// windowEnd是已采集日志窗口的终点，采用服务器时区的DateTime：
// 采集时服务器的now()，或更早时显式的-to——文件在那里结束，
// 因此“最近24小时”也在那里结束。
func windowEnd(collectedAt, tz string, to time.Time) string {
	if to.IsZero() {
		return collectedAt
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05", collectedAt, loc)
	if err != nil || !to.Before(now) {
		return collectedAt
	}
	return to.In(loc).Format("2006-01-02 15:04:05")
}

// readableSize即ClickHouse的formatReadableSize：以1024为底、两位小数（“1.50 GiB”）。
func readableSize(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := 0
	for math.Abs(n) >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", n, units[i])
}

// readableQuantity即ClickHouse的formatReadableQuantity（“1.99 million”）。
func readableQuantity(n float64) string {
	units := []string{"", " thousand", " million", " billion", " trillion", " quadrillion"}
	i := 0
	for math.Abs(n) >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	return fmt.Sprintf("%.2f%s", n, units[i])
}

// round即ClickHouse对Float64的round(x, d)（平局取偶）。
func round(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.RoundToEven(x*p) / p
}

func ratio(a, b float64) float64 {
	if b > 0 {
		return round(a/b, 2)
	}
	return 0
}

// leftRunes即leftUTF8：至多n个字符，绝不拆开一个rune。
func leftRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func limit(rs rows, n int) rows {
	if len(rs) > n {
		return rs[:n]
	}
	return rs
}

// byNum按数值列降序排序，稳定排序。
func byNum(rs rows, col string) {
	sort.SliceStable(rs, func(i, j int) bool { return num(rs[i][col]) > num(rs[j][col]) })
}

func num(v interface{}) float64 {
	switch x := v.(type) {
	case int64:
		return float64(x)
	case float64:
		return x
	case int:
		return float64(x)
	}
	return bundle.Row{"v": v}.Float("v")
}

func strs(v interface{}) []string {
	arr, _ := v.([]interface{})
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// hourOf解析采集到的DateTime（“YYYY-MM-DD hh:mm:ss”）。
func hourOf(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	return t, err == nil
}

// ── 存储 ──

func storageByDB(byTable []bundle.Row) rows {
	type agg struct{ parts, rows, bytes, comp, uncomp float64 }
	m := map[string]*agg{}
	for _, r := range byTable {
		a := m[r.Str("database")]
		if a == nil {
			a = &agg{}
			m[r.Str("database")] = a
		}
		a.parts += r.Float("parts")
		a.rows += r.Float("rows")
		a.bytes += r.Float("bytes_on_disk")
		a.comp += r.Float("data_compressed_bytes")
		a.uncomp += r.Float("data_uncompressed_bytes")
	}
	out := rows{}
	for db, a := range m {
		out = append(out, map[string]interface{}{"database": db, "parts": int64(a.parts), "rows": int64(a.rows),
			"bytes_total": int64(a.bytes), "size_human": readableSize(a.bytes), "compression_ratio": ratio(a.uncomp, a.comp)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["database"].(string) < out[j]["database"].(string) })
	byNum(out, "bytes_total")
	return limit(out, 20)
}

func enginesDist(userTables []bundle.Row) rows {
	m := map[string]int64{}
	for _, t := range userTables {
		m[t.Str("engine")]++
	}
	out := rows{}
	for e, n := range m {
		out = append(out, map[string]interface{}{"engine": e, "count": n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["engine"].(string) < out[j]["engine"].(string) })
	byNum(out, "count")
	return limit(out, 15)
}

// tablesList：每个用户表及其大小。没有parts_by_table时，
// 大小为“n/a”，绝不用0来表示空表。
func tablesList(userTables, byTable []bundle.Row, haveTables, haveParts bool) rows {
	out := rows{}
	if !haveTables {
		return out
	}
	sizes := map[string]bundle.Row{}
	for _, r := range byTable {
		sizes[r.Str("database")+"\x00"+r.Str("table")] = r
	}
	for _, t := range userTables {
		s := sizes[t.Str("database")+"\x00"+t.Str("name")]
		out = append(out, map[string]interface{}{"database": t.Str("database"), "table_name": t.Str("name"), "engine": t.Str("engine"),
			"parts": s.Int("parts"), "total_rows": readableQuantity(s.Float("rows")), "size": readableSize(s.Float("bytes_on_disk")),
			"bytes_on_disk": s.Int("bytes_on_disk"), "partition_key": t.Str("partition_key"), "sorting_key": t.Str("sorting_key"),
			"storage_policy": t.Str("storage_policy")})
		if !haveParts {
			r := out[len(out)-1]
			r["parts"], r["total_rows"], r["size"] = "n/a", "n/a", "n/a"
		}
	}
	byNum(out, "bytes_on_disk")
	return limit(out, 2000)
}

func topTables(byTable []bundle.Row) rows {
	out := rows{}
	for _, r := range byTable {
		out = append(out, map[string]interface{}{"database": r.Str("database"), "table": r.Str("table"), "parts": r.Int("parts"),
			"total_rows": readableQuantity(r.Float("rows")), "compressed_size": readableSize(r.Float("bytes_on_disk")),
			"compression_ratio": ratio(r.Float("data_uncompressed_bytes"), r.Float("data_compressed_bytes")),
			"_bytes":            r.Float("bytes_on_disk")})
	}
	byNum(out, "_bytes")
	out = limit(out, 20)
	for _, r := range out {
		delete(r, "_bytes")
	}
	return out
}

// ── 查询日志 ──

func isException(typ string) bool {
	return typ == "ExceptionWhileProcessing" || typ == "ExceptionBeforeStart"
}

func queryByTime(byHour []bundle.Row) rows {
	type key struct{ t, kind string }
	m := map[key]int64{}
	for _, r := range byHour {
		if r.Str("type") == "QueryFinish" {
			m[key{r.Str("time"), r.Str("query_kind")}] += r.Int("count")
		}
	}
	out := rows{}
	for k, n := range m {
		out = append(out, map[string]interface{}{"time": k.t, "query_kind": k.kind, "count": n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["time"] != out[j]["time"] {
			return out[i]["time"].(string) < out[j]["time"].(string)
		}
		return out[i]["query_kind"].(string) < out[j]["query_kind"].(string)
	})
	return out
}

func queryByKind(byHour []bundle.Row) rows {
	type agg struct{ n, dur, mem float64 }
	m := map[string]*agg{}
	for _, r := range byHour {
		if r.Str("type") != "QueryFinish" {
			continue
		}
		a := m[r.Str("query_kind")]
		if a == nil {
			a = &agg{}
			m[r.Str("query_kind")] = a
		}
		a.n += r.Float("count")
		a.dur += r.Float("query_duration_ms")
		a.mem += r.Float("memory_usage")
	}
	out := rows{}
	for k, a := range m {
		out = append(out, map[string]interface{}{"query_kind": k, "count": int64(a.n),
			"avg_duration_ms": round(a.dur/math.Max(a.n, 1), 0), "avg_memory_mb": round(a.mem/math.Max(a.n, 1)/1048576, 2)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["query_kind"].(string) < out[j]["query_kind"].(string) })
	byNum(out, "count")
	return out
}

// patternAgg按（hash、kind、user）折叠query_log_by_pattern_7_days的行。
type patternAgg struct {
	hash, kind, user, sample         string
	finish, timed, errors            float64
	dur, timedDur, maxDur, read, mem float64
	timedRead, timedMem              float64
}

func foldPatterns(byPattern []bundle.Row, key func(r bundle.Row) string) (map[string]*patternAgg, []string) {
	m := map[string]*patternAgg{}
	var order []string
	for _, r := range byPattern {
		k := key(r)
		a := m[k]
		if a == nil {
			a = &patternAgg{hash: r.Str("hash"), kind: r.Str("query_kind"), user: r.Str("user")}
			m[k] = a
			order = append(order, k)
		}
		switch typ := r.Str("type"); {
		case typ == "QueryFinish":
			a.finish += r.Float("count")
			a.timed += r.Float("timed_count")
			a.dur += r.Float("total_duration_ms")
			a.read += r.Float("total_read_bytes")
			a.mem += r.Float("total_memory_usage")
			a.timedRead += r.Float("timed_read_bytes")
			a.timedMem += r.Float("timed_memory_usage")
			if d := r.Float("max_duration_ms"); d > a.maxDur {
				a.maxDur = d
			}
			if a.sample == "" || a.sample == "(redacted)" {
				a.sample = r.Str("sample_query")
			}
		case isException(typ):
			a.errors += r.Float("count")
			if a.sample == "" {
				a.sample = r.Str("sample_query")
			}
		}
	}
	return m, order
}

func patternKey(r bundle.Row) string {
	return r.Str("hash") + "\x00" + r.Str("query_kind") + "\x00" + r.Str("user")
}

// querySlow：按模式统计有实测耗时的QueryFinish行与失败，
// 平均最慢的排在前面。
func querySlow(byPattern []bundle.Row) rows {
	m, order := foldPatterns(byPattern, patternKey)
	out := rows{}
	for _, k := range order {
		a := m[k]
		if a.timed == 0 && a.errors == 0 {
			continue
		}
		den := math.Max(a.timed, 1)
		out = append(out, map[string]interface{}{"hash": a.hash, "query_kind": a.kind, "user": a.user,
			"executions": int64(a.timed), "avg_duration_ms": round(a.dur/den, 0), "max_duration_ms": int64(a.maxDur),
			"avg_read_mb": round(a.timedRead/den/1048576, 2), "avg_memory_mb": round(a.timedMem/den/1048576, 2),
			"errors": int64(a.errors), "sample_query": a.sample})
	}
	byNum(out, "avg_duration_ms")
	return limit(out, 20)
}

// queryHeavy：按模式统计，只包含QueryFinish，每次执行读取量最大的排在前面。
func queryHeavy(byPattern []bundle.Row) rows {
	m, order := foldPatterns(byPattern, patternKey)
	out := rows{}
	for _, k := range order {
		a := m[k]
		if a.finish == 0 {
			continue
		}
		out = append(out, map[string]interface{}{"hash": a.hash, "query_kind": a.kind, "user": a.user,
			"executions": int64(a.finish), "avg_read_mb": round(a.read/a.finish/1048576, 2),
			"total_read": readableSize(a.read), "avg_duration_ms": round(a.dur/a.finish, 0), "sample_query": a.sample})
	}
	byNum(out, "avg_read_mb")
	return limit(out, 20)
}

func queryByUser(byPattern []bundle.Row) rows {
	m, order := foldPatterns(byPattern, func(r bundle.Row) string { return r.Str("user") })
	out := rows{}
	for _, k := range order {
		a := m[k]
		out = append(out, map[string]interface{}{"user": a.user, "executions": int64(a.finish),
			"avg_duration_ms": round(a.dur/math.Max(a.finish, 1), 0), "total_read_gb": round(a.read/1073741824, 3),
			"total_memory_gb": round(a.mem/1073741824, 3), "error_count": int64(a.errors)})
	}
	byNum(out, "executions")
	return limit(out, 20)
}

func exceptionsByCode(byHour []bundle.Row) rows {
	type agg struct {
		n   int64
		msg string
	}
	m := map[int64]*agg{}
	for _, r := range byHour {
		c := r.Int("exception_code")
		if c == 0 {
			continue
		}
		a := m[c]
		if a == nil {
			a = &agg{}
			m[c] = a
		}
		a.n += r.Int("count")
		if a.msg == "" {
			a.msg = r.Str("exception")
		}
	}
	out := rows{}
	for c, a := range m {
		out = append(out, map[string]interface{}{"exception_code": c, "msg": a.msg, "count": a.n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["exception_code"].(int64) < out[j]["exception_code"].(int64) })
	byNum(out, "count")
	return limit(out, 15)
}

// ── 日志 ──

// textLogPanel：Warning及更严重级别，最新的在前。onprem行不带
// hostname（单服务器）：使用采集到的主机名，与实时面板显示的一致。
func textLogPanel(tl []bundle.Row, host string) rows {
	keep := map[string]bool{"Warning": true, "Error": true, "Critical": true, "Fatal": true}
	out := rows{}
	for _, r := range tl {
		if !keep[r.Str("level")] {
			continue
		}
		e := map[string]interface{}{"event_time": r.Str("event_time"), "level": r.Str("level"),
			"logger_name": r.Str("logger_name"), "message": leftRunes(r.Str("message"), textLogMessageCap)}
		if h := r.Str("hostname"); h != "" {
			e["hostname"] = h
		} else if host != "" {
			e["hostname"] = host
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["event_time"].(string) > out[j]["event_time"].(string) })
	return limit(out, textLogRowCap)
}

// ── part日志 ──

func partLogPanels(pl []bundle.Row) (rows, rows, rows) {
	type dk struct{ day, typ string }
	byDay := map[dk]int64{}
	type tk struct{ n, size float64 }
	byType := map[string]*tk{}
	type ttlk struct{ day, typ, reason string }
	ttl := map[ttlk]*tk{}
	for _, r := range pl {
		day := r.Str("time")
		if t, ok := hourOf(day); ok {
			day = t.Format("2006-01-02") + " 00:00:00"
		}
		typ, n, size := r.Str("event_type"), r.Float("count"), r.Float("size_in_bytes")
		byDay[dk{day, typ}] += int64(n)
		a := byType[typ]
		if a == nil {
			a = &tk{}
			byType[typ] = a
		}
		a.n += n
		a.size += size
		reason := r.Str("merge_reason")
		if reason == "TTLDeleteMerge" || reason == "TTLRecompressMerge" || typ == "RemovePart" {
			k := ttlk{day, typ, reason}
			b := ttl[k]
			if b == nil {
				b = &tk{}
				ttl[k] = b
			}
			b.n += n
			b.size += size
		}
	}
	t1 := rows{}
	for k, n := range byDay {
		t1 = append(t1, map[string]interface{}{"time": k.day, "event_type": k.typ, "count": n})
	}
	sort.Slice(t1, func(i, j int) bool {
		if t1[i]["time"] != t1[j]["time"] {
			return t1[i]["time"].(string) < t1[j]["time"].(string)
		}
		return t1[i]["event_type"].(string) < t1[j]["event_type"].(string)
	})
	t2 := rows{}
	for typ, a := range byType {
		t2 = append(t2, map[string]interface{}{"event_type": typ, "count": int64(a.n), "total_size": readableSize(a.size)})
	}
	sort.Slice(t2, func(i, j int) bool { return t2[i]["event_type"].(string) < t2[j]["event_type"].(string) })
	byNum(t2, "count")
	t3 := rows{}
	for k, a := range ttl {
		t3 = append(t3, map[string]interface{}{"day": k.day, "event_type": k.typ, "merge_reason": k.reason,
			"events": int64(a.n), "total_size": readableSize(a.size)})
	}
	sort.Slice(t3, func(i, j int) bool {
		a, b := t3[i], t3[j]
		if a["day"] != b["day"] {
			return a["day"].(string) < b["day"].(string)
		}
		if a["event_type"] != b["event_type"] {
			return a["event_type"].(string) < b["event_type"].(string)
		}
		return a["merge_reason"].(string) < b["merge_reason"].(string)
	})
	return t1, t2, t3
}

// ── 字典、崩溃、待处理工作 ──

func dictionariesPanel(ds []bundle.Row, host string) rows {
	out := rows{}
	for _, d := range ds {
		h := d.Str("hostname")
		if h == "" {
			h = host
		}
		out = append(out, map[string]interface{}{
			"hostname": h, "database": d.Str("database"), "name": d.Str("name"), "status": d.Str("status"),
			"type": d.Str("type"), "uuid": d.Str("uuid"), "bytes_allocated": d.Int("bytes_allocated"),
			"bytes_allocated_human": readableSize(d.Float("bytes_allocated")),
			"element_count":         d.Int("element_count"), "query_count": d.Int("query_count"), "error_count": d.Int("error_count"),
			"hit_rate_pct": round(d.Float("hit_rate")*100, 2), "found_rate_pct": round(d.Float("found_rate")*100, 2),
			"load_factor": round(d.Float("load_factor"), 4),
			"key_names":   strings.Join(strs(d["key.names"]), ", "), "key_types": strings.Join(strs(d["key.types"]), ", "),
			"attribute_names": strings.Join(strs(d["attribute.names"]), ", "), "attribute_types": strings.Join(strs(d["attribute.types"]), ", "),
			"lifetime_min": d.Int("lifetime_min"), "lifetime_max": d.Int("lifetime_max"),
			"loading_start_time": d.Str("loading_start_time"), "last_update": d.Str("last_successful_update_time"),
			"loading_duration_s": round(d.Float("loading_duration"), 2),
			"source":             d.Str("source"), "origin": d.Str("origin"), "comment": d.Str("comment"), "last_exception": d.Str("last_exception"),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		for _, c := range []string{"database", "name", "hostname"} {
			if out[i][c] != out[j][c] {
				return out[i][c].(string) < out[j][c].(string)
			}
		}
		return false
	})
	return out
}

func crashLogPanel(cs []bundle.Row) rows {
	out := rows{}
	for _, c := range cs {
		out = append(out, map[string]interface{}{"event_time": c.Str("event_time"), "signal": c.Int("signal"),
			"thread_id": c.Str("thread_id"), "query_id": c.Str("query_id"), "version": c.Str("version")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["event_time"].(string) > out[j]["event_time"].(string) })
	return limit(out, 100)
}

// mutationsPanel：未完成且未被终止的mutation。cloud会按副本各列一次，
// 每个副本有自己的积压：这里只保留一次，取最大的parts_to_do。
func mutationsPanel(ms []bundle.Row) rows {
	out := rows{}
	seen := map[string]map[string]interface{}{}
	for _, m := range ms {
		if m.Int("parts_to_do") <= 0 || m.Int("is_killed") == 1 {
			continue
		}
		k := m.Str("database") + "\x00" + m.Str("table") + "\x00" + m.Str("mutation_id")
		if e, ok := seen[k]; ok {
			if n := m.Int("parts_to_do"); n > e["parts_to_do"].(int64) {
				e["parts_to_do"] = n
			}
			continue
		}
		e := map[string]interface{}{"database": m.Str("database"), "table": m.Str("table"),
			"mutation_id": m.Str("mutation_id"), "command": m.Str("command"), "create_time": m.Str("create_time"),
			"parts_to_do": m.Int("parts_to_do")}
		seen[k] = e
		out = append(out, e)
	}
	byNum(out, "parts_to_do")
	return limit(out, 20)
}

// detachedPanel：按表统计，最大的在前；仅当服务器报告bytes_on_disk
// （22.11+）时才给出大小。cloud会按副本各列一次共享part。
func detachedPanel(ds []bundle.Row, mode string) rows {
	type agg struct {
		db, tb  string
		n       int64
		bytes   float64
		sized   bool
		reasons map[string]bool
	}
	m := map[string]*agg{}
	var order []string
	paths := map[string]bool{}
	for _, d := range ds {
		if !notSystemDB(d.Str("database")) {
			continue
		}
		if p := d.Str("path"); mode == "cloud" && p != "" {
			if paths[p] {
				continue
			}
			paths[p] = true
		}
		k := d.Str("database") + "\x00" + d.Str("table")
		a := m[k]
		if a == nil {
			a = &agg{db: d.Str("database"), tb: d.Str("table"), reasons: map[string]bool{}}
			m[k] = a
			order = append(order, k)
		}
		a.n++
		if _, ok := d["bytes_on_disk"]; ok {
			a.sized = true
			a.bytes += d.Float("bytes_on_disk")
		}
		a.reasons[d.Str("reason")] = true
	}
	out := rows{}
	for _, k := range order {
		a := m[k]
		var rs []string
		for r := range a.reasons {
			rs = append(rs, r)
		}
		sort.Strings(rs)
		size, ord := "n/a", float64(a.n)
		if a.sized {
			size, ord = readableSize(a.bytes), a.bytes
		}
		out = append(out, map[string]interface{}{"database": a.db, "table": a.tb, "count": a.n, "size": size,
			"reasons": strings.Join(rs, ", "), "_ord": ord})
	}
	byNum(out, "_ord")
	out = limit(out, 20)
	for _, r := range out {
		delete(r, "_ord")
	}
	return out
}

func clustersPanel(cs []bundle.Row) rows {
	out := rows{}
	for _, c := range cs {
		out = append(out, map[string]interface{}{"cluster": c.Str("cluster"), "host_name": c.Str("host_name"),
			"shard": c.Str("shard_num"), "replica": c.Str("replica_num"), "errors_count": c.Str("errors_count")})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a["cluster"] != b["cluster"] {
			return a["cluster"].(string) < b["cluster"].(string)
		}
		if num(a["shard"]) != num(b["shard"]) {
			return num(a["shard"]) < num(b["shard"])
		}
		return num(a["replica"]) < num(b["replica"])
	})
	return out
}

// replicationQueuePanel：服务中副本的队列，与实时面板读取的一致
// （cloud文件包含每个副本的队列）。
func replicationQueuePanel(rq []bundle.Row, mode, host string) rows {
	out := rows{}
	for _, r := range rq {
		if mode == "cloud" && host != "" && r.Str("hostname") != "" && r.Str("hostname") != host {
			continue
		}
		out = append(out, map[string]interface{}{"table": r.Str("table"), "type": r.Str("type"),
			"executing": r.Str("is_currently_executing"), "last_exception": r.Str("last_exception")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["table"].(string) < out[j]["table"].(string) })
	return limit(out, 50)
}

// replicasPanel：服务节点的副本（实时面板读取本地
// system.replicas；cloud文件包含每个副本），并在
// replicas_problem_tables有数据处附上由Keeper支撑的副本计数。
func replicasPanel(rs, problem []bundle.Row, mode, host string) rows {
	counts := map[string][2]string{}
	for _, p := range problem {
		if mode == "cloud" && host != "" && p.Str("hostname") != "" && p.Str("hostname") != host {
			continue
		}
		counts[p.Str("database")+"\x00"+p.Str("table")] = [2]string{p.Str("total_replicas"), p.Str("active_replicas")}
	}
	out := rows{}
	for _, r := range rs {
		if mode == "cloud" && host != "" && r.Str("hostname") != "" && r.Str("hostname") != host {
			continue
		}
		c := counts[r.Str("database")+"\x00"+r.Str("table")]
		out = append(out, map[string]interface{}{"database": r.Str("database"), "table": r.Str("table"),
			"is_leader": r.Int("is_leader"), "is_readonly": r.Int("is_readonly"), "is_session_expired": r.Int("is_session_expired"),
			"future_parts": r.Int("future_parts"), "parts_to_check": r.Int("parts_to_check"), "queue_size": r.Int("queue_size"),
			"inserts_in_queue": r.Int("inserts_in_queue"), "merges_in_queue": r.Int("merges_in_queue"),
			"queue_oldest_time": r.Str("queue_oldest_time"), "absolute_delay": r.Int("absolute_delay"),
			"total_replicas": c[0], "active_replicas": c[1]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if num(out[i]["absolute_delay"]) != num(out[j]["absolute_delay"]) {
			return num(out[i]["absolute_delay"]) > num(out[j]["absolute_delay"])
		}
		return num(out[i]["is_readonly"]) > num(out[j]["is_readonly"])
	})
	return out
}

// ── Keeper与池 ──

func keeperMetricPanel(ml []bundle.Row, have bool) (rows, bool, bool) {
	out := rows{}
	hasWait, hasSession := false, false
	for _, r := range ml {
		_, w := r["zk_wait_us"]
		_, s := r["zk_sessions_max"]
		hasWait, hasSession = hasWait || w, hasSession || s
		out = append(out, map[string]interface{}{"time": r.Str("time"), "transactions": r.Int("zk_transactions"),
			"hw_exceptions": r.Int("zk_hw_exceptions"), "user_exceptions": r.Int("zk_user_exceptions"),
			"wait_us": r.Int("zk_wait_us"), "sessions_min": r.Int("zk_sessions_min"), "sessions_max": r.Int("zk_sessions_max")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["time"].(string) < out[j]["time"].(string) })
	return out, have && hasWait, have && hasSession
}

func poolsPanel(ml []bundle.Row) rows {
	out := rows{}
	for _, r := range ml {
		out = append(out, map[string]interface{}{"time": r.Str("time"),
			"fetch_tasks": r.Float("max_replica_avg_fetch_pool_tasks"), "schedule_tasks": r.Float("max_replica_avg_schedule_pool_tasks"),
			"merge_tasks": r.Float("max_replica_avg_merge_pool_tasks")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["time"].(string) < out[j]["time"].(string) })
	return out
}

// poolSizesFromFiles：配置的大小，在23.3+取自server_settings，
// 更低版本取自settings（那里的system.settings仍以旧的会话默认值
// 列出这些名称）。在cloud中，若已采集，fetch和schedule大小取
// 各副本中最小的实时大小（metric_log_by_host_3_days）——
// server_settings来自一个副本，而图表序列是最差副本，
// 因此最小的池就是它的参考线。
func (g *Generator) poolSizesFromFiles() map[string]int64 {
	out := map[string]int64{"fetch": 0, "schedule": 0, "merge": 0}
	settings := map[string]string{"fetch": "background_fetches_pool_size", "schedule": "background_schedule_pool_size", "merge": "background_pool_size"}
	if g.mode == "cloud" {
		// 不是merge池：其实时大小是background_pool_size乘以
		// background_merges_mutations_concurrency_ratio，而图表的
		// 参考线是该设置。
		cols := map[string]string{"fetch": "max(CurrentMetric_BackgroundFetchesPoolSize)", "schedule": "max(CurrentMetric_BackgroundSchedulePoolSize)"}
		if hosts, err := g.bundle.Rows("system.metric_log_by_host_3_days"); err == nil {
			for key, col := range cols {
				// 取每台主机最新的值，而不是窗口内的最大值：
				// 从32调小到16的池应按16评判。
				perHost, latest := map[string]int64{}, map[string]string{}
				for _, r := range hosts {
					h, t := r.Str("hostname"), r.Str("time")
					if v := r.Int(col); v > 0 && t >= latest[h] {
						perHost[h], latest[h] = v, t
					}
				}
				for _, v := range perHost {
					if v > 0 && (out[key] == 0 || v < out[key]) {
						out[key] = v
					}
				}
			}
		}
	}
	source := "system.server_settings"
	if v := g.serverVersion; v != (internal.Version{}) && (v.Major < 23 || (v.Major == 23 && v.Minor < 3)) && g.mode != "cloud" {
		source = "system.settings"
	}
	rs, err := g.bundle.Rows(source)
	if err != nil {
		return out
	}
	for key, name := range settings {
		if out[key] != 0 {
			continue
		}
		for _, r := range rs {
			if r.Str("name") == name {
				out[key] = r.Int("value")
			}
		}
	}
	return out
}

var keeperErrorCodes = map[int64]string{999: "KEEPER_EXCEPTION", 242: "TABLE_IS_READ_ONLY", 319: "UNKNOWN_STATUS_OF_INSERT",
	571: "DATABASE_REPLICATION_FAILED", 252: "TOO_MANY_PARTS"}

// timeName是一个错误码名称的一个小时。
type timeName struct{ t, name string }

func keeperErrorsFromErrorLog(el []bundle.Row) rows {
	m := map[timeName]int64{}
	for _, r := range el {
		if _, ok := keeperErrorCodes[r.Int("code")]; ok {
			m[timeName{r.Str("time"), r.Str("error")}] += r.Int("errors")
		}
	}
	return keeperErrorRows(m)
}

func keeperErrorsFromQueryLog(byHour []bundle.Row) rows {
	m := map[timeName]int64{}
	for _, r := range byHour {
		if name, ok := keeperErrorCodes[r.Int("exception_code")]; ok && r.Str("type") == "ExceptionWhileProcessing" {
			m[timeName{r.Str("time"), name}] += r.Int("count")
		}
	}
	return keeperErrorRows(m)
}

func keeperErrorRows(m map[timeName]int64) rows {
	out := rows{}
	for k, n := range m {
		out = append(out, map[string]interface{}{"time": k.t, "code_name": k.name, "count": n})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i]["time"] != out[j]["time"] {
			return out[i]["time"].(string) < out[j]["time"].(string)
		}
		return out[i]["code_name"].(string) < out[j]["code_name"].(string)
	})
	return out
}

func keeperConnectionPanel(zk []bundle.Row, mode string) rows {
	out := rows{}
	for _, r := range zk {
		e := map[string]interface{}{"name": r.Str("name"), "host": r.Str("host"), "port": r.Str("port"), "index": r.Str("index"),
			"connected_time": r.Str("connected_time"), "session_uptime_s": r.Str("session_uptime_elapsed_seconds"),
			"is_expired": r.Str("is_expired"), "api_version": r.Str("keeper_api_version")}
		if mode == "cloud" {
			e["replica"] = r.Str("hostname")
		}
		if _, ok := r["xid"]; ok {
			e["xid"] = r.Str("xid")
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

// ── 磁盘、错误、part、异步插入 ──

func disksPanel(ds []bundle.Row, host string) rows {
	out := rows{}
	for _, d := range ds {
		h := d.Str("hostname")
		if h == "" {
			h = host
		}
		free, total := d.Float("free_bytes"), d.Float("total_bytes")
		pct := 0.0
		if total > 0 {
			pct = round(free/total*100, 1)
		}
		out = append(out, map[string]interface{}{"hostname": h, "name": d.Str("name"), "path": d.Str("path"), "type": d.Str("type"),
			// 原始容量保持精确的UInt64文本：对象存储磁盘用2^64-1表示
			// “无限”，超出int64能容纳的范围。
			"free_space": d.Str("free_bytes"), "total_space": d.Str("total_bytes"), "free_space_human": readableSize(free),
			"total_space_human": readableSize(total), "free_pct": pct})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i]["hostname"] != out[j]["hostname"] {
			return out[i]["hostname"].(string) < out[j]["hostname"].(string)
		}
		return num(out[i]["total_space"]) > num(out[j]["total_space"])
	})
	return out
}

// serverErrorsPanel把各副本的计数器（cloud）折叠为每个错误一行，
// 最频繁的在前。
func serverErrorsPanel(es []bundle.Row) rows {
	type agg struct {
		name, code    string
		total, maxVal int64
		last, lastMsg string
	}
	m := map[string]*agg{}
	var order []string
	for _, e := range es {
		k := e.Str("name") + "\x00" + e.Str("code")
		a := m[k]
		if a == nil {
			a = &agg{name: e.Str("name"), code: e.Str("code"), maxVal: -1}
			m[k] = a
			order = append(order, k)
		}
		v := e.Int("value")
		a.total += v
		if t := e.Str("last_error_time"); t > a.last {
			a.last = t
		}
		if v > a.maxVal {
			a.maxVal, a.lastMsg = v, e.Str("last_error_message")
		}
	}
	out := rows{}
	for _, k := range order {
		a := m[k]
		if a.total <= 0 {
			continue
		}
		out = append(out, map[string]interface{}{"name": a.name, "code": bundle.Row{"c": a.code}.Int("c"), "value": a.total,
			"last_error_time": a.last, "last_error_message": leftRunes(a.lastMsg, 300)})
	}
	byNum(out, "value")
	return limit(out, 30)
}

func highPartCount(pbp []bundle.Row) rows {
	out := rows{}
	for _, r := range pbp {
		if r.Int("parts") > 100 {
			out = append(out, map[string]interface{}{"database": r.Str("database"), "table": r.Str("table"),
				"partition_id": r.Str("partition_id"), "parts_count": r.Int("parts")})
		}
	}
	byNum(out, "parts_count")
	return limit(out, 30)
}

// asyncInsertsPanel：最近24小时（截至采集时的now）按小时和状态统计；
// flush延迟是按各表flush次数加权的平均值。
func asyncInsertsPanel(ai []bundle.Row, collectedAt string) rows {
	now, ok := hourOf(collectedAt)
	type key struct{ hour, status string }
	type agg struct {
		flushes, rows, bytes, msWeighted float64
		hasRows                          bool
	}
	m := map[key]*agg{}
	for _, r := range ai {
		t, tok := hourOf(r.Str("time"))
		if ok && tok && !t.Add(time.Hour).After(now.Add(-24*time.Hour)) {
			continue
		}
		k := key{r.Str("time"), r.Str("status")}
		a := m[k]
		if a == nil {
			a = &agg{}
			m[k] = a
		}
		f := r.Float("flushes")
		a.flushes += f
		a.bytes += r.Float("total_bytes")
		a.msWeighted += r.Float("avg_flush_ms") * f
		if _, has := r["total_rows"]; has {
			a.hasRows = true
			a.rows += r.Float("total_rows")
		}
	}
	out := rows{}
	for k, a := range m {
		totalRows := "n/a"
		if a.hasRows {
			totalRows = fmt.Sprintf("%d", int64(a.rows))
		}
		out = append(out, map[string]interface{}{"hour": k.hour, "status": k.status, "flushes": int64(a.flushes),
			"total_rows": totalRows, "total_bytes": readableSize(a.bytes), "avg_flush_ms": round(a.msWeighted/math.Max(a.flushes, 1), 0)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i]["hour"] != out[j]["hour"] {
			return out[i]["hour"].(string) < out[j]["hour"].(string)
		}
		return out[i]["status"].(string) < out[j]["status"].(string)
	})
	return out
}

// ── 查询分析 ──

// collectAnalysisFromFiles把分析采集器写入的query_analysis/文件嵌入，
// 使用与实时面板相同的payload键。
func (g *Generator) collectAnalysisFromFiles(p map[string]interface{}) {
	if !g.analysis.Enabled() {
		return
	}
	dir := filepath.Join(g.bundle.Dir(), "query_analysis")
	if _, err := os.Stat(dir); err != nil {
		return
	}
	ab := bundle.Open(dir, nil)
	files := map[string]string{
		"qa_details": "query_details", "qa_profile": "profile_events", "qa_text_parts": "text_log_parts",
		"qa_text_full": "text_log_full", "qa_tables": "tables_for_query", "qa_fast_slow": "fast_slow_query_ids",
		"qa_pe_compare": "profile_events_compare", "qa_by_host": "hash_by_host", "qa_summary": "hash_summary",
		"qa_failed_over_time": "failed_over_time", "qa_failed": "failed_queries", "qa_executions": "executions_timeline",
	}
	for key, base := range files {
		rs, err := ab.Rows(base)
		if err != nil {
			continue
		}
		out := make(rows, 0, len(rs))
		for _, r := range rs {
			m := map[string]interface{}(r)
			if g.mode == "gov" {
				for _, sensitive := range []string{"query", "exception", "sample_query", "sample_exception"} {
					if _, ok := m[sensitive]; ok {
						m[sensitive] = "(redacted in gov mode)"
					}
				}
			}
			out = append(out, m)
		}
		p[key] = out
	}
	p["qa_enabled"] = true
	p["qa_query_id"] = g.analysis.QueryID
	p["qa_hash"] = g.analysis.NormalizedQueryHash
	p["qa_from"] = g.analysis.From.UTC().Format(time.RFC3339)
	p["qa_to"] = g.analysis.To.UTC().Format(time.RFC3339)
	p["qa_mode"] = g.mode
}

// ── 模式关系图 ──

// schemaGraphFromFiles用采集到的system.tables / columns /
// data_skipping_indices / dictionaries / view_refreshes构建模式页面，
// 列采用实时查询的列。system.tables未采集时返回nil（没有可绘制的内容）。
// DDL是服务器的原始文本：实时页面用formatQueryOrNull
// 对其做了美化，而只有查询才能做到这一点。
func (g *Generator) schemaGraphFromFiles(version string) map[string]interface{} {
	ts, ok := g.filesRows("模式关系图", "system.tables")
	if !ok {
		return nil
	}
	tables := rows{}
	hasTarget := false
	for _, t := range ts {
		if !notSystemDB(t.Str("database")) {
			continue
		}
		_, tgt := t["target_table"]
		hasTarget = hasTarget || tgt
		tables = append(tables, map[string]interface{}{
			"database": t.Str("database"), "name": t.Str("name"), "engine": t.Str("engine"), "engine_full": t.Str("engine_full"),
			"create_table_query": t.Str("create_table_query"), "sorting_key": t.Str("sorting_key"), "primary_key": t.Str("primary_key"),
			"partition_key": t.Str("partition_key"), "sampling_key": t.Str("sampling_key"),
			"total_rows": t["total_rows"], "total_bytes": t["total_bytes"], "comment": t.Str("comment"),
			"target_database": t.Str("target_database"), "target_table": t.Str("target_table"),
			"dependencies_database": strs(t["dependencies_database"]), "dependencies_table": strs(t["dependencies_table"]),
			"loading_dependencies_database": strs(t["loading_dependencies_database"]),
			"loading_dependencies_table":    strs(t["loading_dependencies_table"]),
		})
	}
	sortBy(tables, "database", "name")

	columns := rows{}
	if cs, ok := g.filesRows("模式关系图", "system.columns"); ok {
		type pos struct {
			r   map[string]interface{}
			pos int64
		}
		var tmp []pos
		for _, c := range cs {
			if !notSystemDB(c.Str("database")) {
				continue
			}
			pk, sk := c.Int("is_in_primary_key"), c.Int("is_in_sorting_key")
			key := int64(0)
			if pk == 1 || sk == 1 {
				key = 1
			}
			hasDefault := int64(0)
			if c.Str("default_kind") != "" {
				hasDefault = 1
			}
			tmp = append(tmp, pos{map[string]interface{}{"database": c.Str("database"), "table": c.Str("table"), "name": c.Str("name"),
				"type": c.Str("type"), "is_key": key, "pk": pk, "sk": sk, "pt": c.Int("is_in_partition_key"),
				"sm": c.Int("is_in_sampling_key"), "has_default": hasDefault}, c.Int("position")})
		}
		sort.SliceStable(tmp, func(i, j int) bool {
			a, b := tmp[i].r, tmp[j].r
			if a["database"] != b["database"] {
				return a["database"].(string) < b["database"].(string)
			}
			if a["table"] != b["table"] {
				return a["table"].(string) < b["table"].(string)
			}
			return tmp[i].pos < tmp[j].pos
		})
		for _, x := range tmp {
			columns = append(columns, x.r)
		}
	}

	indices := rows{}
	if is, ok := g.filesRows("模式关系图索引", "system.data_skipping_indices"); ok {
		for _, i := range is {
			if notSystemDB(i.Str("database")) {
				indices = append(indices, map[string]interface{}{"database": i.Str("database"), "table": i.Str("table"),
					"name": i.Str("name"), "type": i.Str("type"), "expr": i.Str("expr"), "granularity": i.Int("granularity")})
			}
		}
	}
	sortBy(indices, "database", "table", "name")

	// 字典：取各副本中最差的状态及其异常
	// （任一副本为FAILED都优先于其他副本的LOADED）。
	dicts := rows{}
	if ds, err := g.bundle.Rows("system.dictionaries"); err == nil {
		rank := func(s string) int {
			switch {
			case strings.HasPrefix(s, "FAILED"):
				return 3
			case s == "LOADING" || s == "NOT_LOADED":
				return 2
			}
			return 1
		}
		m := map[string]map[string]interface{}{}
		best := map[string]int{}
		var order []string
		for _, d := range ds {
			if !notSystemDB(d.Str("database")) {
				continue
			}
			k := d.Str("database") + "\x00" + d.Str("name")
			if _, seen := m[k]; !seen {
				m[k] = map[string]interface{}{"database": d.Str("database"), "name": d.Str("name"), "source": d.Str("source"),
					"lifetime_min": d.Int("lifetime_min"), "lifetime_max": d.Int("lifetime_max")}
				best[k] = 0
				order = append(order, k)
			}
			if r := rank(d.Str("status")); r > best[k] {
				best[k] = r
				m[k]["status"], m[k]["last_exception"] = d.Str("status"), leftRunes(d.Str("last_exception"), 300)
			}
		}
		for _, k := range order {
			dicts = append(dicts, m[k])
		}
	}
	sortBy(dicts, "database", "name")
	if !schemaGraphHasContent(tables, dicts) {
		return nil
	}

	// 刷新：取执行刷新的副本，而不是报告RunningOnAnotherReplica的副本。
	refreshes := rows{}
	rf, hasRefresh := g.filesRows("模式关系图刷新", "system.view_refreshes")
	if hasRefresh {
		m := map[string]map[string]interface{}{}
		var order []string
		for _, r := range rf {
			k := r.Str("database") + "\x00" + r.Str("view")
			e, seen := m[k]
			if !seen {
				e = map[string]interface{}{"database": r.Str("database"), "view": r.Str("view"), "status": r.Str("status"),
					"last_success_time": r.Str("last_success_time"), "next_refresh_time": r.Str("next_refresh_time"), "exception": r.Str("exception")}
				m[k] = e
				order = append(order, k)
				continue
			}
			if e["status"] == "RunningOnAnotherReplica" && r.Str("status") != "RunningOnAnotherReplica" {
				e["status"] = r.Str("status")
			}
			if s := r.Str("last_success_time"); s > e["last_success_time"].(string) {
				e["last_success_time"] = s
			}
			if s := r.Str("next_refresh_time"); s > e["next_refresh_time"].(string) {
				e["next_refresh_time"] = s
			}
			if e["exception"] == "" {
				e["exception"] = r.Str("exception")
			}
		}
		for _, k := range order {
			refreshes = append(refreshes, m[k])
		}
	}

	n := redactSchemaRecords(tables, "create_table_query", "engine_full")
	n += redactSchemaRecords(dicts, "source", "last_exception")
	if n > 0 {
		fmt.Printf("  [dashboard] 模式关系图：%d 处凭据已替换为 [HIDDEN]\n", n)
	}
	return map[string]interface{}{
		"generated_at": time.Now().UTC().Format("2006-01-02 15:04:05 UTC"), "version": version, "mode": g.mode,
		"tables": tables, "columns": columns, "dictionaries": dicts, "refreshes": refreshes, "indices": indices,
		"ddl_formatted": false, "has_target": hasTarget, "has_refresh": hasRefresh,
	}
}

// sortBy按给定的字符串列依次升序排序。
func sortBy(rs rows, cols ...string) {
	sort.SliceStable(rs, func(i, j int) bool {
		for _, c := range cols {
			a, _ := rs[i][c].(string)
			b, _ := rs[j][c].(string)
			if a != b {
				return a < b
			}
		}
		return false
	})
}
