package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// schemaGraphFixture is a small pipeline with every object kind the graph
// distinguishes: a MergeTree source, an MV writing to an explicit target, an
// MV with an implicit .inner_id target, a plain view, a dictionary reading a
// table, a Distributed table over the source, and a refreshable MV. The DDL
// of one table carries S3 credentials the way a 22.x server would ship them.
//
//	DASHBOARD_PREVIEW_DIR=/tmp go test ./internal/dashboard -run TestBuildSchemaGraphHTML_Preview
//
// writes /tmp/schema_graph_preview.html.
func schemaGraphFixture() map[string]interface{} {
	tbl := func(db, name, engine, engineFull, ddl, sort, pk, part string, rows, bytes int64,
		target [2]string, deps, loadDeps [][2]string) map[string]interface{} {
		zip := func(ps [][2]string) ([]interface{}, []interface{}) {
			a, b := []interface{}{}, []interface{}{}
			for _, p := range ps {
				a = append(a, p[0])
				b = append(b, p[1])
			}
			return a, b
		}
		dd, dt := zip(deps)
		ld, lt := zip(loadDeps)
		return map[string]interface{}{
			"database": db, "name": name, "engine": engine, "engine_full": engineFull,
			"create_table_query": ddl, "sorting_key": sort, "primary_key": pk, "partition_key": part,
			"sampling_key": "", "total_rows": jsonNum(rows), "total_bytes": jsonNum(bytes), "comment": "",
			"target_database": target[0], "target_table": target[1],
			"dependencies_database": dd, "dependencies_table": dt,
			"loading_dependencies_database": ld, "loading_dependencies_table": lt,
		}
	}
	inner := ".inner_id.7fdd1fdf-a1f8-4ebe-ba11-ffb805a93402"
	tables := []map[string]interface{}{
		tbl("shop", "events", "MergeTree", "MergeTree PARTITION BY toYYYYMM(ts) ORDER BY (kind, ts) SETTINGS index_granularity = 8192",
			"CREATE TABLE shop.events (`ts` DateTime, `user_id` UInt64, `kind` LowCardinality(String), `amount` Float64) ENGINE = MergeTree PARTITION BY toYYYYMM(ts) ORDER BY (kind, ts) SETTINGS index_granularity = 8192",
			"kind, ts", "kind, ts", "toYYYYMM(ts)", 50000, 812345, [2]string{"", ""},
			[][2]string{{"shop", "mv_events_by_kind"}, {"shop", "mv_inner"}, {"shop", "v_recent"}, {"shop", "rmv_daily"}}, nil),
		tbl("shop", "events_by_kind", "SummingMergeTree", "SummingMergeTree ORDER BY (kind, day) SETTINGS index_granularity = 8192",
			"CREATE TABLE shop.events_by_kind (`kind` LowCardinality(String), `day` Date, `cnt` UInt64, `total` Float64) ENGINE = SummingMergeTree ORDER BY (kind, day) SETTINGS index_granularity = 8192",
			"kind, day", "kind, day", "", 900, 40960, [2]string{"", ""}, nil, nil),
		tbl("shop", "mv_events_by_kind", "MaterializedView", "MaterializedView",
			"CREATE MATERIALIZED VIEW shop.mv_events_by_kind TO shop.events_by_kind (`kind` LowCardinality(String), `day` Date, `cnt` UInt64, `total` Float64) AS SELECT kind, toDate(ts) AS day, count() AS cnt, sum(amount) AS total FROM shop.events GROUP BY kind, day",
			"", "", "", 0, 0, [2]string{"shop", "events_by_kind"}, nil, [][2]string{{"shop", "events"}}),
		tbl("shop", "mv_inner", "MaterializedView", "MaterializedView",
			"CREATE MATERIALIZED VIEW shop.mv_inner (`user_id` UInt64, `last_seen` DateTime) ENGINE = MergeTree ORDER BY user_id AS SELECT user_id, max(ts) AS last_seen FROM shop.events GROUP BY user_id",
			"", "", "", 0, 0, [2]string{"shop", inner}, nil, [][2]string{{"shop", "events"}}),
		tbl("shop", inner, "MergeTree", "MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"CREATE TABLE shop.`"+inner+"` (`user_id` UInt64, `last_seen` DateTime) ENGINE = MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"user_id", "user_id", "", 1000, 24000, [2]string{"", ""}, nil, nil),
		tbl("shop", "v_recent", "View", "View",
			"CREATE VIEW shop.v_recent (`ts` DateTime, `user_id` UInt64, `kind` LowCardinality(String), `amount` Float64) AS SELECT * FROM shop.events WHERE ts > (now() - toIntervalDay(1))",
			"", "", "", 0, 0, [2]string{"", ""}, nil, [][2]string{{"shop", "events"}}),
		tbl("shop", "users_src", "MergeTree", "MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"CREATE TABLE shop.users_src (`user_id` UInt64, `name` String) ENGINE = MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"user_id", "user_id", "", 1000, 30000, [2]string{"", ""}, [][2]string{{"shop", "dict_users"}}, nil),
		tbl("shop", "dict_users", "Dictionary", "Dictionary",
			"CREATE DICTIONARY shop.dict_users (`user_id` UInt64, `name` String) PRIMARY KEY user_id SOURCE(CLICKHOUSE(DB 'shop' TABLE 'users_src')) LIFETIME(MIN 0 MAX 60) LAYOUT(HASHED())",
			"", "", "", 1000, 65536, [2]string{"", ""}, nil, [][2]string{{"shop", "users_src"}}),
		tbl("shop", "events_dist", "Distributed", "Distributed('default', 'shop', 'events', rand())",
			"CREATE TABLE shop.events_dist (`ts` DateTime, `user_id` UInt64, `kind` LowCardinality(String), `amount` Float64) ENGINE = Distributed('default', 'shop', 'events', rand())",
			"", "", "", 0, 0, [2]string{"", ""}, nil, [][2]string{{"shop", "events"}}),
		tbl("shop", "rmv_daily", "MaterializedView", "MaterializedView",
			"CREATE MATERIALIZED VIEW shop.rmv_daily REFRESH EVERY 1 HOUR (`day` Date, `c` UInt64) ENGINE = MergeTree ORDER BY day AS SELECT toDate(ts) AS day, count() AS c FROM shop.events GROUP BY day",
			"", "", "", 0, 0, [2]string{"shop", ".inner_id.ab32b20f-6d2f-4554-80fc-3e501e082a40"}, nil, [][2]string{{"shop", "events"}}),
		// A 22.x-style DDL with credentials in the engine arguments, plus a
		// "</script>" in the comment to prove the embedding cannot be broken.
		tbl("lake", "raw_s3", "S3", "S3('https://bucket.s3.amazonaws.com/raw/*.parquet', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY', 'Parquet')",
			"CREATE TABLE lake.raw_s3 (`id` UInt64, `payload` String) ENGINE = S3('https://bucket.s3.amazonaws.com/raw/*.parquet', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY', 'Parquet') COMMENT 'loaded nightly </script><b>x</b>'",
			"", "", "", 0, 0, [2]string{"", ""}, nil, nil),
	}
	col := func(db, table, name, typ string, key, def int) map[string]interface{} {
		return map[string]interface{}{"database": db, "table": table, "name": name, "type": typ, "is_key": key, "has_default": def}
	}
	columns := []map[string]interface{}{
		col("shop", "events", "ts", "DateTime", 1, 0), col("shop", "events", "user_id", "UInt64", 0, 0),
		col("shop", "events", "kind", "LowCardinality(String)", 1, 0), col("shop", "events", "amount", "Float64", 0, 0),
		col("shop", "events_by_kind", "kind", "LowCardinality(String)", 1, 0), col("shop", "events_by_kind", "day", "Date", 1, 0),
		col("shop", "events_by_kind", "cnt", "UInt64", 0, 0), col("shop", "events_by_kind", "total", "Float64", 0, 0),
		col("shop", "mv_events_by_kind", "kind", "LowCardinality(String)", 0, 0), col("shop", "mv_events_by_kind", "day", "Date", 0, 0),
		col("shop", "mv_inner", "user_id", "UInt64", 0, 0), col("shop", "mv_inner", "last_seen", "DateTime", 0, 0),
		col("shop", inner, "user_id", "UInt64", 1, 0), col("shop", inner, "last_seen", "DateTime", 0, 0),
		col("shop", "v_recent", "ts", "DateTime", 0, 0), col("shop", "v_recent", "user_id", "UInt64", 0, 0),
		col("shop", "users_src", "user_id", "UInt64", 1, 0), col("shop", "users_src", "name", "String", 0, 0),
		col("shop", "dict_users", "user_id", "UInt64", 1, 0), col("shop", "dict_users", "name", "String", 0, 0),
		col("shop", "events_dist", "ts", "DateTime", 0, 0), col("shop", "events_dist", "user_id", "UInt64", 0, 0),
		col("shop", "rmv_daily", "day", "Date", 0, 0), col("shop", "rmv_daily", "c", "UInt64", 0, 0),
		col("lake", "raw_s3", "id", "UInt64", 0, 0), col("lake", "raw_s3", "payload", "String", 0, 1),
	}
	dicts := []map[string]interface{}{
		{"database": "shop", "name": "dict_users", "source": "ClickHouse: shop.users_src"},
	}
	refreshes := []map[string]interface{}{
		{"database": "shop", "view": "rmv_daily", "status": "Scheduled",
			"last_success_time": "2026-09-25 14:00:03", "next_refresh_time": "2026-09-25 15:00:00", "exception": ""},
	}
	return map[string]interface{}{
		"generated_at": "2026-09-25 14:30:00 UTC", "version": "26.7.5.10", "mode": "onprem",
		"tables": tables, "columns": columns, "dictionaries": dicts, "refreshes": refreshes,
		"has_target": true, "has_refresh": true,
	}
}

