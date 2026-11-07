package dashboard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"clickhouse-diagnostic/internal/alert"
)

func TestBuildHTML_SampleData(t *testing.T) {
	data := map[string]interface{}{
		"generated_at":    time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		"mode":            "onprem",
		"version":         "24.8.5.115",
		"uptime":          "3 days 4 hours 12 minutes",
		"total_databases": 5,
		"total_tables":    42,
		"active_parts":    1280,
		"total_size":      "18.50 GiB",

		"storage_by_db": []map[string]interface{}{
			{"database": "production", "parts": 800, "rows": 1e9, "bytes_total": 12e9, "size_human": "12.00 GiB", "compression_ratio": 3.2},
			{"database": "analytics", "parts": 350, "rows": 5e8, "bytes_total": 5e9, "size_human": "5.00 GiB", "compression_ratio": 4.1},
		},
		"engines_dist": []map[string]interface{}{
			{"engine": "MergeTree", "count": 28},
			{"engine": "ReplicatedMergeTree", "count": 10},
		},
		"tables_list": []map[string]interface{}{
			{"database": "production", "table_name": "events", "engine": "MergeTree", "parts": 320, "total_rows": "500.00 M", "size": "8.50 GiB", "bytes_on_disk": 9126805504, "partition_key": "toYYYYMM(created_at)", "sorting_key": "created_at", "storage_policy": "default"},
			{"database": "analytics", "table_name": "sessions", "engine": "ReplicatedMergeTree", "parts": 180, "total_rows": "200.00 M", "size": "3.20 GiB", "bytes_on_disk": 3435973837, "partition_key": "", "sorting_key": "id", "storage_policy": ""},
			{"database": "production", "table_name": "metrics", "engine": "MergeTree", "parts": 95, "total_rows": "80.00 M", "size": "1.80 GiB", "bytes_on_disk": 1932735283, "partition_key": "", "sorting_key": "ts", "storage_policy": ""},
		},
		"query_by_time": []map[string]interface{}{
			{"time": "2026-04-07 10:00:00", "query_kind": "Select", "count": 1200},
			{"time": "2026-04-07 10:00:00", "query_kind": "Insert", "count": 300},
			{"time": "2026-04-07 11:00:00", "query_kind": "Select", "count": 1500},
			{"time": "2026-04-07 11:00:00", "query_kind": "Insert", "count": 250},
		},
		"query_by_kind": []map[string]interface{}{
			{"query_kind": "Select", "count": 15000, "avg_duration_ms": 45, "avg_memory_mb": 12.5},
			{"query_kind": "Insert", "count": 3200, "avg_duration_ms": 120, "avg_memory_mb": 8.2},
		},
		"query_slow": []map[string]interface{}{
			{"hash": "12345678901234567890", "query_kind": "Select", "user": "default", "executions": 500, "avg_duration_ms": 4200, "max_duration_ms": 18000, "avg_read_mb": 350.5, "avg_memory_mb": 128.3, "errors": 0,
				"sample_query": "SELECT event_type, count() FROM production.events WHERE created_at >= now() - INTERVAL 7 DAY GROUP BY event_type ORDER BY count() DESC LIMIT 100"},
			{"hash": "98765432109876543210", "query_kind": "Select", "user": "analyst", "executions": 120, "avg_duration_ms": 1800, "max_duration_ms": 5200, "avg_read_mb": 820.0, "avg_memory_mb": 256.0, "errors": 2,
				"sample_query": "SELECT * FROM analytics.sessions FINAL WHERE user_id IN (SELECT user_id FROM production.events) ORDER BY started_at DESC"},
		},
		"query_heavy": []map[string]interface{}{
			{"hash": "98765432109876543210", "query_kind": "Select", "user": "analyst", "executions": 120, "avg_read_mb": 820.0, "total_read": "98.40 GiB", "avg_duration_ms": 1800,
				"sample_query": "SELECT * FROM analytics.sessions FINAL WHERE user_id IN (SELECT user_id FROM production.events) ORDER BY started_at DESC"},
			// 没有 sample_query：标签必须回退到完整的 hash，绝不能是截断后的。
			{"hash": "12345678901234567890", "query_kind": "Select", "user": "default", "executions": 500, "avg_read_mb": 350.5, "total_read": "175.25 GiB", "avg_duration_ms": 4200},
		},
		"query_by_user": []map[string]interface{}{
			{"user": "default", "executions": 15000, "avg_duration_ms": 45, "total_read_gb": 175.25, "total_memory_gb": 12.5, "error_count": 5},
			{"user": "analyst", "executions": 3200, "avg_duration_ms": 1800, "total_read_gb": 98.40, "total_memory_gb": 30.2, "error_count": 12},
		},
		"exceptions": []map[string]interface{}{
			{"exception_code": "241", "count": 15, "msg": "Memory limit exceeded"},
			{"exception_code": "60", "count": 8, "msg": "Table doesn't exist"},
		},
		"keeper_metric_hourly": []map[string]interface{}{
			{"time": "2026-04-01 10:00:00", "transactions": 28000000, "hw_exceptions": 0, "user_exceptions": 1200, "wait_us": 5600000000, "sessions_min": 1, "sessions_max": 1},
			{"time": "2026-04-01 11:00:00", "transactions": 2400000, "hw_exceptions": 25000000, "user_exceptions": 900, "wait_us": 9000000000, "sessions_min": 0, "sessions_max": 1},
		},
		"keeper_errors_hourly": []map[string]interface{}{
			{"time": "2026-04-01 11:00:00", "code_name": "KEEPER_EXCEPTION", "count": 732},
			{"time": "2026-04-01 11:00:00", "code_name": "UNKNOWN_STATUS_OF_INSERT", "count": 47},
		},
		"keeper_errors_source": "system.error_log — every thread, background merges and fetches included",
		"keeper_connection": []map[string]interface{}{
			{"name": "default", "host": "keeper-1", "port": "9181", "index": "0", "connected_time": "2026-04-01 11:07:12", "session_uptime_s": "612", "is_expired": "0", "api_version": "3"},
		},
		"part_log_by_time": []map[string]interface{}{
			{"time": "2026-04-01 00:00:00", "event_type": "MERGE_PARTS", "count": 120},
			{"time": "2026-04-01 00:00:00", "event_type": "NEW_PART", "count": 450},
		},
		"part_log_by_type": []map[string]interface{}{
			{"event_type": "NEW_PART", "count": 1350, "total_size": "45.00 GiB"},
			{"event_type": "MERGE_PARTS", "count": 363, "total_size": "18.00 GiB"},
		},
		"dictionaries": []map[string]interface{}{
			{"database": "default", "name": "geo_dict", "status": "LOADED", "type": "Flat", "bytes_allocated": 104857600, "bytes_allocated_human": "100.00 MiB", "element_count": 50000, "query_count": 1200, "hit_rate_pct": 99.5, "found_rate_pct": 98.0, "lifetime_min": 300, "lifetime_max": 3600, "last_update": "2026-04-07 08:00:00", "loading_duration_s": 0.45, "last_exception": ""},
			{"database": "default", "name": "bad_dict", "status": "FAILED", "type": "Complex", "bytes_allocated": 0, "bytes_allocated_human": "0.00 B", "element_count": 0, "query_count": 0, "hit_rate_pct": 0, "found_rate_pct": 0, "lifetime_min": 60, "lifetime_max": 600, "last_update": "", "loading_duration_s": 0, "last_exception": "Connection refused"},
		},
		"crash_log": []map[string]interface{}{
			{"event_time": "2026-04-06 14:22:11", "signal": "SIGSEGV", "thread_id": "12345", "query_id": "abc123", "version": "24.8.5.115"},
		},
		"top_tables": []map[string]interface{}{
			{"database": "production", "table": "events", "parts": 320, "total_rows": "500.00 M", "compressed_size": "8.50 GiB", "compression_ratio": 3.5},
		},
		"mutations":         []map[string]interface{}{},
		"detached":          []map[string]interface{}{},
		"replication_queue": []map[string]interface{}{},
		"clusters":          []map[string]interface{}{},
	}

	// 添加模拟的 alert 结果（一个 fired，一个未 fired）
	firedAt := time.Now().UTC().Format(time.RFC3339)
	data["alerts"] = []alert.Result{
		{
			Name:     "crash_log_entries",
			Title:    "Server crash detected",
			Severity: "critical",
			Rows: []map[string]interface{}{
				{"event_time": "2026-04-06 14:22:11", "signal": "11", "version": "24.8.5.115", "query_id": "abc123"},
			},
			FiredAt: firedAt,
		},
		{
			Name:     "large_parts",
			Title:    "Parts larger than 150 GB",
			Severity: "warning",
			Rows:     []map[string]interface{}{},
			FiredAt:  firedAt,
		},
		// 一条 skipped（不适用）的规则：它的表在这台服务器上不存在。
		// 不能算作已评估，并且必须渲染到弱化的“不适用”提示中，
		// 而不是作为 fired/passed 检查。
		{
			Name:    "text_log_alert",
			Title:   "Text log errors",
			Skipped: true,
			Reason:  "table not present",
			FiredAt: firedAt,
		},
	}

	html := buildHTML(data)

	// 结构性检查
	mustContain := []string{
		"ClickHouse 诊断仪表盘",
		"chart.js",
		"const DATA =",
		"tables_list",
		"query_slow",
		"query_heavy",
		"query_by_user",
		"dictionaries",
		"crash_log",
		"chart-slow-queries",
		"chart-heavy-reads",
		"chart-user-activity",
		"chart-dict-status",
		"chart-dict-bytes",
		"chart-dict-lifetime",
		"tbl-explorer",
		"sec-crashlog",
		"tablesFilter",
		"24.8.5.115",
		// 告警
		"sec-alerts",
		"renderAlerts",
		"alerts-panel",
		"crash_log_entries",
		// skipped（不适用）alert 的机制：JS 过滤器、
		// 弱化提示样式，以及 skipped 规则的 payload 字段。
		"a.skipped",
		"alert-skipped",
		"不适用",
		`"skipped":true`,
		"text_log_alert",
	}
	for _, want := range mustContain {
		if !strings.Contains(html, want) {
			t.Errorf("生成的 HTML 缺少预期字符串：%q", want)
		}
	}

	// 验证嵌入的 JSON 有效
	start := strings.Index(html, "const DATA = ") + len("const DATA = ")
	end := strings.Index(html[start:], ";\n")
	if end < 0 {
		t.Fatal("找不到 DATA 赋值的结尾")
	}
	jsonStr := html[start : start+end]
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		t.Fatalf("嵌入的 DATA 不是有效 JSON：%v", err)
	}

	// 检查 JSON 中存在关键数据
	for _, key := range []string{"tables_list", "query_slow", "dictionaries", "crash_log", "alerts"} {
		if _, ok := out[key]; !ok {
			t.Errorf("DATA JSON 缺少键 %q", key)
		}
	}

	// 检查 alert 数据存在且正确
	alertsRaw, ok := out["alerts"]
	if !ok {
		t.Fatal("DATA 中缺少 alerts 键")
	}
	alertsSlice, ok := alertsRaw.([]interface{})
	if !ok {
		t.Fatalf("alerts 不是 slice，得到 %T", alertsRaw)
	}
	if len(alertsSlice) != 3 {
		t.Errorf("期望 3 条 alert 结果（fired + clean + skipped），得到 %d", len(alertsSlice))
	}

	if err := os.WriteFile("/tmp/dashboard_test.html", []byte(html), 0644); err == nil {
		t.Logf("Dashboard 已写入 /tmp/dashboard_test.html，供人工审阅")
	}
}

