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

// Building the dashboard from the files the collectors already wrote: no
// query is sent to the server after collection. Every panel keeps the key,
// columns and order of its live query (see collect()), so the page template
// is unchanged. A panel whose source collector was skipped or failed is
// listed in the banner with the reason; one whose source does not exist on
// this server (a version or a disabled log table) is simply empty, as it was
// when the live query found no table.

// WithBundle makes the generator read the run directory instead of querying
// the server. Without it (-output-format native or tsv, unit tests) the
// panels are built with live queries.
func (g *Generator) WithBundle(b *bundle.Bundle) *Generator {
	g.bundle = b
	return g
}

type rows = []map[string]interface{}

// filesRows returns a collector's rows. A collector that was skipped or
// failed names the panel in the banner; one never collected (absent on this
// version, table disabled) is just empty.
func (g *Generator) filesRows(panel, collector string) ([]bundle.Row, bool) {
	rs, err := g.bundle.Rows(collector)
	if err == nil {
		return rs, true
	}
	var nc *bundle.NotCollected
	if errors.As(err, &nc) {
		if nc.State == "skipped" || nc.State == "failed" {
			g.skippedPanels = append(g.skippedPanels, fmt.Sprintf("%s (%s %s)", panel, collector, nc.State))
		}
		return nil, false
	}
	fmt.Printf("  [dashboard] %s: %v\n", panel, err)
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
	if v, ok := g.filesRows("version", "system.version"); ok && len(v) > 0 {
		p["version"] = v[0].Str("version")
		host, collectedAt = v[0].Str("hostname"), v[0].Str("collected_at")
	}

	tables, haveTables := g.filesRows("table and part counts", "system.tables")
	var userTables []bundle.Row
	for _, t := range tables {
		if notSystemDB(t.Str("database")) {
			userTables = append(userTables, t)
		}
	}
	byTable, haveParts := g.filesRows("storage", "system.parts_by_table")

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

	// ── Storage ──
	p["storage_by_db"] = storageByDB(byTable)
	p["engines_dist"] = enginesDist(userTables)
	p["tables_list"] = tablesList(userTables, byTable, haveTables)
	p["top_tables"] = topTables(byTable)

	// ── Query log ──
	byHour, _ := g.filesRows("query activity", "system.query_log_by_hour_7_days")
	byPattern, _ := g.filesRows("query patterns", "system.query_log_by_pattern_7_days")
	p["query_by_time"] = queryByTime(byHour)
	p["query_by_kind"] = queryByKind(byHour)
	p["query_slow"] = querySlow(byPattern)
	p["query_heavy"] = queryHeavy(byPattern)
	p["query_by_user"] = queryByUser(byPattern)
	p["exceptions"] = exceptionsByCode(byHour)

	if g.hostInfo != nil {
		g.addHostInfo(p)
	}

	// ── Logs ──
	if tl, ok := g.filesRows("logs", "system.text_log"); ok {
		p["text_log"] = textLogPanel(tl, host)
		p["text_log_row_cap"] = textLogRowCap
	}

	// ── Part log (3 days: the collector's window) ──
	partLog, _ := g.filesRows("part log", "system.part_log_3_days")
	p["part_log_by_time"], p["part_log_by_type"], p["ttl_activity"] = partLogPanels(partLog)

	// ── Dictionaries ──
	dicts, _ := g.filesRows("dictionaries", "system.dictionaries")
	p["dictionaries"] = dictionariesPanel(dicts, host)
	redactDictionaryPanel(p["dictionaries"].([]map[string]interface{}))

	// ── Crash log ──
	crashes, _ := g.filesRows("crash log", "system.crash_log")
	p["crash_log"] = crashLogPanel(crashes)

	// ── Pending work ──
	muts, _ := g.filesRows("mutations", "system.mutations")
	p["mutations"] = mutationsPanel(muts)
	detached, _ := g.filesRows("detached", "system.detached_parts")
	p["detached"] = detachedPanel(detached, g.mode)
	if g.mode == "cloud" {
		cl, _ := g.filesRows("clusters", "system.clusters")
		p["clusters"] = clustersPanel(cl)
	} else {
		p["clusters"] = rows{}
	}
	rq, _ := g.filesRows("replication queue", "system.replication_queue")
	p["replication_queue"] = replicationQueuePanel(rq)
	reps, _ := g.filesRows("replicas", "system.replicas")
	problem, _ := g.bundle.Rows("system.replicas_problem_tables") // optional: Keeper-backed counts
	p["replicas"] = replicasPanel(reps, problem, g.mode, host)

	// ── Keeper health and pools ──
	ml, haveML := g.filesRows("Keeper and pools", "system.metric_log_7_days")
	p["keeper_metric_hourly"], p["keeper_wait_available"], p["keeper_session_available"] = keeperMetricPanel(ml, haveML)
	p["pools_hourly"] = poolsPanel(ml)
	p["pool_sizes"] = g.poolSizesFromFiles()
	p["pools_now"] = collectedAt

	if el, ok := g.bundle.Rows("system.error_log_7_days"); ok == nil {
		p["keeper_errors_hourly"] = keeperErrorsFromErrorLog(el)
		p["keeper_errors_source"] = "system.error_log — every thread, background merges and fetches included"
	} else {
		p["keeper_errors_hourly"] = keeperErrorsFromQueryLog(byHour)
		p["keeper_errors_source"] = "system.query_log — queries only; background merges, fetches and DDL workers are not counted (system.error_log needs 24.8+)"
	}
	zk, _ := g.filesRows("Keeper connection", "system.zookeeper_connection")
	p["keeper_connection"] = keeperConnectionPanel(zk, g.mode)

	// ── Disks, errors, part counts, async inserts ──
	disks, _ := g.filesRows("disks", "system.disks")
	p["disks"] = disksPanel(disks, host)
	errs, _ := g.filesRows("server errors", "system.errors")
	p["server_errors"] = serverErrorsPanel(errs)
	pbp, _ := g.filesRows("high part count", "system.parts_by_partition")
	p["high_part_count"] = highPartCount(pbp)
	if g.mode != "gov" {
		ai, _ := g.filesRows("async inserts", "system.asynchronous_insert_log_7_days")
		p["async_inserts"] = asyncInsertsPanel(ai, collectedAt)
	} else {
		p["async_inserts"] = rows{}
	}

	g.collectAnalysisFromFiles(p)
	return p
}

