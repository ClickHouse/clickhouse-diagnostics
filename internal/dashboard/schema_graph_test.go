package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// schemaGraphFixture 是一个小型管道，包含图所区分的每种对象类型：一个
// MergeTree 源表、一个写入显式目标的 MV、一个带隐式 .inner_id 目标的 MV、
// 一个普通视图、一个读取表的字典、一个基于源表的 Distributed 表，以及一个
// 可刷新 MV。其中一张表的 DDL 带有 S3 凭据，就像 22.x 服务器会带的那样。
//
//	DASHBOARD_PREVIEW_DIR=/tmp go test ./internal/dashboard -run TestBuildSchemaGraphHTML_Preview
//
// 写入 /tmp/schema_graph_preview.html。
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
			"", "", "", 0, 0, [2]string{"", ""}, nil, nil), // 普通 View：服务器不列出依赖
		tbl("shop", "users_src", "MergeTree", "MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"CREATE TABLE shop.users_src (`user_id` UInt64, `name` String) ENGINE = MergeTree ORDER BY user_id SETTINGS index_granularity = 8192",
			"user_id", "user_id", "", 1000, 30000, [2]string{"", ""}, [][2]string{{"shop", "dict_users"}}, nil),
		tbl("shop", "dict_users", "Dictionary", "Dictionary",
			"CREATE DICTIONARY shop.dict_users (`user_id` UInt64, `name` String) PRIMARY KEY user_id SOURCE(CLICKHOUSE(DB 'shop' TABLE 'users_src')) LIFETIME(MIN 0 MAX 60) LAYOUT(HASHED())",
			"", "", "", 1000, 65536, [2]string{"", ""}, nil, [][2]string{{"shop", "users_src"}}),
		tbl("shop", "events_dist", "Distributed", "Distributed('default', 'shop', 'events', rand())",
			"CREATE TABLE shop.events_dist (`ts` DateTime, `user_id` UInt64, `kind` LowCardinality(String), `amount` Float64) ENGINE = Distributed('default', 'shop', 'events', rand())",
			"", "", "", 0, 0, [2]string{"", ""}, nil, nil), // Distributed：本地表只是引擎参数
		tbl("shop", "rmv_daily", "MaterializedView", "MaterializedView",
			"CREATE MATERIALIZED VIEW shop.rmv_daily\nREFRESH EVERY 1 HOUR OFFSET 5 MINUTE RANDOMIZE FOR 1 MINUTE\n(\n    `day` Date,\n    `c` UInt64\n)\nENGINE = MergeTree\nORDER BY day\nAS SELECT\n    toDate(ts) AS day,\n    count() AS c\nFROM shop.events\nGROUP BY day",
			"", "", "", 0, 0, [2]string{"shop", ".inner_id.ab32b20f-6d2f-4554-80fc-3e501e082a40"}, nil, nil), // 可刷新：不是其源的依赖项
		// 一个 22.x 风格的 DDL，凭据写在引擎参数中，注释里还有
		// "</script>"，用来证明嵌入不会被破坏。
		tbl("lake", "raw_s3", "S3", "S3('https://bucket.s3.amazonaws.com/raw/*.parquet', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY', 'Parquet')",
			"CREATE TABLE lake.raw_s3 (`id` UInt64, `payload` String) ENGINE = S3('https://bucket.s3.amazonaws.com/raw/*.parquet', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY', 'Parquet') COMMENT 'loaded nightly </script><b>x</b>'",
			"", "", "", 0, 0, [2]string{"", ""}, nil, nil),
	}
	col := func(db, table, name, typ string, key, def int) map[string]interface{} {
		return map[string]interface{}{"database": db, "table": table, "name": name, "type": typ,
			"is_key": key, "pk": key, "sk": key, "pt": 0, "sm": 0, "has_default": def}
	}
	// 一个同时也是分区键的排序键列，以及一个采样列。
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
		// FAILED 的字典是最应该先看的；实时页面却把它们丢掉了。
		{"database": "shop", "name": "dict_geo", "source": "MySQL: geo.regions", "status": "FAILED",
			"last_exception": "Code: 1000. DB::Exception: mysqlxx::ConnectionFailed: Can't connect to MySQL server on 'geo-db:3306' (password = 'geo\\'pw')"},
		// 在服务器配置（XML）中声明：database 为 ''，且没有 system.tables 行。
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

