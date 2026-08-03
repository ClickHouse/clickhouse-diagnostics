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
			{"hash": "12345678901234", "query_kind": "Select", "user": "default", "executions": 500, "avg_duration_ms": 4200, "max_duration_ms": 18000, "avg_read_mb": 350.5, "avg_memory_mb": 128.3, "errors": 0},
			{"hash": "98765432109876", "query_kind": "Select", "user": "analyst", "executions": 120, "avg_duration_ms": 1800, "max_duration_ms": 5200, "avg_read_mb": 820.0, "avg_memory_mb": 256.0, "errors": 2},
		},
		"query_heavy": []map[string]interface{}{
			{"hash": "98765432109876", "query_kind": "Select", "user": "analyst", "executions": 120, "avg_read_mb": 820.0, "total_read": "98.40 GiB", "avg_duration_ms": 1800},
			{"hash": "12345678901234", "query_kind": "Select", "user": "default", "executions": 500, "avg_read_mb": 350.5, "total_read": "175.25 GiB", "avg_duration_ms": 4200},
		},
		"query_by_user": []map[string]interface{}{
			{"user": "default", "executions": 15000, "avg_duration_ms": 45, "total_read_gb": 175.25, "total_memory_gb": 12.5, "error_count": 5},
			{"user": "analyst", "executions": 3200, "avg_duration_ms": 1800, "total_read_gb": 98.40, "total_memory_gb": 30.2, "error_count": 12},
		},
		"exceptions": []map[string]interface{}{
			{"exception_code": "241", "count": 15, "msg": "Memory limit exceeded"},
			{"exception_code": "60", "count": 8, "msg": "Table doesn't exist"},
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

	// Add mock alert results (one fired, one not)
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
		// A skipped (not-applicable) rule: its table doesn't exist on this
		// server. Must NOT count as evaluated and must render in the
		// muted "not applicable" note, not as a fired/passed check.
		{
			Name:    "text_log_alert",
			Title:   "Text log errors",
			Skipped: true,
			Reason:  "table not present",
			FiredAt: firedAt,
		},
	}

	html := buildHTML(data)

	// structural checks
	mustContain := []string{
		"ClickHouse Diagnostic Dashboard",
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
		// alerts
		"sec-alerts",
		"renderAlerts",
		"alerts-panel",
		"crash_log_entries",
		// skipped (not-applicable) alert plumbing: the JS filter, the
		// muted note style, and the skipped rule's payload fields.
		"a.skipped",
		"alert-skipped",
		"not applicable",
		`"skipped":true`,
		"text_log_alert",
	}
	for _, want := range mustContain {
		if !strings.Contains(html, want) {
			t.Errorf("generated HTML missing expected string: %q", want)
		}
	}

	// verify embedded JSON is valid
	start := strings.Index(html, "const DATA = ") + len("const DATA = ")
	end := strings.Index(html[start:], ";\n")
	if end < 0 {
		t.Fatal("could not find DATA assignment end")
	}
	jsonStr := html[start : start+end]
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		t.Fatalf("embedded DATA is not valid JSON: %v", err)
	}

	// check key data is present in JSON
	for _, key := range []string{"tables_list", "query_slow", "dictionaries", "crash_log", "alerts"} {
		if _, ok := out[key]; !ok {
			t.Errorf("DATA JSON missing key %q", key)
		}
	}

	// check alert data is present and correct
	alertsRaw, ok := out["alerts"]
	if !ok {
		t.Fatal("alerts key missing from DATA")
	}
	alertsSlice, ok := alertsRaw.([]interface{})
	if !ok {
		t.Fatalf("alerts is not a slice, got %T", alertsRaw)
	}
	if len(alertsSlice) != 3 {
		t.Errorf("expected 3 alert results (fired + clean + skipped), got %d", len(alertsSlice))
	}

	if err := os.WriteFile("/tmp/dashboard_test.html", []byte(html), 0644); err == nil {
		t.Logf("Dashboard written to /tmp/dashboard_test.html for manual review")
	}
}

func TestTablesListSQL_Modes(t *testing.T) {
	// system.tables and system.parts are shared across replicas in cloud,
	// so no mode should wrap them with clusterAllReplicas.
	for _, mode := range []string{"cloud", "onprem", "gov"} {
		g := &Generator{mode: mode}
		sql := g.tablesListSQL()
		if sql == "" {
			t.Errorf("mode %q returned empty tablesListSQL", mode)
		}
		if strings.Contains(sql, "clusterAllReplicas") {
			t.Errorf("mode %q should not use clusterAllReplicas for shared tables", mode)
		}
	}
}

func TestSysTable_CloudSharedVsPerReplica(t *testing.T) {
	g := &Generator{mode: "cloud"}
	// per-replica tables → wrapped
	if got := g.sysTable("query_log"); got != "clusterAllReplicas(default, system.query_log)" {
		t.Errorf("query_log: got %q", got)
	}
	// shared tables → plain
	if got := g.sysTable("parts"); got != "system.parts" {
		t.Errorf("parts: got %q", got)
	}
	if got := g.sysTable("tables"); got != "system.tables" {
		t.Errorf("tables: got %q", got)
	}
}

func TestDictionariesSQL_GovOmitsException(t *testing.T) {
	govGen := &Generator{mode: "gov"}
	sql := govGen.dictionariesSQL()
	if strings.Contains(sql, "last_exception") && !strings.Contains(sql, "'' AS last_exception") {
		t.Error("gov mode should not expose last_exception directly")
	}

	cloudGen := &Generator{mode: "cloud"}
	sql2 := cloudGen.dictionariesSQL()
	if !strings.Contains(sql2, "last_exception") {
		t.Error("non-gov mode should include last_exception")
	}
}