// ── helpers ──────────────────────────────────────────────────────────────

// readableSize is ClickHouse formatReadableSize: powers of 1024, two
// decimals ("1.50 GiB").
func readableSize(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := 0
	for math.Abs(n) >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", n, units[i])
}

// readableQuantity is ClickHouse formatReadableQuantity ("1.99 million").
func readableQuantity(n float64) string {
	units := []string{"", " thousand", " million", " billion", " trillion", " quadrillion"}
	i := 0
	for math.Abs(n) >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	return fmt.Sprintf("%.2f%s", n, units[i])
}

// round is ClickHouse round(x, d) on a Float64 (ties to even).
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

// leftRunes is leftUTF8: at most n characters, never a split rune.
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

// byNum sorts descending by a numeric column, stable.
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

// hourOf parses a collected DateTime ("YYYY-MM-DD hh:mm:ss").
func hourOf(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	return t, err == nil
}

// ── storage ──

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

func tablesList(userTables, byTable []bundle.Row, haveTables bool) rows {
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

// ── query log ──

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

// patternAgg folds query_log_by_pattern_7_days rows per (hash, kind, user).
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

// querySlow: per pattern, QueryFinish rows with a measured duration and the
// failures, slowest average first.
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
			"avg_read_mb": round(a.read/den/1048576, 2), "avg_memory_mb": round(a.mem/den/1048576, 2),
			"errors": int64(a.errors), "sample_query": a.sample})
	}
	byNum(out, "avg_duration_ms")
	return limit(out, 20)
}

// queryHeavy: per pattern, QueryFinish only, most read per execution first.
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

// ── logs ──

// textLogPanel: Warning and worse, newest first. Onprem rows carry no
// hostname (one server): it is the collected host, as the live panel showed.
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

// ── part log ──

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

// ── dictionaries, crashes, pending work ──

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

// mutationsPanel: unfinished mutations, not being killed; cloud lists each
// one once per replica, so it is kept once.
func mutationsPanel(ms []bundle.Row) rows {
	out := rows{}
	seen := map[string]bool{}
	for _, m := range ms {
		if m.Int("parts_to_do") <= 0 || m.Int("is_killed") == 1 {
			continue
		}
		k := m.Str("database") + "\x00" + m.Str("table") + "\x00" + m.Str("mutation_id")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, map[string]interface{}{"database": m.Str("database"), "table": m.Str("table"),
			"mutation_id": m.Str("mutation_id"), "command": m.Str("command"), "create_time": m.Str("create_time"),
			"parts_to_do": m.Int("parts_to_do")})
	}
	byNum(out, "parts_to_do")
	return limit(out, 20)
}

// detachedPanel: per table, largest first; a size only when the server
// reports bytes_on_disk (22.11+). Cloud lists a shared part once per replica.
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