// jsonNum 模拟 JSONCompact 响应的到达方式：64 位整数是被引号包裹的字符串
// （output_format_json_quote_64bit_integers=1）。
func jsonNum(n int64) interface{} { return strconv.FormatInt(n, 10) }

// 页面必须能在断网情况下从磁盘工作：没有实时查询、没有凭据处理、没有 CDN。
// 它还必须说明来源。
func TestSchemaGraphTemplate_IsOfflineSelfContainedAndAttributed(t *testing.T) {
	for _, banned := range []string{
		"fetch(", "XMLHttpRequest", "chQuery(", "navigator.credentials", "PasswordCredential",
		"connection-form", `id="password"`, "https://cdn.", "cdnjs", "jsdelivr", "unpkg",
		"loadViewsLoad", "query_views_log", // 热力图是后续工作
	} {
		if strings.Contains(schemaGraphTemplate, banned) {
			t.Errorf("schema graph 页面不得包含 %q", banned)
		}
	}
	for _, want := range []string{
		"Adapted from ClickHouse programs/server/schema.html", "Apache License, Version 2.0",
		"const DATA = /*DATA*/null;", "function loadFromBundle()", "chdiag-theme", "addEventListener('storage'",
		"--surface-card", "--click-font-mono", // 共享 token 层存在
		"function backQuoteIfNeed", "function layoutDatabase", "function showSidebar", "function attachDrag",
		`id="back-link"`, "window.self !== window.top",
	} {
		if !strings.Contains(schemaGraphTemplate, want) {
			t.Errorf("schema graph 页面丢失了 %q", want)
		}
	}
	// --link 是 dashboard 的超链接 token；边颜色不得覆盖它。
	if strings.Contains(schemaGraphTail, "--link:") {
		t.Error("schema graph 重新定义了 --link——SVG 描边请使用 --edge")
	}
	// 每个客户字符串都经过 textContent；innerHTML 只会被赋值为静态标记或被清空。
	for _, line := range strings.Split(schemaGraphTail, "\n") {
		if strings.Contains(line, ".innerHTML") && !strings.Contains(line, "innerHTML = ''") &&
			!strings.Contains(line, `innerHTML = '<option value="">所有数据库</option>'`) {
			t.Errorf("innerHTML 包含动态内容：%s", strings.TrimSpace(line))
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
		t.Fatalf("嵌入的 DATA 不是有效 JSON：%v", err)
	}
	if n := len(out["tables"].([]interface{})); n != 11 {
		t.Errorf("嵌入的表数：%d，期望 11", n)
	}
	for _, want := range []string{`"mv_events_by_kind"`, `"dict_users"`, `"events_dist"`, `"rmv_daily"`, `"Scheduled"`, `.inner_id.7fdd1fdf`} {
		if !strings.Contains(html, want) {
			t.Errorf("页面缺少 %q", want)
		}
	}
	// S3 密钥在嵌入前已被脱敏；key id 也是；还有字典
	// last_exception 中带引号的密码。
	for _, gone := range []string{"wJalrXUtnFEMI", "AKIAIOSFODNN7EXAMPLE", "geo\\'pw", "geo'pw"} {
		if strings.Contains(html, gone) {
			t.Errorf("凭据 %q 到达了页面", gone)
		}
	}
	if !strings.Contains(html, "[HIDDEN]") {
		t.Error("嵌入的 DDL 缺少脱敏标记")
	}
	// DDL 注释中的 "</script>" 必须由 JSON 编码器进行 HTML 转义：
	// 页面最终只会包含模板自带的那些闭合标签（<head> 中的主题标记和主脚本），
	// 不会多一个。
	if got, want := strings.Count(html, "</script>"), strings.Count(schemaGraphTemplate, "</script>"); got != want {
		t.Errorf("客户字符串终止了内联脚本：%d 个 </script> 标签，期望 %d 个", got, want)
	}
	if !strings.Contains(html, `\u003c/script\u003e`) {
		t.Error("期望 DDL 中的 </script> 以 \\u003c/script\\u003e 形式到达")
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
		t.Errorf("替换次数 = %d，期望 2", n)
	}
	if strings.Contains(rows[0]["create_table_query"].(string), "'pw'") || strings.Contains(rows[0]["engine_full"].(string), "'pw'") {
		t.Errorf("密码残留：%v", rows[0])
	}
	if rows[0]["name"] != "pw" {
		t.Error("列表之外的字段被改动了")
	}
	if rows[1]["engine_full"] != "MergeTree ORDER BY x" {
		t.Error("干净的行被改写了")
	}
}

func TestSchemaGraphSummary(t *testing.T) {
	got := schemaGraphSummary(schemaGraphFixture())
	want := map[string]interface{}{"file": "schema_graph.html", "tables": 11, "columns": 26, "databases": 2, "mvs": 3, "refreshable": 1, "dictionaries": 3, "indices": 2}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v，期望 %v", k, got[k], v)
		}
	}
}