func TestTablesListSQL_Modes(t *testing.T) {
	// 在 cloud 模式下，system.tables 和 system.parts 在各 replica 之间共享，
	// 因此任何模式都不应把它们包进 clusterAllReplicas。
	for _, mode := range []string{"cloud", "onprem", "gov"} {
		g := &Generator{mode: mode}
		sql := g.tablesListSQL()
		if sql == "" {
			t.Errorf("模式 %q 返回了空的 tablesListSQL", mode)
		}
		if strings.Contains(sql, "clusterAllReplicas") {
			t.Errorf("模式 %q 不应为共享表使用 clusterAllReplicas", mode)
		}
	}
}

func TestSysTable_CloudSharedVsPerReplica(t *testing.T) {
	g := &Generator{mode: "cloud"}
	// 每 replica 的表 → 包裹
	if got := g.sysTable("query_log"); got != "clusterAllReplicas(default, system.query_log)" {
		t.Errorf("query_log：得到 %q", got)
	}
	// 共享表 → 原样
	if got := g.sysTable("parts"); got != "system.parts" {
		t.Errorf("parts：得到 %q", got)
	}
	if got := g.sysTable("tables"); got != "system.tables" {
		t.Errorf("tables：得到 %q", got)
	}
}

func TestDictionariesSQL_GovOmitsException(t *testing.T) {
	govGen := &Generator{mode: "gov"}
	sql := govGen.dictionariesSQL()
	if strings.Contains(sql, "last_exception") && !strings.Contains(sql, "'' AS last_exception") {
		t.Error("gov 模式不应直接暴露 last_exception")
	}

	cloudGen := &Generator{mode: "cloud"}
	sql2 := cloudGen.dictionariesSQL()
	if !strings.Contains(sql2, "last_exception") {
		t.Error("非 gov 模式应包含 last_exception")
	}
}

