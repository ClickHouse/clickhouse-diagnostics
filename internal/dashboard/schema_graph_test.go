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
			"", "", "", 0, 0, [2]string{"", ""}, nil, nil), // a View: the server lists no dependency
		tbl("shop", "users_src", "MergeTree", "MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"CREATE TABLE shop.users_src (`user_id` UInt64, `name` String) ENGINE = MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"user_id", "user_id", "", 1000, 30000, [2]string{"", ""}, [][2]string{{"shop", "dict_users"}}, nil),
		tbl("shop", "dict_users", "Dictionary", "Dictionary",
			"CREATE DICTIONARY shop.dict_users (`user_id` UInt64, `name` String) PRIMARY KEY user_id SOURCE(CLICKHOUSE(DB 'shop' TABLE 'users_src')) LIFETIME(MIN 0 MAX 60) LAYOUT(HASHED())",
			"", "", "", 1000, 65536, [2]string{"", ""}, nil, [][2]string{{"shop", "users_src"}}),
		tbl("shop", "events_dist", "Distributed", "Distributed('default', 'shop', 'events', rand())",
			"CREATE TABLE shop.events_dist (`ts` DateTime, `user_id` UInt64, `kind` LowCardinality(String), `amount` Float64) ENGINE = Distributed('default', 'shop', 'events', rand())",
			"", "", "", 0, 0, [2]string{"", ""}, nil, nil), // Distributed: the local table is only an engine argument
		tbl("shop", "rmv_daily", "MaterializedView", "MaterializedView",
			"CREATE MATERIALIZED VIEW shop.rmv_daily\nREFRESH EVERY 1 HOUR OFFSET 5 MINUTE RANDOMIZE FOR 1 MINUTE\n(\n    `day` Date,\n    `c` UInt64\n)\nENGINE = MergeTree\nORDER BY day\nAS SELECT\n    toDate(ts) AS day,\n    count() AS c\nFROM shop.events\nGROUP BY day",
			"", "", "", 0, 0, [2]string{"shop", ".inner_id.ab32b20f-6d2f-4554-80fc-3e501e082a40"}, nil, nil), // refreshable: not a dependent of its source
		// A 22.x-style DDL with credentials in the engine arguments, plus a
		// "</script>" in the comment to prove the embedding cannot be broken.
		tbl("lake", "raw_s3", "S3", "S3('https://bucket.s3.amazonaws.com/raw/*.parquet', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY', 'Parquet')",
			"CREATE TABLE lake.raw_s3 (`id` UInt64, `payload` String) ENGINE = S3('https://bucket.s3.amazonaws.com/raw/*.parquet', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY', 'Parquet') COMMENT 'loaded nightly </script><b>x</b>'",
			"", "", "", 0, 0, [2]string{"", ""}, nil, nil),
	}
	col := func(db, table, name, typ string, key, def int) map[string]interface{} {
		return map[string]interface{}{"database": db, "table": table, "name": name, "type": typ,
			"is_key": key, "pk": key, "sk": key, "pt": 0, "sm": 0, "has_default": def}
	}
	// A sorting-key column that is ALSO the partition key, and a sampling column.
	colRoles := func(db, table, name, typ string, pk, sk, pt, sm int) map[string]interface{} {
		return map[string]interface{}{"database": db, "table": table, "name": name, "type": typ,
			"is_key": pk | sk, "pk": pk, "sk": sk, "pt": pt, "sm": sm, "has_default": 0}
	}
	columns := []map[string]interface{}{
		colRoles("shop", "events", "ts", "DateTime", 0, 1, 1, 0), colRoles("shop", "events", "user_id", "UInt64", 0, 0, 0, 1),
		colRoles("shop", "events", "kind", "LowCardinality(String)", 1, 1, 0, 0), col("shop", "events", "amount", "Float64", 0, 0),
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
		{"database": "shop", "name": "dict_users", "source": "ClickHouse: shop.users_src", "status": "LOADED", "last_exception": "", "lifetime_min": "0", "lifetime_max": "60"},
		// FAILED dictionaries are the ones to look at first; the live page dropped them.
		{"database": "shop", "name": "dict_geo", "source": "MySQL: geo.regions", "status": "FAILED",
			"last_exception": "Code: 1000. DB::Exception: mysqlxx::ConnectionFailed: Can't connect to MySQL server on 'geo-db:3306' (password = 'geo\\'pw')"},
		// Declared in server config (XML): database is '' and there is no system.tables row.
		{"database": "", "name": "country_codes", "source": "File: /etc/clickhouse-server/dictionaries/countries.csv", "status": "LOADED", "last_exception": ""},
	}
	refreshes := []map[string]interface{}{
		{"database": "shop", "view": "rmv_daily", "status": "Scheduled",
			"last_success_time": "2026-09-25 14:00:03", "next_refresh_time": "2026-09-25 15:00:00", "exception": ""},
	}
	indices := []map[string]interface{}{
		{"database": "shop", "table": "events", "name": "idx_kind", "type": "bloom_filter", "expr": "kind", "granularity": "4"},
		{"database": "shop", "table": "events", "name": "idx_uid", "type": "minmax", "expr": "user_id", "granularity": "2"},
	}
	return map[string]interface{}{
		"generated_at": "2026-09-25 14:30:00 UTC", "version": "26.7.5.10", "mode": "onprem",
		"tables": tables, "columns": columns, "dictionaries": dicts, "refreshes": refreshes, "indices": indices,
		"has_target": true, "has_refresh": true, "ddl_formatted": true,
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
	redactSchemaRecords(fx["dictionaries"].([]map[string]interface{}), "source", "last_exception")
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
	// The S3 secret was redacted before embedding; the key id too; and the
	// password quoted inside a dictionary's last_exception.
	for _, gone := range []string{"wJalrXUtnFEMI", "AKIAIOSFODNN7EXAMPLE", "geo\\'pw", "geo'pw"} {
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
	want := map[string]interface{}{"file": "schema_graph.html", "tables": 11, "columns": 26, "databases": 2, "mvs": 3, "refreshable": 1, "dictionaries": 3, "indices": 2}
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
	redactSchemaRecords(fx["dictionaries"].([]map[string]interface{}), "source", "last_exception")
	html := buildSchemaGraphHTML(fx)
	if dir := os.Getenv("DASHBOARD_PREVIEW_DIR"); dir != "" {
		dst := filepath.Join(dir, "schema_graph_preview.html")
		if err := os.WriteFile(dst, []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("preview written: %s", dst)
	}
}

// Review findings on the payload queries and the dashboard's own panel.
func TestSchemaGraphQueries_ReviewFindings(t *testing.T) {
	// FAILED / FAILED_AND_RELOADING dictionaries must not be filtered out.
	if strings.Contains(schemaDictionariesSQL, "WHERE status") {
		t.Error("schema dictionaries query filters by status — failed dictionaries are the nodes to inspect")
	}
	for _, want := range []string{"status", "last_exception"} {
		if !strings.Contains(schemaDictionariesSQL, want) {
			t.Errorf("schema dictionaries query lost %q", want)
		}
	}
	// view_refreshes goes through the mode-aware reference and folds replicas.
	cloud := schemaRefreshesSQL(NewGenerator(nil, "cloud").sysTable("view_refreshes"))
	if !strings.Contains(cloud, "clusterAllReplicas(default, system.view_refreshes)") {
		t.Errorf("cloud refreshes query reads the local table only:\n%s", cloud)
	}
	for _, want := range []string{"GROUP BY database, view", "RunningOnAnotherReplica", "argMax(exception"} {
		if !strings.Contains(cloud, want) {
			t.Errorf("refreshes query lost %q", want)
		}
	}
	if onprem := schemaRefreshesSQL(NewGenerator(nil, "onprem").sysTable("view_refreshes")); !strings.Contains(onprem, "FROM system.view_refreshes") {
		t.Errorf("onprem refreshes query: %s", onprem)
	}

	// The dashboard's Dictionaries panel embeds source / last_exception too.
	rows := []map[string]interface{}{
		{"name": "d", "source": "MySQL('h:3306', 'db', 'tbl', 'u', 'pw')", "last_exception": "cannot connect: password = 'pw2'"},
	}
	if n := redactDictionaryPanel(rows); n != 2 {
		t.Errorf("panel redaction count = %d, want 2", n)
	}
	if strings.Contains(rows[0]["source"].(string), "'pw'") || strings.Contains(rows[0]["last_exception"].(string), "pw2") {
		t.Errorf("panel rows still carry credentials: %v", rows[0])
	}
}

// Review finding: nodes were clickable <div>s with no keyboard path.
func TestSchemaGraphTemplate_KeyboardAccessible(t *testing.T) {
	for _, want := range []string{
		"el.tabIndex = 0;", "el.setAttribute('role', 'button');", "el.setAttribute('aria-label'",
		"e.key === 'Enter' || e.key === ' '", ".node:focus-visible", "a.href = '#';",
		// config-declared dictionaries get a node; failed ones show status and error
		"'(config)'", "Dictionary status", "Dictionary error",
	} {
		if !strings.Contains(schemaGraphTail, want) {
			t.Errorf("schema graph page lost %q", want)
		}
	}
}

// Review finding: the collector returned before reading system.dictionaries
// when system.tables had no user rows, so a server whose only user objects
// are config-declared dictionaries got no graph despite the page knowing
// how to draw them.
func TestSchemaGraphHasContent(t *testing.T) {
	d := []map[string]interface{}{{"database": "", "name": "country_codes"}}
	tb := []map[string]interface{}{{"database": "shop", "name": "events"}}
	if !schemaGraphHasContent(nil, d) {
		t.Error("dictionaries alone must produce a graph")
	}
	if !schemaGraphHasContent(tb, nil) {
		t.Error("tables alone must produce a graph")
	}
	if schemaGraphHasContent(nil, nil) {
		t.Error("nothing to draw must produce no page")
	}
	// The summary counts a dictionaries-only payload honestly.
	got := schemaGraphSummary(map[string]interface{}{"tables": []map[string]interface{}{}, "dictionaries": d})
	if got["tables"] != 0 || got["dictionaries"] != 1 {
		t.Errorf("summary = %v", got)
	}
}

// Reviewer suggestions on #34: the RMV schedule and the dictionary LIFETIME on
// the node ribbon, key roles differentiated (and skip indices shown), and a
// pretty-printed, highlighted CREATE statement.
func TestSchemaGraph_ReviewerSuggestions(t *testing.T) {
	for _, want := range []string{
		// 1. schedule parsed from the DDL (system.view_refreshes has no interval)
		"function parseRefresh(ddl)", "RANDOMIZE", "no RANDOMIZE FOR — every copy of this schedule fires at the same instant", "'Refresh schedule'",
		// 2. LIFETIME from the columns, DDL as the fallback for an unloaded dictionary
		"function dictLifetime(n)", "'Dictionary lifetime'",
		// 3. server-side pretty print + client-side highlight, spans only
		"function highlightSQL(pre, text)", "syn-hidden", "highlightSQL(pre, n.createQuery);",
		// 4. four independent key roles, a legend for them, tags in the sidebar, skip indices
		"function colRoles(c)", "key-primary", "key-sorting", "key-partition", "key-sampling",
		"class=\"legend-col column-name key-partition\"", "'Skip indices ('", "role-tag",
	} {
		if !strings.Contains(schemaGraphTail, want) {
			t.Errorf("schema graph page lost %q", want)
		}
	}
	// The highlighter must not take the innerHTML shortcut.
	hl := schemaGraphTail[strings.Index(schemaGraphTail, "function highlightSQL"):]
	hl = hl[:strings.Index(hl, "function engineKind")]
	if strings.Contains(hl, "innerHTML") {
		t.Error("highlightSQL assigns innerHTML — customer DDL must go through textContent")
	}
	// Payload side.
	for _, want := range []string{"is_in_partition_key AS pt", "is_in_sampling_key  AS sm", "formatQueryOrNull(create_table_query)", "fn:formatQueryOrNull"} {
		if !strings.Contains(readSchemaGraphSource(t), want) {
			t.Errorf("collectSchemaGraph lost %q", want)
		}
	}
	if !strings.Contains(schemaDictionariesSQL, "lifetime_min, lifetime_max") {
		t.Error("dictionaries query lost the LIFETIME columns")
	}
	if !strings.Contains(schemaIndicesSQL, "system.data_skipping_indices") || !strings.Contains(schemaIndicesSQL, "granularity") {
		t.Error("indices query lost its table or granularity")
	}
	// A page built from the fixture carries the schedule text, the indices and the lifetime.
	fx := schemaGraphFixture()
	html := buildSchemaGraphHTML(fx)
	for _, want := range []string{"REFRESH EVERY 1 HOUR OFFSET 5 MINUTE RANDOMIZE FOR 1 MINUTE", `"idx_kind"`, `"bloom_filter"`, `"lifetime_max":"60"`, `"pt":1`, `"sm":1`} {
		if !strings.Contains(html, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func readSchemaGraphSource(t *testing.T) string {
	b, err := os.ReadFile("schema_graph.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Reviewer follow-up on #34: a refreshable MV, a plain view and a Distributed
// table have empty dependency arrays on the server, so they floated free. The
// page now infers their source edges from the DDL and draws them dashed.
func TestSchemaGraph_InferredEdgesFromDDL(t *testing.T) {
	for _, want := range []string{
		"function parseSourceTables(ddl, defaultDb)", "function parseDistributedSource(engineFull, defaultDb)",
		"addEdge(src, node.key, 'mv', true);", "addEdge(src, node.key, 'distributed', true);",
		"inferred: !!isInferred", ".arrow.inferred{stroke-dasharray:6 4}", "inferred from DDL</span>",
		"'· inferred from the DDL'",
	} {
		if !strings.Contains(schemaGraphTail, want) {
			t.Errorf("schema graph page lost %q", want)
		}
	}
	// Metadata edges must be added before inferred ones so a real dependency is
	// never demoted to dashed: the second pass comes after the first loop.
	first := strings.Index(schemaGraphTail, "for (const node of nodes.values()) {\n        /// MV/RMV: SELECT FROM dependsOn")
	second := strings.Index(schemaGraphTail, "/// Second pass, after every metadata edge is in")
	if first < 0 || second < 0 || second < first {
		t.Error("inferred edges must be added after the metadata pass")
	}
}