// dashboard.html 这一侧：标签页存在，在 payload 表明页面已写入前保持隐藏，
// 并且 iframe 只在点击处理函数内获得 src。
func TestDashboardTemplate_SchemaTabLoadsOnDemand(t *testing.T) {
	for _, want := range []string{
		`id="nav-schema" style="display:none"`, `<section id="sec-schema" style="display:none">`,
		`<iframe id="schema-frame"`, `id="schema-load"`, `id="schema-open"`,
		"const sg=DATA.schema_graph;", "frame.src=sg.file;",
		"document.getElementById('nav-schema').addEventListener('click', load);",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("dashboard 丢失了 %q", want)
		}
	}
	if strings.Contains(htmlTemplate, `<iframe id="schema-frame" src=`) {
		t.Error("schema iframe 不得携带静态 src——那会为每个读者加载该图")
	}
	// 没有 payload 键时什么都不显示：iframe 保持未赋值。
	html := buildHTML(map[string]interface{}{"alerts": []interface{}{}})
	if strings.Contains(html, `"schema_graph"`) {
		t.Error("在没有生成页面时出现了 schema_graph 键")
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
		t.Logf("预览已写入：%s", dst)
	}
}

// 针对 payload 查询和 dashboard 自身面板的评审发现。
func TestSchemaGraphQueries_ReviewFindings(t *testing.T) {
	// FAILED / FAILED_AND_RELOADING 的字典不得被过滤掉。
	dictSQL := schemaDictionariesSQL("system.dictionaries")
	if strings.Contains(dictSQL, "WHERE status") || strings.Contains(dictSQL, "status IN ('LOADED'") {
		t.Error("schema dictionaries 查询按 status 过滤——失败的字典才是需要检查的节点")
	}
	for _, want := range []string{"status", "last_exception"} {
		if !strings.Contains(dictSQL, want) {
			t.Errorf("schema dictionaries 查询丢失了 %q", want)
		}
	}
	// 评审发现：此前所有模式都从本地表读取字典，因此通过某个 cloud pod
	// 构建的图会把在另一个 pod 上 FAILED 的字典显示为健康。该查询现在经过
	// 感知模式的引用，把各 replica 折叠为每个字典一行，将 FAILED 排在最前，
	// 并像其他 schema 查询一样排除系统数据库。
	cloudDict := schemaDictionariesSQL(NewGenerator(nil, "cloud").sysTable("dictionaries"))
	for _, want := range []string{
		"FROM clusterAllReplicas(default, system.dictionaries)",
		"GROUP BY database, name",
		"argMax(status, _rank)",
		"argMax(leftUTF8(last_exception, 300), _rank)",
		"startsWith(toString(status), 'FAILED'), 3",
		schemaSysFilter,
	} {
		if !strings.Contains(cloudDict, want) {
			t.Errorf("cloud dictionaries 查询丢失了 %q", want)
		}
	}
	if !strings.Contains(dictSQL, "FROM system.dictionaries") {
		t.Error("onprem dictionaries 查询必须读取本地表")
	}
	// 页面的字典节点按名称读取这些列。
	for _, col := range []string{"AS source", "AS status", "AS lifetime_min", "AS lifetime_max", "AS last_exception"} {
		if !strings.Contains(dictSQL, col) {
			t.Errorf("dictionaries 查询丢失了输出列 %q", col)
		}
	}
	// view_refreshes 经过感知模式的引用，并折叠各 replica。
	cloud := schemaRefreshesSQL(NewGenerator(nil, "cloud").sysTable("view_refreshes"))
	if !strings.Contains(cloud, "clusterAllReplicas(default, system.view_refreshes)") {
		t.Errorf("cloud refreshes 查询只读取了本地表：\n%s", cloud)
	}
	for _, want := range []string{"GROUP BY database, view", "RunningOnAnotherReplica", "argMax(exception"} {
		if !strings.Contains(cloud, want) {
			t.Errorf("refreshes 查询丢失了 %q", want)
		}
	}
	if onprem := schemaRefreshesSQL(NewGenerator(nil, "onprem").sysTable("view_refreshes")); !strings.Contains(onprem, "FROM system.view_refreshes") {
		t.Errorf("onprem refreshes 查询：%s", onprem)
	}

	// dashboard 的 Dictionaries 面板也会嵌入 source / last_exception。
	rows := []map[string]interface{}{
		{"name": "d", "source": "MySQL('h:3306', 'db', 'tbl', 'u', 'pw')", "last_exception": "cannot connect: password = 'pw2'"},
	}
	if n := redactDictionaryPanel(rows); n != 2 {
		t.Errorf("面板脱敏次数 = %d，期望 2", n)
	}
	if strings.Contains(rows[0]["source"].(string), "'pw'") || strings.Contains(rows[0]["last_exception"].(string), "pw2") {
		t.Errorf("面板行仍然携带凭据：%v", rows[0])
	}
}