// 行数不能用字节格式化器渲染。formatReadableSize
// 曾把 diag_demo.events（1,992,000 行）变成字符串 "1.90 MiB"，Tables Explorer
// 和 Top Tables 面板都是如此。
func TestRowCountsUseQuantityFormatter(t *testing.T) {
	g := &Generator{mode: "onprem"}
	for name, sql := range map[string]string{
		"tablesListSQL": g.tablesListSQL(),
		"topTablesSQL":  g.topTablesSQL(),
	} {
		if !strings.Contains(sql, "formatReadableQuantity") {
			t.Errorf("%s：行数必须使用 formatReadableQuantity，得到：\n%s", name, sql)
		}
		if strings.Contains(sql, "formatReadableSize(sum(rows))") ||
			strings.Contains(sql, "formatReadableSize(coalesce(p.total_rows") {
			t.Errorf("%s：行数仍然被格式化为字节", name)
		}
	}
}

// error_count 在结构上不可达：它在 WHERE type = 'QueryFinish' 的窗口内统计
// exception_code != 0，而失败的查询永远不会被记录为 QueryFinish。
func TestQueryByUserSQL_CountsExceptionRows(t *testing.T) {
	sql := (&Generator{mode: "onprem"}).queryByUserSQL()

	for _, want := range []string{"ExceptionWhileProcessing", "ExceptionBeforeStart"} {
		if !strings.Contains(sql, want) {
			t.Errorf("query_by_user 必须包含 %s 行，否则 error_count 永远是 0：\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "AND type = 'QueryFinish'") {
		t.Error("query_by_user 不能把整个窗口限定为 QueryFinish")
	}
	// 开销聚合必须仍然只统计 QueryFinish，这样放宽窗口
	// 不会改变它们的含义。
	for _, want := range []string{
		"countIf(type = 'QueryFinish') AS executions",
		"sumIf(read_bytes, type = 'QueryFinish')",
		"sumIf(memory_usage, type = 'QueryFinish')",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("query_by_user 在开销聚合 %q 上丢失了 QueryFinish 限定：\n%s", want, sql)
		}
	}
	// 对全失败的用户使用 avgIf 会得到 nan，而 nan 无法在 JSON 中保留。
	if strings.Contains(sql, "avgIf(query_duration_ms") {
		t.Error("用 sum/greatest(count,1) 替代 avgIf：对全失败的用户，avgIf 会得到 nan")
	}
}
