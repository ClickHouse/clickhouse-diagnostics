package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/bundle"
)

type frow = map[string]interface{}

// runDir按executor的命名方式写入collector文件。
func runDir(t *testing.T, files map[string][]frow) string {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["system.version"]; !ok {
		files["system.version"] = []frow{{"version": "26.7.1.1", "hostname": "ch-01", "timezone": "UTC", "collected_at": "2026-10-29 12:00:00"}}
	}
	for name, rs := range files {
		var sb strings.Builder
		for _, r := range rs {
			b, _ := json.Marshal(r)
			sb.Write(b)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, name+"_20261029_120000.jsonl"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func payloadOf(t *testing.T, html string) map[string]interface{} {
	t.Helper()
	i := strings.Index(html, "const DATA = ")
	if i < 0 {
		t.Fatal("页面中没有DATA")
	}
	var p map[string]interface{}
	if err := json.NewDecoder(strings.NewReader(html[i+len("const DATA = "):])).Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureRun() map[string][]frow {
	return map[string][]frow{
		"system.tables": {
			{"database": "db", "name": "a", "engine": "MergeTree", "partition_key": "d", "sorting_key": "k", "storage_policy": "default",
				"create_table_query": "CREATE TABLE db.a (k UInt64) ENGINE = MergeTree ORDER BY k", "dependencies_database": []string{}, "dependencies_table": []string{}},
			{"database": "db", "name": "b", "engine": "ReplicatedMergeTree", "dependencies_database": []string{}, "dependencies_table": []string{}},
			{"database": "system", "name": "query_log", "engine": "MergeTree"},
		},
		"system.parts_by_table": {
			{"database": "db", "table": "a", "parts": "3", "rows": "1500000", "bytes_on_disk": "3145728", "data_compressed_bytes": "100", "data_uncompressed_bytes": "350"},
		},
		"system.columns": {
			{"database": "db", "table": "a", "name": "k", "type": "UInt64", "position": "1", "default_kind": "", "is_in_primary_key": 1, "is_in_sorting_key": 1, "is_in_partition_key": 0, "is_in_sampling_key": 0},
		},
		"system.query_log_by_hour_7_days": {
			{"time": "2026-10-29 10:00:00", "query_kind": "Select", "type": "QueryFinish", "exception_code": 0, "count": "10", "query_duration_ms": "1000", "memory_usage": "10485760"},
			{"time": "2026-10-29 10:00:00", "query_kind": "Select", "type": "ExceptionWhileProcessing", "exception_code": 999, "count": "4", "exception": "Keeper gone"},
		},
		"system.query_log_by_pattern_7_days": {
			{"hash": "18446744073709551615", "query_kind": "Select", "user": "u", "type": "QueryFinish", "count": "10", "timed_count": "8",
				"total_duration_ms": "800", "max_duration_ms": "300", "total_read_bytes": "83886080", "total_memory_usage": "10485760", "sample_query": "SELECT 1"},
			{"hash": "18446744073709551615", "query_kind": "Select", "user": "u", "type": "ExceptionWhileProcessing", "count": "4"},
		},
		"system.mutations": {
			{"database": "db", "table": "a", "mutation_id": "m1", "command": "DELETE WHERE 1", "create_time": "2026-10-29 09:00:00", "parts_to_do": "2", "is_killed": 0},
			{"database": "db", "table": "a", "mutation_id": "m1", "command": "DELETE WHERE 1", "create_time": "2026-10-29 09:00:00", "parts_to_do": "2", "is_killed": 0},
			{"database": "db", "table": "a", "mutation_id": "m2", "parts_to_do": "5", "is_killed": 1},
		},
		"system.metric_log_7_days": {
			{"time": "2026-10-29 10:00:00", "zk_transactions": "100", "zk_hw_exceptions": "2", "zk_user_exceptions": "1", "zk_wait_us": "5000",
				"zk_sessions_min": "1", "zk_sessions_max": "1", "max_replica_avg_fetch_pool_tasks": 3.5},
		},
		"system.server_settings": {{"name": "background_fetches_pool_size", "value": "16"}, {"name": "background_pool_size", "value": "16"}},
		"system.settings":        {{"name": "background_fetches_pool_size", "value": "8"}},
		"system.disks":           {{"name": "default", "path": "/var/lib/clickhouse/", "type": "Local", "free_bytes": "1073741824", "total_bytes": "4294967296"}},
		"system.text_log": {
			{"event_time": "2026-10-29 11:00:00", "level": "Error", "logger_name": "x", "message": strings.Repeat("é", 400)},
			{"event_time": "2026-10-29 11:30:00", "level": "Information", "logger_name": "x", "message": "noise"},
		},
	}
}

// 从运行目录构建时，dashboard不会向服务器发送任何内容：
// generator根本没有客户端，因此任何查询都会panic。
func TestGenerate_FromFilesSendsNoQuery(t *testing.T) {
	dir := runDir(t, fixtureRun())
	g := NewGenerator(nil, "onprem").WithServerVersion(internal.Version{Major: 26, Minor: 7, Patch: 1, Build: 1}).
		WithBundle(bundle.Open(dir, map[string]bundle.Status{"system.detached_parts": {State: "skipped", Reason: "essential collection"}}))
	if err := g.Generate(dir, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dashboard.html"))
	if err != nil {
		t.Fatal(err)
	}
	p := payloadOf(t, string(raw))
	check := func(name string, ok bool) {
		t.Helper()
		if !ok {
			b, _ := json.Marshal(p[name])
			t.Errorf("%s：%s", name, b)
		}
	}
	first := func(k string) map[string]interface{} {
		rs, _ := p[k].([]interface{})
		if len(rs) == 0 {
			return map[string]interface{}{}
		}
		return rs[0].(map[string]interface{})
	}
	check("version", p["version"] == "26.7.1.1")
	check("total_tables", p["total_tables"] == 2.0 && p["total_databases"] == 1.0)
	check("total_size", p["total_size"] == "3.00 MiB" && p["active_parts"] == 3.0)
	check("top_tables", first("top_tables")["total_rows"] == "1.50 million" && first("top_tables")["compression_ratio"] == 3.5)
	check("tables_list", len(p["tables_list"].([]interface{})) == 2 && first("tables_list")["size"] == "3.00 MiB")
	check("query_by_kind", first("query_by_kind")["avg_duration_ms"] == 100.0 && first("query_by_kind")["avg_memory_mb"] == 1.0)
	// hash保持为文本：JavaScript会对UInt64做舍入。
	check("query_slow", first("query_slow")["hash"] == "18446744073709551615" && first("query_slow")["executions"] == 8.0 &&
		first("query_slow")["avg_duration_ms"] == 100.0 && first("query_slow")["errors"] == 4.0)
	check("query_heavy", first("query_heavy")["executions"] == 10.0 && first("query_heavy")["avg_read_mb"] == 8.0)
	check("exceptions", first("exceptions")["exception_code"] == 999.0 && first("exceptions")["msg"] == "Keeper gone" && first("exceptions")["count"] == 4.0)
	check("keeper_errors_hourly", first("keeper_errors_hourly")["code_name"] == "KEEPER_EXCEPTION" && first("keeper_errors_hourly")["count"] == 4.0)
	check("mutations", len(p["mutations"].([]interface{})) == 1)
	check("keeper_metric_hourly", first("keeper_metric_hourly")["wait_us"] == 5000.0 && p["keeper_wait_available"] == true)
	check("pools_hourly", first("pools_hourly")["fetch_tasks"] == 3.5)
	// 23.3+：使用server_settings，而不是settings中已过时的会话默认值。
	check("pool_sizes", p["pool_sizes"].(map[string]interface{})["fetch"] == 16.0)
	check("pools_now", p["pools_now"] == "2026-10-29 12:00:00")
	check("disks", first("disks")["free_pct"] == 25.0 && first("disks")["hostname"] == "ch-01")
	check("text_log", len(p["text_log"].([]interface{})) == 1 && len([]rune(first("text_log")["message"].(string))) == textLogMessageCap &&
		first("text_log")["hostname"] == "ch-01")
	sp := p["collect"].(map[string]interface{})["skipped_panels"].([]interface{})
	check("collect", len(sp) == 1 && strings.Contains(sp[0].(string), "system.detached_parts 跳过"))
	if _, err := os.Stat(filepath.Join(dir, schemaGraphFile)); err != nil {
		t.Errorf("schema graph未从文件写出：%v", err)
	}
}

// Cloud对每个副本各列出一次共享的detached part、对每个副本各列出
// 一次mutation，并列出每个主机的副本：每一项都会被折叠回去。
func TestFromFiles_CloudFolds(t *testing.T) {
	var det []bundle.Row
	for i := 0; i < 3; i++ {
		det = append(det, bundle.Row{"database": "db", "table": "a", "reason": "broken", "bytes_on_disk": "1024", "path": "/s3/x/detached/broken_all_1_1_0/"})
	}
	if d := detachedPanel(det, "cloud"); len(d) != 1 || d[0]["count"] != int64(1) || d[0]["size"] != "1.00 KiB" {
		t.Errorf("detached：%v", d)
	}
	if d := detachedPanel(det, "onprem"); d[0]["count"] != int64(3) {
		t.Errorf("onprem保留每一行：%v", d)
	}
	reps := []bundle.Row{{"hostname": "r1", "database": "db", "table": "a"}, {"hostname": "r2", "database": "db", "table": "a"}}
	problem := []bundle.Row{{"hostname": "r1", "database": "db", "table": "a", "total_replicas": "3", "active_replicas": "2"},
		{"hostname": "r2", "database": "db", "table": "a", "total_replicas": "3", "active_replicas": "1"}}
	if r := replicasPanel(reps, problem, "cloud", "r1"); len(r) != 1 || r[0]["active_replicas"] != "2" {
		t.Errorf("replicas：仅服务节点，包含其计数：%v", r)
	}
}

// 23.3之前，池大小在system.settings中。
func TestFromFiles_PoolSizesByVersion(t *testing.T) {
	dir := runDir(t, map[string][]frow{
		"system.settings":        {{"name": "background_fetches_pool_size", "value": "8"}},
		"system.server_settings": {{"name": "background_fetches_pool_size", "value": "16"}},
	})
	g := NewGenerator(nil, "onprem").WithServerVersion(internal.Version{Major: 22, Minor: 8}).WithBundle(bundle.Open(dir, nil))
	if got := g.poolSizesFromFiles()["fetch"]; got != 8 {
		t.Errorf("22.8：期望从system.settings得到8，实际得到%d", got)
	}
}

// 从未采集的来源（在此版本上不存在）是空面板，而不是banner条目；
// 跳过或失败的来源会被列出。
func TestFromFiles_NotCollectedIsQuiet(t *testing.T) {
	dir := runDir(t, map[string][]frow{})
	g := NewGenerator(nil, "onprem").WithBundle(bundle.Open(dir, map[string]bundle.Status{"system.errors": {State: "failed", Reason: "timeout"}}))
	g.filesRows("crash log", "system.crash_log")
	g.filesRows("server errors", "system.errors")
	if len(g.skippedPanels) != 1 || !strings.Contains(g.skippedPanels[0], "system.errors 失败") {
		t.Errorf("跳过的面板：%v", g.skippedPanels)
	}
}

func TestReadableFormats(t *testing.T) {
	for in, want := range map[float64]string{0: "0.00 B", 1536: "1.50 KiB", 3 << 30: "3.00 GiB"} {
		if got := readableSize(in); got != want {
			t.Errorf("readableSize(%v) = %s，期望%s", in, got, want)
		}
	}
	for in, want := range map[float64]string{500: "500.00", 1500000: "1.50 million", 2e9: "2.00 billion"} {
		if got := readableQuantity(in); got != want {
			t.Errorf("readableQuantity(%v) = %s，期望%s", in, got, want)
		}
	}
}

// 基于文件的面板的评审修复，每个修复对应一个回归测试。
func TestFromFiles_ReviewFixes(t *testing.T) {
	// -to早于采集时间：窗口在此结束，使用服务器时间。
	to := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if got := windowEnd("2026-10-29 12:00:00", "Europe/Paris", to); got != "2026-10-20 11:00:00" {
		t.Errorf("带-to的windowEnd：%s", got)
	}
	if got := windowEnd("2026-10-29 12:00:00", "UTC", time.Time{}); got != "2026-10-29 12:00:00" {
		t.Errorf("不带-to的windowEnd：%s", got)
	}
	ai := []bundle.Row{{"time": "2026-10-20 08:00:00", "status": "Ok", "flushes": "2", "total_bytes": "10", "avg_flush_ms": "5"}}
	if got := asyncInsertsPanel(ai, windowEnd("2026-10-29 12:00:00", "UTC", to)); len(got) != 1 {
		t.Errorf("在-to结束的事故的异步插入会被保留：%v", got)
	}

	// 慢查询模式：仅对已计时的执行求平均。
	pat := []bundle.Row{{"hash": "1", "query_kind": "Select", "user": "u", "type": "QueryFinish", "count": "2", "timed_count": "1",
		"total_duration_ms": "100", "total_read_bytes": "8388608", "timed_read_bytes": "4194304",
		"total_memory_usage": "2097152", "timed_memory_usage": "1048576"}}
	if s := querySlow(pat); s[0]["avg_read_mb"] != 4.0 || s[0]["avg_memory_mb"] != 1.0 {
		t.Errorf("慢查询：%v", s)
	}
	if h := queryHeavy(pat); h[0]["avg_read_mb"] != 4.0 {
		t.Errorf("heavy保留每个QueryFinish：%v", h)
	}

	// Cloud的mutation积压因副本而异：保留最大的。
	ms := []bundle.Row{{"database": "db", "table": "a", "mutation_id": "m", "parts_to_do": "2"},
		{"database": "db", "table": "a", "mutation_id": "m", "parts_to_do": "200"}}
	if m := mutationsPanel(ms); len(m) != 1 || m[0]["parts_to_do"] != int64(200) {
		t.Errorf("mutations：%v", m)
	}

	// Cloud replication queue：服务副本的。
	rq := []bundle.Row{{"hostname": "r1", "table": "a", "type": "GET_PART"}, {"hostname": "r2", "table": "a", "type": "GET_PART"}}
	if q := replicationQueuePanel(rq, "cloud", "r1"); len(q) != 1 {
		t.Errorf("replication queue：%v", q)
	}
	if q := replicationQueuePanel(rq, "onprem", "r1"); len(q) != 2 {
		t.Errorf("onprem原样保留文件：%v", q)
	}

	// 磁盘容量：精确的UInt64文本（2^64-1 = 无限）。
	ds := []bundle.Row{{"name": "s3", "free_bytes": "18446744073709551615", "total_bytes": "18446744073709551615"}}
	if d := disksPanel(ds, "h"); d[0]["total_space"] != "18446744073709551615" {
		t.Errorf("disks：%v", d)
	}

	// Cloud池大小：每个主机的最新值，而非最大值。
	dir := runDir(t, map[string][]frow{"system.metric_log_by_host_3_days": {
		{"hostname": "r1", "time": "2026-10-28 10:00:00", "max(CurrentMetric_BackgroundFetchesPoolSize)": "32"},
		{"hostname": "r1", "time": "2026-10-29 10:00:00", "max(CurrentMetric_BackgroundFetchesPoolSize)": "16"},
	}})
	g := NewGenerator(nil, "cloud").WithBundle(bundle.Open(dir, nil))
	if got := g.poolSizesFromFiles()["fetch"]; got != 16 {
		t.Errorf("池大小：期望最新的16，实际得到%d", got)
	}
}

// 完整运行会点名collector失败的面板，而不仅是essential运行。
func TestTemplate_FullBannerListsPanelsWithoutData(t *testing.T) {
	i := strings.Index(htmlTemplate, "'<b>完整诊断（full）</b>")
	if i < 0 || !strings.Contains(htmlTemplate[i:i+700], "sp.length?") {
		t.Error("完整运行的banner未渲染skipped_panels")
	}
}

// 第二轮评审：缺失的来源会被点名，绝不以零显示。
func TestFromFiles_MissingSourcesAreNamed(t *testing.T) {
	files := fixtureRun()
	delete(files, "system.parts_by_table")
	files["system.replicas"] = []frow{{"database": "db", "table": "b", "is_readonly": 0}}
	dir := runDir(t, files)
	st := map[string]bundle.Status{}
	for _, c := range []string{"system.parts_by_table", "system.replicas_problem_tables", "system.error_log_7_days",
		"system.data_skipping_indices", "system.view_refreshes"} {
		st[c] = bundle.Status{State: "skipped", Reason: "time budget"}
	}
	g := NewGenerator(nil, "onprem").WithBundle(bundle.Open(dir, st))
	if err := g.Generate(dir, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "dashboard.html"))
	p := payloadOf(t, string(raw))
	sp := strings.Join(func() []string {
		var s []string
		for _, x := range p["collect"].(map[string]interface{})["skipped_panels"].([]interface{}) {
			s = append(s, x.(string))
		}
		return s
	}(), " | ")
	for _, c := range []string{"system.parts_by_table", "system.replicas_problem_tables", "system.error_log_7_days",
		"system.data_skipping_indices", "system.view_refreshes"} {
		if !strings.Contains(sp, c+" 跳过") {
			t.Errorf("banner未点名%s：%s", c, sp)
		}
	}
	tl := p["tables_list"].([]interface{})
	if len(tl) != 2 || tl[0].(map[string]interface{})["size"] != "n/a" {
		t.Errorf("没有parts_by_table的表：大小必须为n/a，得到%v", tl)
	}
	// error_log被跳过：query_log回退仍会填充面板。
	if ke := p["keeper_errors_hourly"].([]interface{}); len(ke) != 1 || !strings.Contains(p["keeper_errors_source"].(string), "query_log") {
		t.Errorf("Keeper错误回退：%v %v", ke, p["keeper_errors_source"])
	}
}

// 标题说明这些文件覆盖的时间窗口。
func TestFromFiles_WindowLabels(t *testing.T) {
	g := NewGenerator(nil, "onprem")
	if got := g.windowLabel("最近 3 天"); got != "最近 3 天" {
		t.Errorf("默认：%s", got)
	}
	g.WithCollectMode("essential", "load", nil)
	if got := g.windowLabel("最近 7 天"); got != "最近一天（essential 运行）" {
		t.Errorf("essential：%s", got)
	}
	g.WithWindow(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC))
	if got := g.windowLabel("最近 7 天"); got != "2026-10-20 00:00 UTC – 2026-10-21 06:00 UTC" {
		t.Errorf("-from/-to：%s", got)
	}
	for _, want := range []string{`Part 日志事件（<span data-win="3d">`, `TTL 活动（<span data-win="3d">`,
		`Keeper 健康（<span data-win="7d">`, "querySelectorAll('[data-win]')"} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("模板缺少%q", want)
		}
	}
}