// 评审发现：节点是可点击的 <div>，但没有键盘访问路径。
func TestSchemaGraphTemplate_KeyboardAccessible(t *testing.T) {
	for _, want := range []string{
		"el.tabIndex = 0;", "el.setAttribute('role', 'button');", "el.setAttribute('aria-label'",
		"e.key === 'Enter' || e.key === ' '", ".node:focus-visible", "a.href = '#';",
		// 配置声明的字典会获得节点；失败的会显示状态和错误
		"'(config)'", "字典状态", "字典错误",
	} {
		if !strings.Contains(schemaGraphTail, want) {
			t.Errorf("schema graph 页面丢失了 %q", want)
		}
	}
}

// 评审发现：当 system.tables 没有用户行时，collector 在读取
// system.dictionaries 之前就返回了，因此一个只有配置声明字典作为用户对象的
// 服务器得不到图，尽管页面知道如何绘制它们。
func TestSchemaGraphHasContent(t *testing.T) {
	d := []map[string]interface{}{{"database": "", "name": "country_codes"}}
	tb := []map[string]interface{}{{"database": "shop", "name": "events"}}
	if !schemaGraphHasContent(nil, d) {
		t.Error("仅有字典也必须生成图")
	}
	if !schemaGraphHasContent(tb, nil) {
		t.Error("仅有表也必须生成图")
	}
	if schemaGraphHasContent(nil, nil) {
		t.Error("没有可绘制内容时必须不生成页面")
	}
	// 摘要在只有字典的 payload 上如实计数。
	got := schemaGraphSummary(map[string]interface{}{"tables": []map[string]interface{}{}, "dictionaries": d})
	if got["tables"] != 0 || got["dictionaries"] != 1 {
		t.Errorf("摘要 = %v", got)
	}
}