func replicationQueuePanel(rq []bundle.Row) rows {
	out := rows{}
	for _, r := range rq {
		out = append(out, map[string]interface{}{"table": r.Str("table"), "type": r.Str("type"),
			"executing": r.Str("is_currently_executing"), "last_exception": r.Str("last_exception")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["table"].(string) < out[j]["table"].(string) })
	return limit(out, 50)
}

// replicasPanel: the serving node's replicas (the live panel read the local
// system.replicas; the cloud file holds every replica), with the
// Keeper-backed replica counts where replicas_problem_tables has them.
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

// ── Keeper and pools ──

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

// poolSizesFromFiles: the configured sizes, from server_settings on 23.3+
// and settings below (system.settings there still lists the names with the
// old session defaults). In cloud the fetch and schedule sizes are the
// smallest live size over the replicas (metric_log_by_host_3_days) when
// collected — server_settings comes from one replica, and the chart's
// series is the worst replica, so the smallest pool is its reference line.
func (g *Generator) poolSizesFromFiles() map[string]int64 {
	out := map[string]int64{"fetch": 0, "schedule": 0, "merge": 0}
	settings := map[string]string{"fetch": "background_fetches_pool_size", "schedule": "background_schedule_pool_size", "merge": "background_pool_size"}
	if g.mode == "cloud" {
		// Not the merge pool: its live size is background_pool_size times
		// background_merges_mutations_concurrency_ratio, and the chart's
		// reference line is the setting.
		cols := map[string]string{"fetch": "max(CurrentMetric_BackgroundFetchesPoolSize)", "schedule": "max(CurrentMetric_BackgroundSchedulePoolSize)"}
		if hosts, err := g.bundle.Rows("system.metric_log_by_host_3_days"); err == nil {
			for key, col := range cols {
				perHost := map[string]int64{}
				for _, r := range hosts {
					if v := r.Int(col); v > perHost[r.Str("hostname")] {
						perHost[r.Str("hostname")] = v
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

// timeName is one hour of one error code name.
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

// ── disks, errors, parts, async inserts ──

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
			"free_space": int64(free), "total_space": int64(total), "free_space_human": readableSize(free),
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

// serverErrorsPanel folds the per-replica counters (cloud) into one row per
// error, the most frequent first.
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

// asyncInsertsPanel: the last 24 hours (to the collection's now) per hour
// and status; the flush latency is the per-table averages weighted by
// their flush counts.
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

// ── query analysis ──

// collectAnalysisFromFiles embeds the query_analysis/ files the analysis
// collector wrote, under the same payload keys as the live panels.
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

// ── schema graph ──

// schemaGraphFromFiles builds the schema page from the collected
// system.tables / columns / data_skipping_indices / dictionaries /
// view_refreshes, with the columns of the live queries. nil when
// system.tables was not collected (nothing to draw). The DDL is the
// server's original text: the live page pretty-printed it with
// formatQueryOrNull, which only a query can do.
func (g *Generator) schemaGraphFromFiles(version string) map[string]interface{} {
	ts, ok := g.filesRows("schema graph", "system.tables")
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
	if cs, ok := g.filesRows("schema graph", "system.columns"); ok {
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
	if is, err := g.bundle.Rows("system.data_skipping_indices"); err == nil {
		for _, i := range is {
			if notSystemDB(i.Str("database")) {
				indices = append(indices, map[string]interface{}{"database": i.Str("database"), "table": i.Str("table"),
					"name": i.Str("name"), "type": i.Str("type"), "expr": i.Str("expr"), "granularity": i.Int("granularity")})
			}
		}
	}
	sortBy(indices, "database", "table", "name")

	// Dictionaries: the worst status over the replicas, with its exception
	// (FAILED on any replica outranks LOADED elsewhere).
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

	// Refreshes: the replica that runs the refresh, not the ones reporting
	// RunningOnAnotherReplica.
	refreshes := rows{}
	rf, err := g.bundle.Rows("system.view_refreshes")
	hasRefresh := err == nil
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
		fmt.Printf("  [dashboard] schema graph: %d credential(s) replaced with [HIDDEN]\n", n)
	}
	return map[string]interface{}{
		"generated_at": time.Now().UTC().Format("2006-01-02 15:04:05 UTC"), "version": version, "mode": g.mode,
		"tables": tables, "columns": columns, "dictionaries": dicts, "refreshes": refreshes, "indices": indices,
		"ddl_formatted": false, "has_target": hasTarget, "has_refresh": hasRefresh,
	}
}

// sortBy sorts ascending by string columns, in order.
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