// jsonNum mimics how the JSONCompact response arrives: 64-bit integers are
// quoted strings (output_format_json_quote_64bit_integers=1).
func jsonNum(n int64) interface{} { return strconv.FormatInt(n, 10) }

// The page must work from disk with the network off: no live queries, no
// credential handling, no CDN. It must also say where it came from.
func TestSchemaGraphTemplate_IsOfflineSelfContainedAndAttributed(t *testing.T) {
	for _, banned := range []string{
		"fetch(", "XMLHttpRequest", "chQuery(", "navigator.credentials", "PasswordCredential",
		"connection-form", `id="password"`, "https://cdn.", "cdnjs", "jsdelivr", "unpkg",
		"loadViewsLoad", "query_views_log", // the heat map is a follow-up
	} {
		if strings.Contains(schemaGraphTemplate, banned) {
			t.Errorf("schema graph page must not contain %q", banned)
		}
	}
	for _, want := range []string{
		"Adapted from ClickHouse programs/server/schema.html", "Apache License, Version 2.0",
		"const DATA = /*DATA*/null;", "function loadFromBundle()", "chdiag-theme", "addEventListener('storage'",
		"--surface-card", "--click-font-mono", // the shared token layer is present
		"function backQuoteIfNeed", "function layoutDatabase", "function showSidebar", "function attachDrag",
		`id="back-link"`, "window.self !== window.top",
	} {
		if !strings.Contains(schemaGraphTemplate, want) {
			t.Errorf("schema graph page lost %q", want)
		}
	}
	// --link is the dashboard's hyperlink token; the edge colour must not shadow it.
	if strings.Contains(schemaGraphTail, "--link:") {
		t.Error("schema graph redefines --link — use --edge for the SVG stroke")
	}
	// Every customer string goes through textContent; innerHTML is only ever
	// assigned static markup or cleared.
	for _, line := range strings.Split(schemaGraphTail, "\n") {
		if strings.Contains(line, ".innerHTML") && !strings.Contains(line, "innerHTML = ''") &&
			!strings.Contains(line, `innerHTML = '<option value="">All databases</option>'`) {
			t.Errorf("innerHTML with dynamic content: %s", strings.TrimSpace(line))
		}
	}
}

