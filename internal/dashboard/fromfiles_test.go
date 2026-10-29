package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/bundle"
)

type frow = map[string]interface{}

// runDir writes collector files the way the executor names them.
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
		t.Fatal("no DATA in page")
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

// Built from a run directory, the dashboard sends nothing to the server:
// the generator has no client at all, so any query would panic.
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
			t.Errorf("%s: %s", name, b)
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
	// The hash stays text: JavaScript would round a UInt64.
	check("query_slow", first("query_slow")["hash"] == "18446744073709551615" && first("query_slow")["executions"] == 8.0 &&
		first("query_slow")["avg_duration_ms"] == 100.0 && first("query_slow")["errors"] == 4.0)
	check("query_heavy", first("query_heavy")["executions"] == 10.0 && first("query_heavy")["avg_read_mb"] == 8.0)
	check("exceptions", first("exceptions")["exception_code"] == 999.0 && first("exceptions")["msg"] == "Keeper gone" && first("exceptions")["count"] == 4.0)
	check("keeper_errors_hourly", first("keeper_errors_hourly")["code_name"] == "KEEPER_EXCEPTION" && first("keeper_errors_hourly")["count"] == 4.0)
	check("mutations", len(p["mutations"].([]interface{})) == 1)
	check("keeper_metric_hourly", first("keeper_metric_hourly")["wait_us"] == 5000.0 && p["keeper_wait_available"] == true)
	check("pools_hourly", first("pools_hourly")["fetch_tasks"] == 3.5)
	// 23.3+: server_settings, not the obsolete session default in settings.
	check("pool_sizes", p["pool_sizes"].(map[string]interface{})["fetch"] == 16.0)
	check("pools_now", p["pools_now"] == "2026-10-29 12:00:00")
	check("disks", first("disks")["free_pct"] == 25.0 && first("disks")["hostname"] == "ch-01")
	check("text_log", len(p["text_log"].([]interface{})) == 1 && len([]rune(first("text_log")["message"].(string))) == textLogMessageCap &&
		first("text_log")["hostname"] == "ch-01")
	sp := p["collect"].(map[string]interface{})["skipped_panels"].([]interface{})
	check("collect", len(sp) == 1 && strings.Contains(sp[0].(string), "system.detached_parts skipped"))
	if _, err := os.Stat(filepath.Join(dir, schemaGraphFile)); err != nil {
		t.Errorf("schema graph not written from files: %v", err)
	}
}

// Cloud lists a shared detached part once per replica, a mutation once per
// replica, and the replicas of every host: each is folded back.
func TestFromFiles_CloudFolds(t *testing.T) {
	var det []bundle.Row
	for i := 0; i < 3; i++ {
		det = append(det, bundle.Row{"database": "db", "table": "a", "reason": "broken", "bytes_on_disk": "1024", "path": "/s3/x/detached/broken_all_1_1_0/"})
	}
	if d := detachedPanel(det, "cloud"); len(d) != 1 || d[0]["count"] != int64(1) || d[0]["size"] != "1.00 KiB" {
		t.Errorf("detached: %v", d)
	}
	if d := detachedPanel(det, "onprem"); d[0]["count"] != int64(3) {
		t.Errorf("onprem keeps every row: %v", d)
	}
	reps := []bundle.Row{{"hostname": "r1", "database": "db", "table": "a"}, {"hostname": "r2", "database": "db", "table": "a"}}
	problem := []bundle.Row{{"hostname": "r1", "database": "db", "table": "a", "total_replicas": "3", "active_replicas": "2"},
		{"hostname": "r2", "database": "db", "table": "a", "total_replicas": "3", "active_replicas": "1"}}
	if r := replicasPanel(reps, problem, "cloud", "r1"); len(r) != 1 || r[0]["active_replicas"] != "2" {
		t.Errorf("replicas: the serving node only, with its counts: %v", r)
	}
}

// Below 23.3 the pool sizes are in system.settings.
func TestFromFiles_PoolSizesByVersion(t *testing.T) {
	dir := runDir(t, map[string][]frow{
		"system.settings":        {{"name": "background_fetches_pool_size", "value": "8"}},
		"system.server_settings": {{"name": "background_fetches_pool_size", "value": "16"}},
	})
	g := NewGenerator(nil, "onprem").WithServerVersion(internal.Version{Major: 22, Minor: 8}).WithBundle(bundle.Open(dir, nil))
	if got := g.poolSizesFromFiles()["fetch"]; got != 8 {
		t.Errorf("22.8: want 8 from system.settings, got %d", got)
	}
}

// A source that was never collected (absent on this version) is an empty
// panel, not a banner entry; skipped or failed is listed.
func TestFromFiles_NotCollectedIsQuiet(t *testing.T) {
	dir := runDir(t, map[string][]frow{})
	g := NewGenerator(nil, "onprem").WithBundle(bundle.Open(dir, map[string]bundle.Status{"system.errors": {State: "failed", Reason: "timeout"}}))
	g.filesRows("crash log", "system.crash_log")
	g.filesRows("server errors", "system.errors")
	if len(g.skippedPanels) != 1 || !strings.Contains(g.skippedPanels[0], "system.errors failed") {
		t.Errorf("skipped panels: %v", g.skippedPanels)
	}
}

func TestReadableFormats(t *testing.T) {
	for in, want := range map[float64]string{0: "0.00 B", 1536: "1.50 KiB", 3 << 30: "3.00 GiB"} {
		if got := readableSize(in); got != want {
			t.Errorf("readableSize(%v) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[float64]string{500: "500.00", 1500000: "1.50 million", 2e9: "2.00 billion"} {
		if got := readableQuantity(in); got != want {
			t.Errorf("readableQuantity(%v) = %s, want %s", in, got, want)
		}
	}
}