// 针对 #34 的评审建议：在节点信息带上显示 RMV 刷新计划和字典 LIFETIME，
// 区分键角色（并显示跳数索引），以及美化并高亮显示的 CREATE 语句。
func TestSchemaGraph_ReviewerSuggestions(t *testing.T) {
	for _, want := range []string{
		// 1. 从 DDL 解析刷新计划（system.view_refreshes 没有 interval）
		"function parseRefresh(ddl)", "RANDOMIZE", "没有 RANDOMIZE FOR — 该调度的每个副本会在同一时刻触发", "'刷新计划'",
		// 2. LIFETIME 来自列，DDL 作为未加载字典的备用来源
		"function dictLifetime(n)", "'字典生存时间'",
		// 3. 服务端美化打印 + 客户端高亮，仅使用 span
		"function highlightSQL(pre, text)", "syn-hidden", "highlightSQL(pre, n.createQuery);",
		// 4. 四个独立的键角色、对应的图例、侧栏中的标签、跳数索引
		"function colRoles(c)", "key-primary", "key-sorting", "key-partition", "key-sampling",
		"class=\"legend-col column-name key-partition\"", "'跳数索引（'", "role-tag",
	} {
		if !strings.Contains(schemaGraphTail, want) {
			t.Errorf("schema graph 页面丢失了 %q", want)
		}
	}
	// 高亮器不得走 innerHTML 捷径。
	hl := schemaGraphTail[strings.Index(schemaGraphTail, "function highlightSQL"):]
	hl = hl[:strings.Index(hl, "function engineKind")]
	if strings.Contains(hl, "innerHTML") {
		t.Error("highlightSQL 赋值 innerHTML——客户 DDL 必须经过 textContent")
	}
	// payload 一侧。
	for _, want := range []string{"is_in_partition_key AS pt", "is_in_sampling_key  AS sm", "formatQueryOrNull(create_table_query)", "fn:formatQueryOrNull"} {
		if !strings.Contains(readSchemaGraphSource(t), want) {
			t.Errorf("collectSchemaGraph 丢失了 %q", want)
		}
	}
	if d := schemaDictionariesSQL("system.dictionaries"); !strings.Contains(d, "any(lifetime_min) AS lifetime_min") || !strings.Contains(d, "any(lifetime_max) AS lifetime_max") {
		t.Error("dictionaries 查询丢失了 LIFETIME 列")
	}
	if !strings.Contains(schemaIndicesSQL, "system.data_skipping_indices") || !strings.Contains(schemaIndicesSQL, "granularity") {
		t.Error("indices 查询丢失了表或 granularity")
	}
	// 由 fixture 构建的页面携带刷新计划文本、索引和生存时间。
	fx := schemaGraphFixture()
	html := buildSchemaGraphHTML(fx)
	for _, want := range []string{"REFRESH EVERY 1 HOUR OFFSET 5 MINUTE RANDOMIZE FOR 1 MINUTE", `"idx_kind"`, `"bloom_filter"`, `"lifetime_max":"60"`, `"pt":1`, `"sm":1`} {
		if !strings.Contains(html, want) {
			t.Errorf("页面缺少 %q", want)
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

// 针对 #34 的评审后续：可刷新 MV、普通视图和 Distributed 表在服务器上的
// 依赖数组为空，因此它们自由漂浮。页面现在从 DDL 推断它们的源边，
// 并以虚线绘制。
func TestSchemaGraph_InferredEdgesFromDDL(t *testing.T) {
	for _, want := range []string{
		"function parseSourceTables(ddl, defaultDb)", "function parseDistributedSource(engineFull, defaultDb)",
		"addEdge(src, node.key, 'mv', true);", "addEdge(src, node.key, 'distributed', true);",
		"inferred: !!isInferred", ".arrow.inferred{stroke-dasharray:6 4}", "由 DDL 推断</span>",
		"'· 由 DDL 推断'",
	} {
		if !strings.Contains(schemaGraphTail, want) {
			t.Errorf("schema graph 页面丢失了 %q", want)
		}
	}
	// 元数据边必须先于推断边添加，这样真实的依赖永远不会被降级为虚线：
	// 第二遍在第一个循环之后进行。
	first := strings.Index(schemaGraphTail, "for (const node of nodes.values()) {\n        /// MV/RMV：从 dependsOn SELECT，写入 dependents")
	second := strings.Index(schemaGraphTail, "/// 第二遍，在所有元数据边都加入之后")
	if first < 0 || second < 0 || second < first {
		t.Error("推断边必须在元数据遍历之后添加")
	}
}