func TestBuildSchemaGraphHTML_EmbedsFixtureAndCannotBreakOutOfTheScript(t *testing.T) {
	fx := schemaGraphFixture()
	redactSchemaRecords(fx["tables"].([]map[string]interface{}), "create_table_query", "engine_full")
	html := buildSchemaGraphHTML(fx)

	start := strings.Index(html, "const DATA = ") + len("const DATA = ")
	end := strings.Index(html[start:], ";\n")
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(html[start:start+end]), &out); err != nil {
		t.Fatalf("embedded DATA is not valid JSON: %v", err)
	}
	if n := len(out["tables"].([]interface{})); n != 11 {
		t.Errorf("tables embedded: %d, want 11", n)
	}
	for _, want := range []string{`"mv_events_by_kind"`, `"dict_users"`, `"events_dist"`, `"rmv_daily"`, `"Scheduled"`, `.inner_id.7fdd1fdf`} {
		if !strings.Contains(html, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The S3 secret was redacted before embedding; the key id too.
	for _, gone := range []string{"wJalrXUtnFEMI", "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(html, gone) {
			t.Errorf("credential %q reached the page", gone)
		}
	}
	if !strings.Contains(html, "[HIDDEN]") {
		t.Error("redaction token missing from the embedded DDL")
	}
	// "</script>" inside a DDL comment must be HTML-escaped by the JSON encoder:
	// the page ends up with exactly the closing tags its template has (the
	// theme stamp in <head> and the main script), and not one more.
	if got, want := strings.Count(html, "</script>"), strings.Count(schemaGraphTemplate, "</script>"); got != want {
		t.Errorf("a customer string terminated the inline script: %d </script> tags, want %d", got, want)
	}
	if !strings.Contains(html, `\u003c/script\u003e`) {
		t.Error("expected the DDL's </script> to arrive as \\u003c/script\\u003e")
	}
}

func TestRedactSchemaRecords(t *testing.T) {
	rows := []map[string]interface{}{
		{"create_table_query": "CREATE TABLE d.t (x UInt8) ENGINE = MySQL('h', 'db', 'tbl', 'u', 'pw')", "engine_full": "MySQL('h', 'db', 'tbl', 'u', 'pw')", "name": "pw"},
		{"create_table_query": "CREATE TABLE d.u (x UInt8) ENGINE = MergeTree ORDER BY x", "engine_full": "MergeTree ORDER BY x"},
		{"create_table_query": nil},
	}
	n := redactSchemaRecords(rows, "create_table_query", "engine_full")
	if n != 2 {
		t.Errorf("replacements = %d, want 2", n)
	}
	if strings.Contains(rows[0]["create_table_query"].(string), "'pw'") || strings.Contains(rows[0]["engine_full"].(string), "'pw'") {
		t.Errorf("password survived: %v", rows[0])
	}
	if rows[0]["name"] != "pw" {
		t.Error("a field outside the list was touched")
	}
	if rows[1]["engine_full"] != "MergeTree ORDER BY x" {
		t.Error("a clean row was rewritten")
	}
}

func TestSchemaGraphSummary(t *testing.T) {
	got := schemaGraphSummary(schemaGraphFixture())
	want := map[string]interface{}{"file": "schema_graph.html", "tables": 11, "columns": 26, "databases": 2, "mvs": 3, "refreshable": 1, "dictionaries": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

// dashboard.html's side: the tab exists, is hidden until the payload says the
// page was written, and the frame gets its src only inside the click handler.
func TestDashboardTemplate_SchemaTabLoadsOnDemand(t *testing.T) {
	for _, want := range []string{
		`id="nav-schema" style="display:none"`, `<section id="sec-schema" style="display:none">`,
		`<iframe id="schema-frame"`, `id="schema-load"`, `id="schema-open"`,
		"const sg=DATA.schema_graph;", "frame.src=sg.file;",
		"document.getElementById('nav-schema').addEventListener('click', load);",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("dashboard lost %q", want)
		}
	}
	if strings.Contains(htmlTemplate, `<iframe id="schema-frame" src=`) {
		t.Error("the schema frame must not carry a static src — that would load the graph for every reader")
	}
	// Without the payload key nothing is shown: the frame stays unassigned.
	html := buildHTML(map[string]interface{}{"alerts": []interface{}{}})
	if strings.Contains(html, `"schema_graph"`) {
		t.Error("schema_graph key present without a generated page")
	}
}

func TestBuildSchemaGraphHTML_Preview(t *testing.T) {
	fx := schemaGraphFixture()
	redactSchemaRecords(fx["tables"].([]map[string]interface{}), "create_table_query", "engine_full")
	html := buildSchemaGraphHTML(fx)
	if dir := os.Getenv("DASHBOARD_PREVIEW_DIR"); dir != "" {
		dst := filepath.Join(dir, "schema_graph_preview.html")
		if err := os.WriteFile(dst, []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("preview written: %s", dst)
	}
}
