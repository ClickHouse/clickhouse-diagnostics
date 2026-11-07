package dashboard

// schema_graph.html — the table-dependency graph.
//
// Adapted from ClickHouse's built-in schema page, programs/server/schema.html
// (https://github.com/ClickHouse/ClickHouse/blob/master/programs/server/schema.html),
// Copyright ClickHouse, Inc., licensed under the Apache License, Version 2.0.
// The layout algorithm, node/edge rendering, sidebar and interaction code are
// theirs; this file removes everything that talks to a live server and reads
// the same five result sets from JSON embedded at generation time instead.
//
// Why a second file rather than a section of dashboard.html: the graph needs
// every column of every user table embedded. A near-empty server measured at
// ~750 KiB of DDL and column data for 189 tables; a service with 5,000 tables
// extrapolates to ~20 MB, which would make dashboard.html slow to open for a
// reader who never wanted the graph. So dashboard.html gains a Schema tab
// that assigns this file as an <iframe> source only when clicked — a
// navigation, which file:// permits, where fetch()/XHR of a sibling file is
// blocked. Nobody pays for the graph until they ask for it.
//
// What the page does NOT do, deliberately:
//   - contact any server or CDN (the dashboard's Chart.js is not needed here);
//   - script across the iframe boundary (opaque origins under file://); the
//     theme is shared through localStorage, which file:// documents in one
//     directory do share;
//   - render the INSERT-pipeline heat map from system.query_views_log — that
//     is a follow-up once that collector has landed.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"clickhouse-diagnostic/internal/collection"
)

// schemaGraphFile 是页面在运行目录中、dashboard.html 旁边的文件名。
// 它由 dashboard 的 Schema 标签页引用，并像 dashboard.html 本身一样
// 从 Collected Files 索引中排除。
const schemaGraphFile = "schema_graph.html"

// schemaSysFilter 排除服务器内部数据库：它们带有数百张描述服务器本身的表，
// 而不是客户的 pipeline，并且在小规模部署上占据 payload 的大部分。
const schemaSysFilter = "database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')"

// collectSchemaGraph 运行在线 schema 页面运行的那五个查询，并返回
// schema_graph.html 的 payload；当 system.tables 完全无法读取时返回 nil
// （没有可绘制的内容）。列名与在线页面的查询一致，
// 因此移植后的 JS 可以原样读取它们。
func (g *Generator) collectSchemaGraph(version string) map[string]interface{} {
	// target_database / target_table（26.6）指明 MV 写入的目标表——
	// 包括用 ENGINE 声明的 MV 隐式的 .inner_id.<uuid> 目标，
	// dependencies_* 不会携带它。较旧的服务器得到空字符串，
	// 图会回退到依赖数组。
	targetCols := "'' AS target_database, '' AS target_table"
	if g.hasColumn("tables", "target_table") {
		targetCols = "target_database, target_table"
	}
	// 服务器会尽可能对 CREATE 语句做 pretty-print：
	// formatQueryOrNull（24.x；formatQuery 本身是 23.11）会重新序列化
	// DDL，每个子句一行，并且对无法解析的语句返回 NULL 而不是失败——
	// 在一台 26.7 测试服务器上 201 条 system DDL 中有 1 条如此，
	// 这就是不使用普通 formatQuery 的原因：这样的一行会让整个查询失败。
	// 较旧的服务器输出单行原始语句。
	ddlCol := "create_table_query"
	ddlFormatted := g.probe("fn:formatQueryOrNull",
		"SELECT count() FROM system.functions WHERE name = 'formatQueryOrNull'")
	if ddlFormatted {
		ddlCol = "coalesce(formatQueryOrNull(create_table_query), create_table_query) AS create_table_query"
	}
	tables := g.safeQuery("schema_tables", fmt.Sprintf(`
		SELECT database, name, engine, engine_full, %s,
		       sorting_key, primary_key, partition_key, sampling_key,
		       total_rows, total_bytes, comment,
		       %s,
		       dependencies_database, dependencies_table,
		       loading_dependencies_database, loading_dependencies_table
		FROM system.tables
		WHERE %s
		ORDER BY database, name`, ddlCol, targetCols, schemaSysFilter))
	// 四个键角色是相互独立的标志——一个列可以同时属于
	// 排序键和分区键——因此四个标志都要带出来，而不是一个 OR。
	columns := g.safeQuery("schema_columns", fmt.Sprintf(`
		SELECT database, table, name, type,
		       (is_in_primary_key OR is_in_sorting_key) AS is_key,
		       is_in_primary_key   AS pk,
		       is_in_sorting_key   AS sk,
		       is_in_partition_key AS pt,
		       is_in_sampling_key  AS sm,
		       default_kind != '' AS has_default
		FROM system.columns
		WHERE %s
		ORDER BY database, table, position`, schemaSysFilter))
	indices := []map[string]interface{}{}
	if g.hasTable("data_skipping_indices") {
		indices = g.safeQuery("schema_indices", fmt.Sprintf(schemaIndicesSQL, schemaSysFilter))
	}
	// 定义在所有 replica 之间共享，但 STATUS 不是：一个在这里 LOADED 的
	// 字典在另一个 pod 上可能是 FAILED，而通过单个连接构建的图会把它
	// 显示为健康。因此模式感知的引用在 cloud 中扇出，查询把各 replica
	// 的行折叠为每个字典一行，保留最差状态及其异常。所有状态都会保留——
	// 在线页面会丢弃 FAILED / FAILED_AND_RELOADING，但在
	// 诊断 bundle 中，失败的字典正是第一个要看的节点。
	dicts := g.safeQuery("schema_dictionaries", schemaDictionariesSQL(g.sysTable("dictionaries")))
	// 现在才决定是否有可绘制的内容：如果服务器的唯一用户对象是配置中
	// 声明的字典，system.tables 中就没有它们的行，页面会从这个结果构建节点。
	if !schemaGraphHasContent(tables, dicts) {
		return nil
	}
	refreshes := []map[string]interface{}{}
	if g.hasTable("view_refreshes") {
		refreshes = g.safeQuery("schema_refreshes", schemaRefreshesSQL(g.sysTable("view_refreshes")))
	}

	// DDL 中的凭据。服务器 >= 23.x 会自己把引擎密钥掩码为 '[HIDDEN]'；
	// 较旧的会原样输出，而且没有服务器会掩码 SETTINGS 子句中的 AWS
	// key id 或密码。这里应用与 JSONL collector 相同的脱敏器，
	// 因此侧边栏的 CREATE 语句永远不会比 bundle 显示得更多。
	n := redactSchemaRecords(tables, "create_table_query", "engine_full")
	n += redactSchemaRecords(dicts, "source", "last_exception")
	if n > 0 {
		fmt.Printf("  [dashboard] 模式关系图：%d 处凭据已替换为 [HIDDEN]\n", n)
	}

	return map[string]interface{}{
		"generated_at":  time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		"version":       version,
		"mode":          g.mode,
		"tables":        tables,
		"columns":       columns,
		"dictionaries":  dicts,
		"refreshes":     refreshes,
		"indices":       indices,
		"ddl_formatted": ddlFormatted,
		"has_target":    strings.HasPrefix(targetCols, "target_"),
		"has_refresh":   g.hasTable("view_refreshes"),
	}
}

// schemaDictionariesSQL 通过 ref 读取所有字典，不论状态——
// ref 是模式感知的表引用，在 cloud 中是 clusterAllReplicas——
// 并把各 replica 的行折叠为每个字典一行。折叠保留
// 最差状态及与之对应的异常：任意 replica 上的 FAILED
// 优先于其他位置的 LOADED，因此通过一个 pod 构建的图仍然会显示
// 失败。定义列在每个 replica 上完全相同，所以对它们来说 any()
// 是精确的。在单个服务器上 GROUP BY 是空操作。在服务器配置（XML）
// 中声明的字典 database = ” 且没有 system.tables 行；页面会从这个
// 结果中为它添加节点。系统数据库像其他所有 schema 查询一样被排除。
func schemaDictionariesSQL(ref string) string {
	return fmt.Sprintf(`
		SELECT database, name,
		       any(source) AS source,
		       argMax(status, _rank) AS status,
		       any(lifetime_min) AS lifetime_min,
		       any(lifetime_max) AS lifetime_max,
		       argMax(leftUTF8(last_exception, 300), _rank) AS last_exception
		FROM (
		    SELECT database, name, source, status, lifetime_min, lifetime_max, last_exception,
		           multiIf(startsWith(toString(status), 'FAILED'), 3,
		                   toString(status) IN ('LOADING', 'NOT_LOADED'), 2,
		                   1) AS _rank
		    FROM %s
		    WHERE %s
		)
		GROUP BY database, name
		ORDER BY database, name`, ref, schemaSysFilter)
}

// schemaIndicesSQL 按表列出跳数索引（data-skipping indices）。只包含定义列，
// 在所有模式下直接读取：它们在每个 replica 上完全相同。
// %s 是系统数据库过滤器。
const schemaIndicesSQL = `
		SELECT database, table, name, type, expr, granularity
		FROM system.data_skipping_indices
		WHERE %s
		ORDER BY database, table, name`

// schemaRefreshesSQL 通过模式感知的表引用读取 system.view_refreshes——
// 在 cloud 中是 clusterAllReplicas——并把各 replica 的行折叠为每个视图一行。
// 在复制数据库中，刷新只在一个 replica 上运行：它报告 Running / Scheduled，
// 其他 replica 报告 RunningOnAnotherReplica，因此有信息量的行是
// 不是该状态的那一行。hasTable 也是 cloud 感知的，因此这里只读取本地表
// 会只为单个 replica 作答，却声称代表整个 cluster。
func schemaRefreshesSQL(ref string) string {
	return fmt.Sprintf(`
		SELECT database, view,
		       argMax(status, status != 'RunningOnAnotherReplica') AS status,
		       toString(max(last_success_time))                    AS last_success_time,
		       toString(max(next_refresh_time))                    AS next_refresh_time,
		       argMax(exception, exception != '')                  AS exception
		FROM %s
		GROUP BY database, view`, ref)
}

// schemaGraphHasContent 说明 payload 是否会绘制出至少一个节点。
// 仅表、仅字典，或两者都有；空对意味着页面不会被写入，
// dashboard 也不会显示 Schema 标签页。
func schemaGraphHasContent(tables, dicts []map[string]interface{}) bool {
	return len(tables) > 0 || len(dicts) > 0
}

// redactDictionaryPanel 清理 dashboard 自己的 Dictionaries 面板行——
// 在 23.x 以下的服务器上，source 可能内嵌带密码的连接串，
// last_exception 也可能引用一个。
func redactDictionaryPanel(rows []map[string]interface{}) int {
	return redactSchemaRecords(rows, "source", "last_exception")
}

// redactSchemaRecords 对每条记录中指定的字符串字段原地运行 RedactSQLText，
// 并返回替换次数。
func redactSchemaRecords(rows []map[string]interface{}, fields ...string) int {
	total := 0
	for _, r := range rows {
		for _, f := range fields {
			s, ok := r[f].(string)
			if !ok || s == "" {
				continue
			}
			out, n := collection.RedactSQLText(s)
			if n > 0 {
				r[f] = out
				total += n
			}
		}
	}
	return total
}

// schemaGraphSummary 是 dashboard.html 嵌入的关于图页面的信息：
// 要加载的文件，以及足以在任何人打开它之前描述它的计数。
func schemaGraphSummary(sg map[string]interface{}) map[string]interface{} {
	tables, _ := sg["tables"].([]map[string]interface{})
	columns, _ := sg["columns"].([]map[string]interface{})
	dicts, _ := sg["dictionaries"].([]map[string]interface{})
	refreshes, _ := sg["refreshes"].([]map[string]interface{})
	indices, _ := sg["indices"].([]map[string]interface{})
	dbs := map[string]bool{}
	mvs := 0
	for _, t := range tables {
		if db, ok := t["database"].(string); ok {
			dbs[db] = true
		}
		if e, _ := t["engine"].(string); e == "MaterializedView" {
			mvs++
		}
	}
	return map[string]interface{}{
		"file":         schemaGraphFile,
		"tables":       len(tables),
		"columns":      len(columns),
		"databases":    len(dbs),
		"mvs":          mvs,
		"refreshable":  len(refreshes),
		"dictionaries": len(dicts),
		"indices":      len(indices),
	}
}

// buildSchemaGraphHTML 把 payload 序列化进图页面。json.Marshal
// 默认对 <、> 和 & 做 HTML 转义，因此包含 "</script>" 的
// create_table_query（注释、字符串字面量）无法终止内联脚本。
func buildSchemaGraphHTML(data map[string]interface{}) string {
	b, _ := json.Marshal(data)
	return strings.Replace(schemaGraphTemplate, "/*DATA*/null", string(b), 1)
}

// schemaGraphTemplate 是完整页面：它自己的 head、与 dashboard.html
// 共享的 token 层，以及图的 CSS/标记/脚本。
var schemaGraphTemplate = schemaGraphHead + themeTokensCSS + schemaGraphTail

const schemaGraphHead = `<!DOCTYPE html>
<!--
  clickhouse-diagnostic bundle 的表依赖关系图。

  Adapted from ClickHouse programs/server/schema.html
  (https://github.com/ClickHouse/ClickHouse/blob/master/programs/server/schema.html)
  Copyright ClickHouse, Inc. Licensed under the Apache License, Version 2.0.
  改为从采集时嵌入的 JSON 渲染：无服务器、无网络、无凭据。
  参见 internal/dashboard/schema_graph.go。
-->
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>模式关系图 — ClickHouse 诊断</title>
<script>
// 在首次绘制前写入主题。这个 key 就是 dashboard.html
// 写入的那个，同一文件夹下的 file:// 文档共享 localStorage，因此图会以
// dashboard 正在显示的主题打开。
try{var _t=localStorage.getItem("chdiag-theme");if(_t)document.documentElement.setAttribute("data-cui-theme",_t);}catch(e){}
</script>
<style>
`

const schemaGraphTail = `
/* ── schema.html 自带的调色板，用共享 token 表达 ─────────────────────────────
   在线页面直接定义颜色；这里每个基础色都是一个 token，
   这样浅色/深色跟随 dashboard，只有引擎调色板——那些承载含义的节点颜色——
   保留自己的取值（浅色和深色）。边颜色使用 --edge 而不是 dashboard 的
   超链接 token --link，侧边栏的交叉引用会原样复用它。 */
:root{
  --color:var(--ink);
  --background:var(--surface-page);
  --card-background:var(--surface-card);
  --border-color:var(--stroke);
  --header-background:var(--header-bg);
  --header-text:var(--header-ink);
  --shadow-color:rgba(0,0,0,.10);
  --error-color:var(--status-critical);
  --muted:var(--ink-muted);
  --table-header-bg:var(--surface-sunken);
  --btn-primary-bg:var(--click-global-color-accent-default);
  --btn-primary-color:var(--surface-card);
  --accent:var(--header-logo);
  --header-mt:#B58900;          --header-mt-text:black;
  --header-mv:#6A1B9A;          --header-mv-text:white;
  --header-rmv:#AD1457;         --header-rmv-text:white;
  --header-dict:#0277BD;        --header-dict-text:white;
  --header-distributed:#E65100; --header-distributed-text:white;
  --header-view:#455A64;        --header-view-text:white;
  --header-other:#5D4037;       --header-other-text:white;
  --column-key:#B71C1C;
  --column-default:#2E7D32;
  --column-type:var(--ink-muted);
  --key-partition:#B45309;
  --key-sampling:#0E7490;
  --syn-kw:#6A1B9A; --syn-type:#0277BD; --syn-str:#2E7D32; --syn-num:#B45309; --syn-cmt:var(--ink-muted); --syn-hidden:#B71C1C;
  --edge:#999999;
  --edge-mv:#6A1B9A;
  --edge-dict:#0277BD;
  --edge-distributed:#E65100;
  --edge-highlight:#B58900;
  --db-bg:#FFFEF0;
  --db-border:#E0D060;
}
@media (prefers-color-scheme:dark){
  :root:where(:not([data-cui-theme="light"])){
    --shadow-color:rgba(0,0,0,.35);
    --key-partition:#F59E0B;
    --key-sampling:#22D3EE;
    --syn-kw:#CE93D8; --syn-type:#4FC3F7; --syn-str:#A5D6A7; --syn-num:#FFB74D; --syn-hidden:#EF9A9A;
    --header-mt:#FCFF74;          --header-mt-text:black;
    --header-mv:#AB47BC;          --header-mv-text:white;
    --header-rmv:#EC407A;         --header-rmv-text:white;
    --header-dict:#29B6F6;        --header-dict-text:black;
    --header-distributed:#FF8A65; --header-distributed-text:black;
    --header-view:#90A4AE;        --header-view-text:black;
    --header-other:#A1887F;       --header-other-text:black;
    --column-key:#EF5350;
    --column-default:#66BB6A;
    --edge:#666666;
    --edge-mv:#CE93D8;
    --edge-dict:#4FC3F7;
    --edge-distributed:#FF8A65;
    --edge-highlight:#FCFF74;
    --db-bg:#1d2a18;
    --db-border:#555533;
  }
}
:root[data-cui-theme="dark"]{
  --shadow-color:rgba(0,0,0,.35);
  --key-partition:#F59E0B;
  --key-sampling:#22D3EE;
  --syn-kw:#CE93D8; --syn-type:#4FC3F7; --syn-str:#A5D6A7; --syn-num:#FFB74D; --syn-hidden:#EF9A9A;
  --header-mt:#FCFF74;          --header-mt-text:black;
  --header-mv:#AB47BC;          --header-mv-text:white;
  --header-rmv:#EC407A;         --header-rmv-text:white;
  --header-dict:#29B6F6;        --header-dict-text:black;
  --header-distributed:#FF8A65; --header-distributed-text:black;
  --header-view:#90A4AE;        --header-view-text:black;
  --header-other:#A1887F;       --header-other-text:black;
  --column-key:#EF5350;
  --column-default:#66BB6A;
  --edge:#666666;
  --edge-mv:#CE93D8;
  --edge-dict:#4FC3F7;
  --edge-distributed:#FF8A65;
  --edge-highlight:#FCFF74;
  --db-bg:#1d2a18;
  --db-border:#555533;
}

*{box-sizing:border-box}
html{scrollbar-width:thin;scrollbar-color:var(--border-color) transparent}
::-webkit-scrollbar{width:10px;height:10px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--border-color);border-radius:5px}
::-webkit-scrollbar-thumb:hover{background:var(--muted)}
html,body{height:100%}
body{font-family:var(--click-font-regular);margin:0;padding:0;background:var(--background);color:var(--color);font-size:var(--click-font-size-2)}
.header{background:var(--header-background);color:var(--header-text);padding:var(--click-space-2) var(--click-space-4);display:flex;justify-content:space-between;align-items:center;box-shadow:0 2px 4px var(--shadow-color);position:sticky;top:0;z-index:100;flex-wrap:wrap;gap:var(--click-space-2)}
.header h1{margin:0;font-size:var(--click-font-size-4);font-weight:var(--click-font-weight-3)}
.header .brand{color:var(--header-logo);font-weight:var(--click-font-weight-4);margin-right:var(--click-space-2)}
.header .meta{font-size:var(--click-font-size-1);opacity:.75;margin-left:var(--click-space-3)}
.controls{display:flex;gap:var(--click-space-3);align-items:center;flex-wrap:wrap}
.controls a{color:var(--header-text);font-size:var(--click-font-size-1);opacity:.85;text-decoration:none;border:1px solid rgba(255,255,255,.25);border-radius:var(--click-radii-full);padding:var(--click-space-1) var(--click-space-3)}
.controls a:hover{background:rgba(255,255,255,.12)}
.btn{padding:0.45rem 0.9rem;border:none;border-radius:var(--click-radii-1);cursor:pointer;font-size:var(--click-font-size-1);transition:opacity .2s;font-family:inherit}
.btn:hover{opacity:.8}
.btn-primary{background:var(--btn-primary-bg);color:var(--btn-primary-color)}
.btn-secondary{background:var(--border-color);color:var(--color)}
.btn-toggle{background:var(--card-background);color:var(--color);border:1px solid var(--border-color);border-radius:var(--click-radii-1);padding:0.4rem 0.7rem;font-size:var(--click-font-size-1);font-family:inherit;cursor:pointer;transition:background .15s,color .15s,border-color .15s}
.btn-toggle:hover{opacity:.85}
.btn-toggle.active{background:var(--btn-primary-bg);color:var(--btn-primary-color);border-color:var(--btn-primary-bg)}
.theme-toggle{background:transparent;border:1px solid rgba(255,255,255,.25);color:var(--header-text);font-size:var(--click-font-size-1);cursor:pointer;padding:var(--click-space-1) var(--click-space-3);border-radius:var(--click-radii-full);line-height:1.4;font-family:inherit;white-space:nowrap}
.theme-toggle:hover{background:rgba(255,255,255,.12)}

.container{max-width:100%;margin:0;padding:var(--click-space-2) var(--click-space-2) var(--click-space-3)}
.section{background:var(--card-background);border:1px solid var(--border-color);border-radius:var(--click-radii-2);padding:var(--click-space-2) var(--click-space-3);margin-bottom:var(--click-space-2);box-shadow:var(--click-shadow-5)}
.filter-row{display:flex;gap:var(--click-space-3);align-items:center;flex-wrap:wrap}
.filter-row select,.filter-row input[type="text"]{padding:0.4rem 0.65rem;border:1px solid var(--border-color);border-radius:var(--click-radii-1);background:var(--card-background);color:var(--color);font-size:var(--click-font-size-1);font-family:inherit}
.filter-row select:focus,.filter-row input[type="text"]:focus{outline:none;border-color:var(--muted)}
#search{min-width:16rem}
#db-filter{min-width:12rem}
.legend{display:flex;gap:0.9rem;align-items:center;font-size:var(--click-font-size-1);flex-wrap:wrap;padding-top:var(--click-space-3);border-top:1px solid var(--border-color);margin-top:var(--click-space-3)}
.legend-item{display:inline-flex;align-items:center;gap:0.35rem}
.legend-swatch{display:inline-block;width:0.9rem;height:0.9rem;border-radius:2px;border:1px solid rgba(0,0,0,.2)}
.legend-line{display:inline-block;width:1.6rem;height:2px}
.legend-line.dashed{height:0;background:none !important;border-top:2px dashed var(--muted)}
#status{margin-left:auto;font-family:var(--click-font-mono);font-size:var(--click-font-size-1);opacity:.8;white-space:pre-wrap}
#status.error{color:var(--error-color);opacity:1;font-weight:600}

.graph-container{position:relative;background:var(--card-background);color:var(--color);border:1px solid var(--border-color);border-radius:var(--click-radii-2);padding:0;box-shadow:var(--click-shadow-5);overflow:hidden}
#viewport{position:relative;overflow:auto;background:var(--card-background);height:calc(100vh - 13rem);min-height:24rem;border-radius:var(--click-radii-2)}
#canvas-wrap{position:relative;min-width:100%;min-height:100%}
#canvas{position:relative;transform-origin:0 0}
#links-svg{position:absolute;top:0;left:0;pointer-events:none;overflow:visible}
.db-group{position:absolute;border:2px dashed var(--db-border);background:color-mix(in srgb,var(--db-bg) 60%,transparent);border-radius:8px;pointer-events:none}
.db-label{position:absolute;top:0.4rem;left:0.8rem;color:var(--header-text);background:var(--header-background);padding:0.05rem 0.5rem;font-weight:700;font-size:var(--click-font-size-2);pointer-events:auto;user-select:none;border-radius:3px;box-shadow:1px 1px 0 var(--shadow-color)}

.node{position:absolute;background:var(--card-background);border:1px solid var(--border-color);border-radius:6px;box-shadow:0 1px 3px var(--shadow-color);font-size:0.78rem;min-width:13rem;max-width:19rem;cursor:pointer;user-select:none;-webkit-user-select:none;transition:box-shadow .15s,border-color .15s,transform .15s}
.node:hover{box-shadow:0 4px 12px var(--shadow-color);transform:translateY(-1px);z-index:5}
.node.selected{border-color:var(--btn-primary-bg);box-shadow:0 0 0 3px var(--accent),0 4px 10px var(--shadow-color);z-index:6}
.node.highlighted{border-color:var(--accent);box-shadow:0 0 0 2px var(--accent);z-index:6}
.node.dimmed{opacity:.25}
.node:focus-visible{outline:3px solid var(--accent);outline-offset:2px;z-index:7}
.node-header{padding:0.35rem 0.55rem;border-radius:5px 5px 0 0;font-weight:600;display:flex;justify-content:space-between;align-items:center;gap:0.4rem}
.node-name{white-space:nowrap;overflow:hidden;text-overflow:ellipsis;flex:1 1 auto;min-width:0;font-family:var(--click-font-mono)}
.node-kind{font-size:0.7rem;opacity:.85;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:8rem;flex:0 1 auto;font-family:var(--click-font-mono)}
.node-mt .node-header{background:var(--header-mt);color:var(--header-mt-text)}
.node-mv .node-header{background:var(--header-mv);color:var(--header-mv-text)}
.node-rmv .node-header{background:var(--header-rmv);color:var(--header-rmv-text)}
.node-dict .node-header{background:var(--header-dict);color:var(--header-dict-text)}
.node-distributed .node-header{background:var(--header-distributed);color:var(--header-distributed-text)}
.node-view .node-header{background:var(--header-view);color:var(--header-view-text)}
.node-other .node-header{background:var(--header-other);color:var(--header-other-text)}
.node-columns{padding:0.25rem 0.55rem;max-height:16rem;overflow-y:auto}
.node-columns.collapsed{display:none}
.column{display:flex;justify-content:space-between;gap:0.5rem;font-family:var(--click-font-mono);font-size:0.78rem;line-height:1.45;border-bottom:1px dotted var(--border-color)}
.column:last-child{border-bottom:none}
.column-name{color:var(--color)}
.column-name.key,.column-name.key-sorting{color:var(--column-key)}
.column-name.key-primary{color:var(--column-key);font-weight:700}
.column-name.key-partition{text-decoration:underline dotted var(--key-partition);text-decoration-thickness:2px;text-underline-offset:3px}
.column-name.key-sampling{border-bottom:2px dashed var(--key-sampling)}
.column-name.default{color:var(--column-default)}
.legend-col{font-family:var(--click-font-mono);font-size:0.78rem;padding:0 0.15rem}
.role-tag{display:inline-block;font-size:0.62rem;font-family:var(--click-font-mono);padding:0 0.3rem;border-radius:3px;margin-left:0.3rem;background:var(--table-header-bg);color:var(--muted);border:1px solid var(--border-color);vertical-align:middle}
.role-tag.pk{color:var(--column-key);border-color:var(--column-key)}
.role-tag.pt{color:var(--key-partition);border-color:var(--key-partition)}
.role-tag.sm{color:var(--key-sampling);border-color:var(--key-sampling)}
.column-type{color:var(--column-type);font-size:0.72rem;text-align:right;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:50%}
.node-stats{padding:0.2rem 0.55rem;background:var(--table-header-bg);border-top:1px solid var(--border-color);border-radius:0 0 5px 5px;font-size:0.72rem;color:var(--muted);display:flex;justify-content:space-between;gap:0.5rem;flex-wrap:wrap;font-family:var(--click-font-mono)}
.node-stats .sched{color:var(--header-rmv);font-weight:600}
.node-stats .sched.no-jitter{text-decoration:underline dotted}
.node-stats .life{color:var(--header-dict);font-weight:600}
.node-stats .idx{color:var(--key-partition)}
#sidebar pre .syn-kw{color:var(--syn-kw);font-weight:600}
#sidebar pre .syn-type{color:var(--syn-type)}
#sidebar pre .syn-str{color:var(--syn-str)}
#sidebar pre .syn-num{color:var(--syn-num)}
#sidebar pre .syn-cmt{color:var(--syn-cmt);font-style:italic}
#sidebar pre .syn-hidden{color:var(--syn-hidden);font-weight:700}

#sidebar{position:fixed;right:0;top:0;bottom:0;width:30rem;max-width:100vw;background:var(--card-background);border-left:4px solid var(--header-background);padding:1rem 1.25rem;overflow-y:auto;z-index:200;display:none;box-shadow:-6px 0 18px var(--shadow-color)}
#sidebar.open{display:block}
#sidebar h2{font-size:1.05rem;margin:0 0 0.6rem;display:flex;justify-content:space-between;align-items:center;font-family:var(--click-font-mono);padding-bottom:0.5rem;border-bottom:2px solid var(--header-background);word-break:break-all}
#sidebar h3{font-size:0.8rem;margin:1rem 0 0.4rem;color:var(--muted);text-transform:uppercase;letter-spacing:.06em}
#sidebar pre{background:var(--table-header-bg);border:1px solid var(--border-color);padding:0.6rem 0.75rem;font-size:0.78rem;font-family:var(--click-font-mono);overflow-x:auto;white-space:pre-wrap;word-break:break-word;border-radius:4px}
#sidebar table{width:100%;border-collapse:collapse;font-size:0.8rem;font-family:var(--click-font-mono)}
#sidebar table td{padding:0.2rem 0.4rem;border-bottom:1px dotted var(--border-color);vertical-align:top;word-break:break-word}
#sidebar table td:first-child{color:var(--muted);white-space:nowrap;width:36%}
#sidebar .close{background:transparent;border:none;color:var(--muted);cursor:pointer;font-size:1.4rem;line-height:1}
#sidebar .close:hover{color:var(--color)}
#sidebar a{color:var(--link);cursor:pointer;text-decoration:underline;font-family:var(--click-font-mono)}
#sidebar .related-table{display:flex;flex-direction:column;gap:0.25rem;padding:0.2rem 0}
#sidebar .inferred-note{color:var(--muted);font-size:0.72rem;margin:-0.2rem 0 0 0.4rem}

#empty{position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);color:var(--muted);font-size:1rem;text-align:center;font-style:italic;max-width:36rem}
#empty.hidden{display:none}
#empty .hint{font-size:0.85rem;margin-top:0.5rem;opacity:.75}

.arrow{fill:none;stroke:var(--edge);stroke-width:1.4px;opacity:.85}
.arrow.mv{stroke:var(--edge-mv)}
.arrow.dict{stroke:var(--edge-dict)}
.arrow.distributed{stroke:var(--edge-distributed)}
.arrow.inferred{stroke-dasharray:6 4}
.arrow.highlighted{stroke:var(--edge-highlight);stroke-width:2.4px;opacity:1}
.arrow.dimmed{opacity:.1}
.arrowhead{fill:var(--edge)}
.arrowhead.mv{fill:var(--edge-mv)}
.arrowhead.dict{fill:var(--edge-dict)}
.arrowhead.distributed{fill:var(--edge-distributed)}
.arrowhead.highlighted{fill:var(--edge-highlight)}

#zoom-controls{position:fixed;bottom:1rem;right:1rem;display:flex;gap:0.3rem;background:var(--card-background);border:1px solid var(--border-color);padding:0.3rem;border-radius:6px;z-index:50;box-shadow:0 2px 6px var(--shadow-color)}
#zoom-controls button{background:var(--table-header-bg);color:var(--color);border:1px solid var(--border-color);width:2.1rem;height:2.1rem;cursor:pointer;font-size:1rem;font-weight:bold;border-radius:4px}
#zoom-controls button:hover{opacity:.85}
</style>
</head>
<body>

<div class="header">
  <h1><span class="brand">ClickHouse</span>模式关系图 <span class="meta" id="hdr-meta"></span></h1>
  <div class="controls">
    <a id="back-link" href="dashboard.html">← 仪表盘</a>
    <button id="theme-toggle" class="theme-toggle" type="button" aria-label="切换配色主题"></button>
  </div>
</div>

<div class="container">
  <div class="section">
    <div class="filter-row">
      <input id="search" type="text" placeholder="🔍 搜索表或列…" spellcheck="false">
      <select id="db-filter"><option value="">所有数据库</option></select>
      <button id="toggle-columns" class="btn-toggle active" type="button">列</button>
      <button id="relayout" class="btn btn-secondary" type="button">重新布局</button>
      <span id="status"></span>
    </div>
    <div class="legend">
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-mt)"></span>MergeTree / 表</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-mv)"></span>物化视图</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-rmv)"></span>可刷新 MV</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-dict)"></span>字典</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-distributed)"></span>Distributed</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-view)"></span>视图</span>
      <span class="legend-item"><span class="legend-line" style="background:var(--edge-mv)"></span>MV 流向</span>
      <span class="legend-item"><span class="legend-line" style="background:var(--edge-dict)"></span>字典来源</span>
      <span class="legend-item"><span class="legend-line" style="background:var(--edge-distributed)"></span>Distributed 分片</span>
      <span class="legend-item" title="system.tables 不会为可刷新 MV、普通视图或 Distributed 表列出依赖；这些边来自 DDL 中的 FROM / JOIN 子句和 Distributed 引擎参数"><span class="legend-line dashed"></span>由 DDL 推断</span>
      <span class="legend-item" style="margin-left:.6rem"><span class="legend-col column-name key-primary">col</span>主键</span>
      <span class="legend-item"><span class="legend-col column-name key-sorting">col</span>排序键</span>
      <span class="legend-item"><span class="legend-col column-name key-partition">col</span>分区键</span>
      <span class="legend-item"><span class="legend-col column-name key-sampling">col</span>采样键</span>
      <span class="legend-item"><span class="legend-col column-name default">col</span>有默认值</span>
    </div>
  </div>

  <div class="graph-container">
    <div id="viewport">
      <div id="canvas-wrap">
        <div id="canvas">
          <svg id="links-svg" xmlns="http://www.w3.org/2000/svg"></svg>
        </div>
      </div>
      <div id="empty">
        bundle 中没有可绘制的用户表（已排除 system 和 INFORMATION_SCHEMA 数据库）。
        <div class="hint">点击节点查看详情，拖动节点可移动位置。</div>
      </div>
    </div>
  </div>
</div>

<aside id="sidebar">
  <h2><span id="sidebar-title"></span><button class="close" id="sidebar-close" type="button">×</button></h2>
  <div id="sidebar-content"></div>
</aside>

<div id="zoom-controls">
  <button id="zoom-out" title="缩小" type="button">−</button>
  <button id="zoom-reset" title="重置缩放" type="button">⌂</button>
  <button id="zoom-in" title="放大" type="button">+</button>
</div>

<script>
'use strict';

const DATA = /*DATA*/null;

const $ = id => document.getElementById(id);

// ── 主题 ─────────────────────────────────────────────────────────────────────
// 通过同一个 localStorage key 与 dashboard.html 共享。当此页面位于
// dashboard 的 iframe 内时，在 file:// 下两个文档是分离的（不透明）
// 源，无法互相脚本化，但它们确实共享 localStorage，因此 dashboard 的
// 切换会以 'storage' 事件到达我们这里，我们的切换也以同样方式到达它。
function currentTheme() {
    const stamped = document.documentElement.getAttribute('data-cui-theme');
    if (stamped) return stamped;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}
function applyTheme(t) {
    document.documentElement.setAttribute('data-cui-theme', t);
    $('theme-toggle').textContent = t === 'dark' ? '☀ 浅色' : '☾ 深色';
}
function setTheme(t) {
    applyTheme(t);
    try { localStorage.setItem('chdiag-theme', t); } catch (e) {}
}
applyTheme(currentTheme());
$('theme-toggle').onclick = () => setTheme(currentTheme() === 'dark' ? 'light' : 'dark');
window.addEventListener('storage', e => { if (e.key === 'chdiag-theme' && e.newValue) applyTheme(e.newValue); });

// 在 dashboard 的 frame 内，返回链接会把 frame 导航到 dashboard 的
// 第二份副本上；在那里隐藏它。
if (window.self !== window.top) $('back-link').style.display = 'none';

const state = {
    tables: [],
    columnsByTable: new Map(),
    dictSources: new Map(),
    refreshes: new Map(),
    nodes: new Map(),
    nodesByDb: new Map(),
    edges: [],
    sections: [],
    showColumns: true,
    dbFilter: '',
    selectedKey: null,
    zoom: 1.0,
};

function setStatus(text, isError) {
    $('status').textContent = text;
    $('status').classList.toggle('error', !!isError);
}

/// 为 (database, table) 对生成无歧义的复合 key。我们不能用
/// db + '.' + t，因为两半都是任意的 ClickHouse 标识符（反引号名称中
/// 任何字节都合法），所以得到的字符串不可逆——名为 a.b 的数据库
/// 和名为 c 的表会与数据库 a 和表 b.c 冲突。对双元素数组做
/// JSON.stringify 可得到一一对应的编码，无论内部字符是什么。
function tableKey(db, t) { return JSON.stringify([db, t]); }

/// 镜像 ClickHouse 中的 backQuoteIfNeed（src/IO/WriteHelpers.h）：当标识符
/// 匹配 [A-Za-z_][A-Za-z0-9_]* 且不是保留字时使用裸标识符，否则用
/// 服务器转义的同一字符集加反引号。
const QUOTED_NAME_RESERVED = /^(distinct|all|table|select|from|values)$/i;
const QUOTED_NAME_ESCAPE = { '\\': '\\\\', '` + "`" + `': '\\` + "`" + `', '\b': '\\b', '\f': '\\f',
                             '\n': '\\n', '\r': '\\r', '\t': '\\t', '\0': '\\0' };
function backQuoteIfNeed(s) {
    if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(s) && !QUOTED_NAME_RESERVED.test(s)) return s;
    return '` + "`" + `' + s.replace(/[\\` + "`" + `\b\f\n\r\t\0]/g, ch => QUOTED_NAME_ESCAPE[ch]) + '` + "`" + `';
}
function quotedFullName(db, t) { return backQuoteIfNeed(db) + '.' + backQuoteIfNeed(t); }

/// 可刷新 MV 的刷新计划只存在于它的 DDL 中——
/// system.view_refreshes 没有 interval 列。REFRESH EVERY|AFTER <interval>
/// [OFFSET <interval>] [RANDOMIZE FOR <interval>]，读取到紧随其后的子句为止。
/// 既适用于服务器的单行 DDL，也适用于 formatQuery() 形式，
/// 后者的子句独占一行。
function parseRefresh(ddl) {
    if (!ddl) return null;
    const m = /\bREFRESH\s+(EVERY|AFTER)\s+([\s\S]*?)(?=\s+(?:APPEND\b|TO\b|DEPENDS\b|ENGINE\b|AS\b|SETTINGS\b|COMMENT\b|DEFINER\b|SQL\b)|\s*\(|\s*$)/i.exec(ddl);
    if (!m) return null;
    let rest = m[2].replace(/\s+/g, ' ').trim();
    const out = { kind: m[1].toUpperCase(), interval: rest, offset: '', randomize: '' };
    const r = /\s+RANDOMIZE\s+FOR\s+(.+)$/i.exec(rest);
    if (r) { out.randomize = r[1].trim(); rest = rest.slice(0, r.index); }
    const o = /\s+OFFSET\s+(.+)$/i.exec(rest);
    if (o) { out.offset = o[1].trim(); rest = rest.slice(0, o.index); }
    out.interval = rest.trim();
    return out;
}
function fmtSchedule(sc) {
    if (!sc) return '';
    let s = sc.kind + ' ' + sc.interval;
    if (sc.offset) s += ' OFFSET ' + sc.offset;
    if (sc.randomize) s += ' RANDOMIZE FOR ' + sc.randomize;
    return s;
}

/// 字典 LIFETIME：system.dictionaries.lifetime_min/max 只在字典加载后
/// 才有值（FAILED 的字典读作 0/0），因此 DDL 中的
/// LIFETIME(MIN a MAX b) / LIFETIME(n) 是回退方案。
function dictLifetime(n) {
    const d = n.dict || {};
    const mn = Number(d.lifetime_min) || 0, mx = Number(d.lifetime_max) || 0;
    if (mn || mx) return { min: mn, max: mx };
    const m = /\bLIFETIME\s*\(\s*(?:MIN\s+(\d+)\s+MAX\s+(\d+)|(\d+))\s*\)/i.exec(n.createQuery || '');
    if (!m) return null;
    if (m[3] != null) return { min: 0, max: Number(m[3]) };
    return { min: Number(m[1]), max: Number(m[2]) };
}
function fmtLifetime(lt) {
    if (!lt) return '';
    if (lt.min && lt.min !== lt.max) return 'LIFETIME ' + lt.min + '–' + lt.max + 's';
    return 'LIFETIME ' + lt.max + 's';
}

/// 列的键角色。相互独立的标志：一个列可以同时在排序键
/// 和分区键中。较旧的 payload 只携带 is_key。
function colRoles(c) {
    const on = v => v === 1 || v === true;
    const roles = [];
    if (on(c.pk)) roles.push('pk');
    else if (on(c.sk) || (c.pk == null && on(c.is_key))) roles.push('sk');
    if (on(c.pt)) roles.push('pt');
    if (on(c.sm)) roles.push('sm');
    return roles;
}
const ROLE_LABEL = { pk: 'PRIMARY KEY', sk: 'ORDER BY', pt: 'PARTITION BY', sm: 'SAMPLE BY' };
function applyColRoles(el, c) {
    const roles = colRoles(c);
    for (const r of roles) el.classList.add({ pk: 'key-primary', sk: 'key-sorting', pt: 'key-partition', sm: 'key-sampling' }[r]);
    if (!roles.length && (c.has_default === 1 || c.has_default === true)) el.classList.add('default');
    if (roles.length) el.title = roles.map(r => ROLE_LABEL[r]).join(', ');
    return roles;
}

/// 侧边栏中 CREATE 语句的语法高亮：一个小型分词器，按 class 输出
/// <span>——关键字、类型、字符串、数字、注释，以及 '[HIDDEN]' 凭据 token。
/// 文本只经过 textContent；任何客户可控的内容都不会进入 innerHTML。
const SQL_KEYWORDS = new Set(('CREATE TABLE MATERIALIZED VIEW DICTIONARY DATABASE IF NOT EXISTS OR REPLACE TEMPORARY ON CLUSTER ENGINE ORDER BY PRIMARY KEY PARTITION SAMPLE SETTINGS TTL AS SELECT FROM WHERE GROUP HAVING JOIN INNER LEFT RIGHT FULL OUTER CROSS ANY ALL ASOF USING LIMIT OFFSET UNION DISTINCT WITH TO APPEND REFRESH EVERY AFTER RANDOMIZE FOR DEPENDS INDEX TYPE GRANULARITY PROJECTION CODEC DEFAULT ALIAS EPHEMERAL COMMENT LIFETIME MIN MAX LAYOUT SOURCE RANGE DEFINER SQL SECURITY INVOKER NONE NULL AND IN LIKE ILIKE BETWEEN CASE WHEN THEN ELSE END INTERVAL ASC DESC NULLS FIRST LAST ARRAY GLOBAL PREWHERE FINAL ONLY DELETE WHERE TRUE FALSE TABLES').split(' '));
const SQL_TYPES = /^(U?Int(8|16|32|64|128|256)|Float(32|64)|BFloat16|Decimal(32|64|128|256)?|String|FixedString|Date(32)?|DateTime(64)?|Time(64)?|UUID|IPv[46]|Bool|Boolean|Enum(8|16)?|LowCardinality|Nullable|Array|Map|Tuple|Nested|JSON|Object|Variant|Dynamic|Nothing|Interval\w*|AggregateFunction|SimpleAggregateFunction|Point|Ring|Polygon|MultiPolygon|LineString|MultiLineString|QBit)$/;
const SQL_TOKEN = /(--[^\n]*)|(\/\*[\s\S]*?\*\/)|('(?:\\.|''|[^'\\])*'?)|(` + "`" + `[^` + "`" + `]*` + "`" + `?)|(\b\d+(?:\.\d+)?(?:e[+-]?\d+)?\b)|([A-Za-z_][A-Za-z0-9_]*)|(\s+)|(.)/g;
function highlightSQL(pre, text) {
    pre.textContent = '';
    SQL_TOKEN.lastIndex = 0;
    let m;
    while ((m = SQL_TOKEN.exec(text)) !== null) {
        const tok = m[0];
        let cls = '';
        if (m[1] || m[2]) cls = 'syn-cmt';
        else if (m[3]) cls = tok.indexOf('[HIDDEN]') >= 0 ? 'syn-hidden' : 'syn-str';
        else if (m[5]) cls = 'syn-num';
        else if (m[6]) cls = SQL_KEYWORDS.has(tok.toUpperCase()) ? 'syn-kw' : (SQL_TYPES.test(tok) ? 'syn-type' : '');
        if (cls) {
            const span = document.createElement('span');
            span.className = cls;
            span.textContent = tok;
            pre.appendChild(span);
        } else {
            pre.appendChild(document.createTextNode(tok));
        }
    }
}

/// 视图读取的源表，在服务器的依赖数组未说明时从 DDL 中取得。
/// 对于可刷新 MV（它不在 insert 时触发，因此不会注册为源的依赖项）、
/// 普通 View，或 Distributed 表（其本地表是引擎参数）来说，
/// 依赖数组不会说明。语句中的每个 "FROM x" / "JOIN x"——先移除字符串
/// 字面量，跳过 ARRAY JOIN 和表函数（"name("），未限定名称解析到
/// 视图自己的数据库。由此构建的边画成虚线并标记为推断，
/// 因为文本是证据，不是元数据。
function parseSourceTables(ddl, defaultDb) {
    if (!ddl) return [];
    const at = ddl.search(/\bAS\s+(?:\(\s*)?(?:WITH|SELECT)\b/i);
    if (at < 0) return [];
    const body = ddl.slice(at).replace(/'(?:\\.|''|[^'\\])*'/g, "''");
    const out = new Set();
    const re = /\b(FROM|JOIN)\s+((?:` + "`" + `[^` + "`" + `]+` + "`" + `|[A-Za-z_][A-Za-z0-9_]*)(?:\s*\.\s*(?:` + "`" + `[^` + "`" + `]+` + "`" + `|[A-Za-z_][A-Za-z0-9_]*))?)(?!\s*\()/gi;
    let m;
    while ((m = re.exec(body)) !== null) {
        if (/ARRAY\s*$/i.test(body.slice(Math.max(0, m.index - 8), m.index))) continue;
        const parts = m[2].split(/\s*\.\s*/).map(x => x.replace(/^` + "`" + `|` + "`" + `$/g, ''));
        if (parts.length === 1) {
            if (/^(SELECT|WITH|VALUES|FINAL|SAMPLE|ARRAY|ONLY|CLUSTER)$/i.test(parts[0])) continue;
            out.add(tableKey(defaultDb, parts[0]));
        } else {
            out.add(tableKey(parts[0], parts[1]));
        }
    }
    return [...out];
}

/// Distributed('cluster', 'db', 'table'[, sharding_key[, policy]])——Distributed
/// 表扇出到的本地表。当该表位于此服务器上时，它得到一条虚线边；
/// 在仅远程的 cluster 上没有节点，什么也不绘制。
function parseDistributedSource(engineFull, defaultDb) {
    const m = /\bDistributed\s*\(\s*'[^']*'\s*,\s*'([^']*)'\s*,\s*'([^']*)'/i.exec(engineFull || '');
    if (!m) return null;
    return tableKey(m[1] || defaultDb, m[2]);
}

function engineKind(engine) {
    if (!engine) return 'other';
    if (engine === 'Dictionary') return 'dict';
    if (engine === 'Distributed') return 'distributed';
    if (engine === 'View') return 'view';
    if (engine === 'LiveView' || engine === 'WindowView') return 'view';
    if (engine === 'MaterializedView') return 'mv';
    if (engine.includes('MergeTree')) return 'mt';
    return 'other';
}

/// 节点头部的简短引擎标签——像 "MaterializedView" 这样的完整引擎名
/// 会在头部遮住实际的表名。
function engineLabel(n) {
    if (n.kind === 'rmv') return 'RMV';
    if (n.kind === 'mv') return 'MV';
    return n.engine || '';
}

// ── 从内嵌 payload 加载 ────────────────────────────────────────────────────────
// 在线页面原本在这里针对服务器运行五个查询。同样的
// 五个结果集在采集时嵌入；列名与在线查询一致，
// 因此下游的一切都原样读取它们。
function loadFromBundle() {
    const d = DATA || {};
    const tables = d.tables || [];
    const columns = d.columns || [];
    const dicts = d.dictionaries || [];
    const refreshes = d.refreshes || [];
    const indices = d.indices || [];
    state.indicesByTable = new Map();
    for (const ix of indices) {
        const k = tableKey(ix.database, ix.table);
        if (!state.indicesByTable.has(k)) state.indicesByTable.set(k, []);
        state.indicesByTable.get(k).push(ix);
    }

    state.tables = tables;
    state.columnsByTable = new Map();
    for (const c of columns) {
        const k = tableKey(c.database, c.table);
        if (!state.columnsByTable.has(k)) state.columnsByTable.set(k, []);
        state.columnsByTable.get(k).push(c);
    }
    state.dictSources = new Map();
    const tableKeys = new Set(tables.map(t => tableKey(t.database, t.name)));
    for (const x of dicts) {
        const k = tableKey(x.database, x.name);
        state.dictSources.set(k, x);
        /// 在服务器配置（XML）中声明的字典位于 system.dictionaries，
        /// database = '' 且没有 system.tables 行，因此它不会有节点。
        /// 给它一个节点，标记为配置定义；它没有依赖数组，所以
        /// 不带边绘制——其来源文本仍在侧边栏中。
        if (!tableKeys.has(k)) {
            state.tables.push({
                database: x.database || '(config)', name: x.name,
                engine: 'Dictionary', engine_full: '字典（在服务器配置中声明）',
                create_table_query: '', sorting_key: '', primary_key: '', partition_key: '', sampling_key: '',
                total_rows: null, total_bytes: null, comment: '',
                target_database: '', target_table: '',
                dependencies_database: [], dependencies_table: [],
                loading_dependencies_database: [], loading_dependencies_table: [],
            });
            tableKeys.add(k);
            if (x.database === '') state.dictSources.set(tableKey('(config)', x.name), x);
        }
    }
    state.refreshes = new Map();
    for (const r of refreshes) state.refreshes.set(tableKey(r.database, r.view), r);

    const meta = [];
    if (d.version) meta.push('ClickHouse ' + d.version);
    if (d.generated_at) meta.push('采集于 ' + d.generated_at);
    $('hdr-meta').textContent = meta.length ? '· ' + meta.join(' · ') : '';

    setStatus(tables.length + ' 张表，' + columns.length + ' 列（来自 bundle）'
        + (d.has_target === false ? ' · MV 目标为推断（服务器 < 26.6）' : ''));
    buildGraph();
    updateDbFilter();
    render();
}

function buildGraph() {
    const nodes = new Map();
    const nodesByDb = new Map();
    const zipKeys = (dbs, tbls) => {
        if (!dbs || !tbls) return [];
        const out = [];
        for (let i = 0; i < dbs.length && i < tbls.length; i++)
            out.push(tableKey(dbs[i], tbls[i]));
        return out;
    };
    for (const t of state.tables) {
        const key = tableKey(t.database, t.name);
        const isRefreshable = state.refreshes.has(key);
        const kind = isRefreshable ? 'rmv' : engineKind(t.engine);
        const node = {
            key,
            displayName: quotedFullName(t.database, t.name),
            database: t.database,
            name: t.name,
            engine: t.engine,
            engineFull: t.engine_full,
            kind,
            createQuery: t.create_table_query,
            sortingKey: t.sorting_key,
            primaryKey: t.primary_key,
            partitionKey: t.partition_key,
            samplingKey: t.sampling_key,
            totalRows: t.total_rows,
            totalBytes: t.total_bytes,
            comment: t.comment,
            targetDatabase: t.target_database || '',
            targetTable: t.target_table || '',
            dependents: zipKeys(t.dependencies_database, t.dependencies_table),
            dependsOn: zipKeys(t.loading_dependencies_database, t.loading_dependencies_table),
            columns: state.columnsByTable.get(key) || [],
            dict: state.dictSources.get(key),
            refresh: state.refreshes.get(key),
            indices: (state.indicesByTable && state.indicesByTable.get(key)) || [],
            schedule: parseRefresh(t.create_table_query),
            x: 0, y: 0, w: 0, h: 0,
            depth: 0,
        };
        nodes.set(key, node);
        if (!nodesByDb.has(t.database)) nodesByDb.set(t.database, []);
        nodesByDb.get(t.database).push(node);
    }

    /// 边：为每张表绘制 source -> target 的箭头。
    /// 使用 loading_dependencies（depends_on）和 dependents（已经给出正向方向）。
    const edges = [];
    const seen = new Set();
    const inferred = new Set();
    function addEdge(from, to, kind, isInferred) {
        if (!nodes.has(from) || !nodes.has(to)) return;
        if (from === to) return;
        const k = from + '\x00' + to;
        if (seen.has(k)) return;
        seen.add(k);
        edges.push({ from, to, kind, inferred: !!isInferred });
        if (isInferred) inferred.add(k);
    }

    for (const node of nodes.values()) {
        /// MV/RMV：从 dependsOn SELECT，写入 dependents
        const isMv = node.kind === 'mv' || node.kind === 'rmv';
        for (const dep of node.dependsOn) {
            if (isMv) addEdge(dep, node.key, 'mv');
            else if (node.kind === 'dict') addEdge(dep, node.key, 'dict');
            else if (node.kind === 'distributed') addEdge(dep, node.key, 'distributed');
            else addEdge(dep, node.key, 'normal');
        }
        for (const dep of node.dependents) {
            /// “依赖我的表”；对 MV 来说目标表在这里。
            if (isMv) addEdge(node.key, dep, 'mv');
            else if (nodes.get(dep) && (nodes.get(dep).kind === 'mv' || nodes.get(dep).kind === 'rmv'))
                addEdge(node.key, dep, 'mv');
            else if (nodes.get(dep) && nodes.get(dep).kind === 'dict')
                addEdge(node.key, dep, 'dict');
            else if (nodes.get(dep) && nodes.get(dep).kind === 'distributed')
                addEdge(node.key, dep, 'distributed');
            else
                addEdge(node.key, dep, 'normal');
        }
        /// MV -> 目标表。由 system.tables.target_table（26.6+）暴露，
        /// 覆盖显式 TO 目标以及由
        /// MATERIALIZED VIEW ... ENGINE = ... AS SELECT ... 创建的隐式 .inner_id.* 表
        if (isMv && node.targetTable) {
            addEdge(node.key, tableKey(node.targetDatabase || node.database, node.targetTable), 'mv');
        }
    }

    /// 第二遍，在所有元数据边都加入之后（addEdge 保留一对中的第一条，
    /// 因此元数据优先）：DDL 说视图读取什么，以及 Distributed 表
    /// 背后的本地表。可刷新 MV 和普通 View 在服务器上的依赖数组为空，
    /// 因此没有这一步它们会自由漂浮。
    for (const node of nodes.values()) {
        if (node.kind === 'mv' || node.kind === 'rmv' || node.kind === 'view') {
            for (const src of parseSourceTables(node.createQuery, node.database)) {
                addEdge(src, node.key, 'mv', true);
            }
        } else if (node.kind === 'distributed') {
            const src = parseDistributedSource(node.engineFull, node.database);
            if (src) addEdge(src, node.key, 'distributed', true);
        }
    }

    state.nodes = nodes;
    state.nodesByDb = nodesByDb;
    state.edges = edges;
    state.inferredEdges = inferred;
}

function updateDbFilter() {
    const sel = $('db-filter');
    const cur = sel.value;
    sel.innerHTML = '<option value="">所有数据库</option>';
    const dbs = Array.from(state.nodesByDb.keys()).sort();
    for (const db of dbs) {
        const o = document.createElement('option');
        o.value = db;
        o.textContent = db + ' (' + state.nodesByDb.get(db).length + ')';
        sel.appendChild(o);
    }
    sel.value = dbs.includes(cur) ? cur : '';
    state.dbFilter = sel.value;
}

/// 为子图（一个数据库）计算最长路径分层。
function layoutDatabase(dbNodes, edges) {
    const inDb = new Set(dbNodes.map(n => n.key));
    /// 将边限制在数据库内部；跨库边会被绘制，但在分层时忽略。
    const adjOut = new Map();
    const adjIn = new Map();
    for (const n of dbNodes) { adjOut.set(n.key, []); adjIn.set(n.key, []); }
    for (const e of edges) {
        if (inDb.has(e.from) && inDb.has(e.to)) {
            adjOut.get(e.from).push(e.to);
            adjIn.get(e.to).push(e.from);
        }
    }

    /// 最长路径分层（拓扑），通过限制深度来容忍环。
    const depth = new Map();
    const visiting = new Set();
    function dfs(k, stack) {
        if (depth.has(k)) return depth.get(k);
        if (visiting.has(k)) return 0;
        visiting.add(k);
        let d = 0;
        for (const u of adjIn.get(k) || []) {
            if (stack.has(u)) continue;
            stack.add(u);
            d = Math.max(d, dfs(u, stack) + 1);
            stack.delete(u);
        }
        visiting.delete(k);
        depth.set(k, d);
        return d;
    }
    for (const n of dbNodes) dfs(n.key, new Set([n.key]));

    /// 按深度分组。
    const layers = new Map();
    let maxDepth = 0;
    for (const n of dbNodes) {
        const d = depth.get(n.key) || 0;
        if (!layers.has(d)) layers.set(d, []);
        layers.get(d).push(n);
        if (d > maxDepth) maxDepth = d;
    }

    /// 层内先按字母顺序排序，然后应用重心法减少交叉。
    for (const arr of layers.values()) arr.sort((a, b) => a.name.localeCompare(b.name));

    for (let iter = 0; iter < 8; iter++) {
        for (let d = 1; d <= maxDepth; d++) {
            const layer = layers.get(d) || [];
            const prev = layers.get(d - 1) || [];
            const prevIdx = new Map();
            prev.forEach((n, i) => prevIdx.set(n.key, i));
            for (const n of layer) {
                const ins = (adjIn.get(n.key) || []).filter(k => prevIdx.has(k)).map(k => prevIdx.get(k));
                n._bary = ins.length ? ins.reduce((a, b) => a + b, 0) / ins.length : prevIdx.size / 2;
            }
            layer.sort((a, b) => (a._bary - b._bary) || a.name.localeCompare(b.name));
        }
        for (let d = maxDepth - 1; d >= 0; d--) {
            const layer = layers.get(d) || [];
            const next = layers.get(d + 1) || [];
            const nextIdx = new Map();
            next.forEach((n, i) => nextIdx.set(n.key, i));
            for (const n of layer) {
                const outs = (adjOut.get(n.key) || []).filter(k => nextIdx.has(k)).map(k => nextIdx.get(k));
                n._bary = outs.length ? outs.reduce((a, b) => a + b, 0) / outs.length : nextIdx.size / 2;
            }
            layer.sort((a, b) => (a._bary - b._bary) || a.name.localeCompare(b.name));
        }
    }

    return { layers, maxDepth };
}

function estimateNodeSize(node) {
    const showCols = state.showColumns;
    const colCount = showCols ? Math.min(node.columns.length, 14) : 0;
    const w = 220;
    const h = 36 + colCount * 17 + (node.totalRows != null && node.totalRows !== '0' ? 20 : 0);
    return { w, h };
}

/// 在 nodeSet 中查找连通分量，分组时把 state.edges 当作无向边。
/// 返回的每个分量都是节点 key 列表。
function connectedComponentsIn(nodeSet) {
    const adj = new Map();
    for (const k of nodeSet) adj.set(k, []);
    for (const e of state.edges) {
        if (nodeSet.has(e.from) && nodeSet.has(e.to)) {
            adj.get(e.from).push(e.to);
            adj.get(e.to).push(e.from);
        }
    }
    const seen = new Set();
    const comps = [];
    for (const start of nodeSet) {
        if (seen.has(start)) continue;
        const stack = [start], comp = [];
        seen.add(start);
        while (stack.length) {
            const v = stack.pop();
            comp.push(v);
            for (const u of adj.get(v)) {
                if (!seen.has(u)) { seen.add(u); stack.push(u); }
            }
        }
        comps.push(comp);
    }
    return comps;
}

/// 布局策略：
///   1. 每个数据库得到一个区块，容纳其所有表。多节点依赖链
///      按分层图布局并自上而下堆叠，最深的在前——最长的 MV 图升到顶部。
///   2. 数据库的独立（单节点）表直接紧挨在其链下方打包成网格，
///      按引擎种类分组，并按 db.name 排序。
///   3. 数据库区块随后以同样的方式排序，并换行以填满视口。
function layoutAll() {
    const visibleDbs = Array.from(state.nodesByDb.keys())
        .filter(db => !state.dbFilter || state.dbFilter === db)
        .sort();

    const LAYER_W = 260;
    const NODE_GAP_X = 60;     /// 层之间的水平间距（= 层间箭头长度）
    const NODE_GAP_Y = 36;     /// 列 / 网格行内节点之间的垂直间距
    const CHAIN_GAP_Y = 60;    /// 同一 DB 区块中堆叠链之间的垂直间距
    const DB_PADDING = 16;
    const DB_PADDING_TOP = 32; /// 区块标签的空间
    const DB_GAP = 28;

    /// 预先估算节点尺寸（估算；真实高度在首次渲染后测量）。
    for (const n of state.nodes.values()) {
        const s = estimateNodeSize(n);
        n.w = s.w;
        n.h = s.h;
    }

    const MARGIN = 8;
    const viewportW = Math.max(LAYER_W, $('viewport').clientWidth - MARGIN * 2);

    const dbSections = [];

    for (const db of visibleDbs) {
        const dbNodes = state.nodesByDb.get(db);
        const dbKeys = new Set(dbNodes.map(n => n.key));
        const comps = connectedComponentsIn(dbKeys);

        const chains = [];
        const singles = [];
        for (const comp of comps) {
            if (comp.length === 1) singles.push(state.nodes.get(comp[0]));
            else chains.push(comp);
        }
        if (!chains.length && !singles.length) continue;

        /// 1) 对每条链做分层布局；链自上而下堆叠。
        const chainLayouts = [];
        for (const comp of chains) {
            const compNodes = comp.map(k => state.nodes.get(k));
            const { layers, maxDepth } = layoutDatabase(compNodes, state.edges);
            const layerHeights = new Map();
            for (let d = 0; d <= maxDepth; d++) {
                const layer = layers.get(d) || [];
                let y = 0;
                for (const n of layer) {
                    n._cx = d * (LAYER_W + NODE_GAP_X);
                    n._cy = y;
                    y += n.h + NODE_GAP_Y;
                }
                layerHeights.set(d, y);
            }
            const chainW = Math.max(0, (maxDepth + 1) * LAYER_W + maxDepth * NODE_GAP_X);
            const chainH = Math.max(...Array.from(layerHeights.values()), 0);
            chainLayouts.push({ comp, maxDepth, chainW, chainH });
        }

        chainLayouts.sort((a, b) =>
            (b.maxDepth - a.maxDepth) ||
            (b.comp.length - a.comp.length) ||
            ((a.comp[0] || '').localeCompare(b.comp[0] || '')));

        let curY = 0;
        let sectionW = 0;
        for (const cl of chainLayouts) {
            for (const k of cl.comp) {
                const n = state.nodes.get(k);
                n._rx = n._cx || 0;
                n._ry = curY + (n._cy || 0);
            }
            curY += cl.chainH + CHAIN_GAP_Y;
            sectionW = Math.max(sectionW, cl.chainW);
        }
        const chainsBlockH = chains.length ? Math.max(0, curY - CHAIN_GAP_Y) : 0;

        /// 2) 这个数据库的独立表，打包成网格放在其链下方，
        ///    按引擎种类分组，组内按 db.name 排序。
        let singlesBlockH = 0;
        if (singles.length) {
            const kindOrder = ['mt', 'view', 'mv', 'rmv', 'dict', 'distributed', 'other'];
            singles.sort((a, b) => {
                const ra = kindOrder.indexOf(a.kind), rb = kindOrder.indexOf(b.kind);
                const pa = ra < 0 ? 99 : ra, pb = rb < 0 ? 99 : rb;
                if (pa !== pb) return pa - pb;
                return a.key.localeCompare(b.key);
            });

            /// 列数随表数量大致按 sqrt 增长，因此有许多独立表的数据库
            /// 会形成紧凑的块，而不是一个高列——
            /// 有上限，使单个区块永远不会宽于视口。
            const maxCols = Math.max(1,
                Math.floor((viewportW - DB_PADDING * 2 + NODE_GAP_X) / (LAYER_W + NODE_GAP_X)));
            const cols = Math.max(1, Math.min(maxCols, Math.round(Math.sqrt(singles.length * 1.6))));

            const startY = chains.length ? chainsBlockH + CHAIN_GAP_Y : 0;
            let col = 0, gy = 0, rowMaxH = 0;
            for (const n of singles) {
                n._rx = col * (LAYER_W + NODE_GAP_X);
                n._ry = startY + gy;
                rowMaxH = Math.max(rowMaxH, n.h);
                if (++col >= cols) { gy += rowMaxH + NODE_GAP_Y; rowMaxH = 0; col = 0; }
            }
            singlesBlockH = gy + (col > 0 ? rowMaxH : 0);
            sectionW = Math.max(sectionW, cols * LAYER_W + (cols - 1) * NODE_GAP_X);
        }

        const contentH = chainsBlockH
            + (chains.length && singles.length ? CHAIN_GAP_Y : 0)
            + singlesBlockH;

        dbSections.push({
            kind: 'db',
            db,
            nodes: chains.flat().map(k => state.nodes.get(k)).concat(singles),
            w: sectionW + DB_PADDING * 2,
            h: contentH + DB_PADDING_TOP + DB_PADDING,
            sortDepth: chainLayouts.length ? chainLayouts[0].maxDepth : 0,
            sortSize: dbNodes.length,
        });
    }

    /// 排序 DB 区块：最长的顶部链优先，然后按大小，然后按字母。
    dbSections.sort((a, b) =>
        (b.sortDepth - a.sortDepth) ||
        (b.sortSize - a.sortSize) ||
        a.db.localeCompare(b.db));

    /// 将 DB 区块打包进一个换行网格，填满视口宽度。
    let cursorX = MARGIN, cursorY = MARGIN, rowH = 0;
    const placed = [];
    for (const s of dbSections) {
        if (cursorX > MARGIN && cursorX + s.w > viewportW + MARGIN) {
            cursorX = MARGIN;
            cursorY += rowH + DB_GAP;
            rowH = 0;
        }
        s.x = cursorX;
        s.y = cursorY;
        for (const n of s.nodes) {
            n.x = cursorX + DB_PADDING + (n._rx || 0);
            n.y = cursorY + DB_PADDING_TOP + (n._ry || 0);
        }
        cursorX += s.w + DB_GAP;
        rowH = Math.max(rowH, s.h);
        placed.push(s);
    }

    state.sections = placed;
}

function fmtBytes(n) {
    if (n == null) return '';
    n = Number(n);
    if (!isFinite(n) || n === 0) return '';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let u = 0;
    while (n >= 1024 && u < units.length - 1) { n /= 1024; u++; }
    return n.toFixed(n < 10 ? 2 : (n < 100 ? 1 : 0)) + ' ' + units[u];
}
function fmtRows(n) {
    if (n == null) return '';
    n = Number(n);
    if (!isFinite(n) || n === 0) return '';
    if (n >= 1e12) return (n / 1e12).toFixed(2) + 'T';
    if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(2) + 'K';
    return String(n);
}

function render() {
    const canvas = $('canvas');
    const svg = $('links-svg');
    /// 移除已有的节点/区块（保留 svg）。
    Array.from(canvas.querySelectorAll('.node, .db-group')).forEach(e => e.remove());

    if (!state.tables.length) {
        $('empty').classList.remove('hidden');
        svg.innerHTML = '';
        return;
    }
    $('empty').classList.add('hidden');

    layoutAll();

    const search = $('search').value.trim().toLowerCase();
    const filtered = new Set();
    if (search) {
        for (const n of state.nodes.values()) {
            if (n.displayName.toLowerCase().includes(search)) filtered.add(n.key);
            else if (n.columns.some(c => c.name.toLowerCase().includes(search))) filtered.add(n.key);
            else if (n.indices.some(ix => String(ix.name).toLowerCase().includes(search))) filtered.add(n.key);
        }
    }

    let maxX = 0, maxY = 0;

    for (let i = 0; i < state.sections.length; ++i) {
        const s = state.sections[i];
        const g = document.createElement('div');
        g.className = 'db-group';
        g.dataset.sectionIndex = String(i);
        g.style.left = s.x + 'px';
        g.style.top = s.y + 'px';
        g.style.width = s.w + 'px';
        g.style.height = s.h + 'px';
        const label = document.createElement('div');
        label.className = 'db-label';
        label.textContent = s.db;
        g.appendChild(label);
        canvas.appendChild(g);
    }

    for (const n of state.nodes.values()) {
        if (state.dbFilter && n.database !== state.dbFilter) continue;
        if (!n.w) continue;
        const el = document.createElement('div');
        el.className = 'node node-' + n.kind;
        el.dataset.key = n.key;
        el.style.left = n.x + 'px';
        el.style.top = n.y + 'px';
        el.style.width = n.w + 'px';
        if (state.selectedKey === n.key) el.classList.add('selected');
        if (search && !filtered.has(n.key)) el.classList.add('dimmed');

        const header = document.createElement('div');
        header.className = 'node-header';
        const nameEl = document.createElement('span');
        nameEl.className = 'node-name';
        nameEl.title = n.displayName;
        /// 每个节点都位于自己的数据库区块内，因此头部只显示
        /// 表名；完全限定名在悬停提示和侧边栏中。
        nameEl.textContent = n.name;
        const kindEl = document.createElement('span');
        kindEl.className = 'node-kind';
        kindEl.textContent = engineLabel(n);
        header.appendChild(nameEl);
        header.appendChild(kindEl);
        el.appendChild(header);

        if (state.showColumns && n.columns.length) {
            const cols = document.createElement('div');
            cols.className = 'node-columns';
            const maxShow = 14;
            const slice = n.columns.slice(0, maxShow);
            for (const c of slice) {
                const row = document.createElement('div');
                row.className = 'column';
                const cn = document.createElement('span');
                cn.className = 'column-name';
                applyColRoles(cn, c);
                cn.textContent = c.name;
                const ct = document.createElement('span');
                ct.className = 'column-type';
                ct.title = c.type;
                ct.textContent = c.type;
                row.appendChild(cn);
                row.appendChild(ct);
                cols.appendChild(row);
            }
            if (n.columns.length > maxShow) {
                const more = document.createElement('div');
                more.className = 'column';
                more.style.color = 'var(--muted)';
                more.style.fontStyle = 'italic';
                more.textContent = '… 另有 ' + (n.columns.length - maxShow) + ' 个';
                cols.appendChild(more);
            }
            el.appendChild(cols);
        }

        /// 灰色条带：持有数据的表的行数和大小；可刷新 MV 的刷新
        /// 计划（34 个全部是 EVERY 2 MINUTE 且没有
        /// RANDOMIZE，这是你想从图上直接读出的发现）；字典的 LIFETIME；
        /// 跳数索引数量。
        const ribbon = [];
        if (n.totalRows && Number(n.totalRows) > 0) {
            ribbon.push(['', fmtRows(n.totalRows) + ' 行']);
            if (fmtBytes(n.totalBytes)) ribbon.push(['', fmtBytes(n.totalBytes)]);
        }
        if (n.kind === 'rmv' && n.schedule) {
            const cls = 'sched' + (n.schedule.randomize ? '' : ' no-jitter');
            ribbon.push([cls, fmtSchedule(n.schedule), n.schedule.randomize ? '' : '没有 RANDOMIZE FOR — 该调度的每个副本会在同一时刻触发']);
        }
        if (n.kind === 'dict') {
            const lt = fmtLifetime(dictLifetime(n));
            if (lt) ribbon.push(['life', lt]);
        }
        if (n.indices && n.indices.length) {
            ribbon.push(['idx', n.indices.length + ' 个跳数索引',
                n.indices.map(ix => ix.name + ' ' + ix.type + '(' + ix.expr + ')').join('\n')]);
        }
        if (ribbon.length) {
            const stats = document.createElement('div');
            stats.className = 'node-stats';
            for (const [cls, text, title] of ribbon) {
                const sp = document.createElement('span');
                if (cls) sp.className = cls;
                sp.textContent = text;
                if (title) sp.title = title;
                stats.appendChild(sp);
            }
            el.appendChild(stats);
        }

        el.addEventListener('click', e => {
            e.stopPropagation();
            selectNode(n.key);
        });
        /// 键盘：节点是一个按钮——Tab 可以到达它，Enter 或 Space 打开它。
        el.tabIndex = 0;
        el.setAttribute('role', 'button');
        el.setAttribute('aria-label', n.displayName + ', ' + (engineLabel(n) || n.engine || '表'));
        el.addEventListener('keydown', e => {
            if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                e.stopPropagation();
                selectNode(n.key);
            }
        });

        attachDrag(el, n);

        canvas.appendChild(el);
        maxX = Math.max(maxX, n.x + n.w);
        maxY = Math.max(maxY, n.y + n.h);
    }

    /// 现在节点已进入 DOM，测量实际高度（列列表可能略有变化）。
    for (const el of canvas.querySelectorAll('.node')) {
        const k = el.dataset.key;
        const node = state.nodes.get(k);
        if (node) {
            node.h = el.offsetHeight;
            node.w = el.offsetWidth;
            maxX = Math.max(maxX, node.x + node.w);
            maxY = Math.max(maxY, node.y + node.h);
        }
    }

    /// 根据实际节点尺寸更新区块边界。通过每个 .db-group 上
    /// 标记的区块索引来解析节点，这样框的大小正好来自
    /// 布局分配给该区块的那些节点。
    for (const g of canvas.querySelectorAll('.db-group')) {
        const idx = Number(g.dataset.sectionIndex);
        const section = state.sections[idx];
        if (!section) continue;
        let mx = 0, my = 0;
        const sx = parseFloat(g.style.left), sy = parseFloat(g.style.top);
        for (const n of section.nodes) {
            if (state.dbFilter && n.database !== state.dbFilter) continue;
            mx = Math.max(mx, n.x + n.w - sx);
            my = Math.max(my, n.y + n.h - sy);
        }
        g.style.width = (mx + 24) + 'px';
        g.style.height = (my + 24) + 'px';
    }

    drawEdges();

    canvas.style.width = (maxX + 60) + 'px';
    canvas.style.height = (maxY + 60) + 'px';
    svg.setAttribute('width', maxX + 60);
    svg.setAttribute('height', maxY + 60);
}

function drawEdges() {
    const svg = $('links-svg');
    svg.innerHTML = '';

    const visible = new Set();
    for (const n of state.nodes.values()) {
        if (!state.dbFilter || n.database === state.dbFilter) visible.add(n.key);
    }

    const search = $('search').value.trim().toLowerCase();
    const filtered = new Set();
    if (search) {
        for (const n of state.nodes.values()) {
            if (n.displayName.toLowerCase().includes(search) ||
                n.columns.some(c => c.name.toLowerCase().includes(search))) filtered.add(n.key);
        }
    }

    /// 构建邻接关系用于高亮。
    const incoming = new Map(), outgoing = new Map();
    for (const e of state.edges) {
        if (!visible.has(e.from) || !visible.has(e.to)) continue;
        if (!incoming.has(e.to)) incoming.set(e.to, []);
        if (!outgoing.has(e.from)) outgoing.set(e.from, []);
        incoming.get(e.to).push(e.from);
        outgoing.get(e.from).push(e.to);
    }
    state._incoming = incoming;
    state._outgoing = outgoing;

    for (const e of state.edges) {
        if (!visible.has(e.from) || !visible.has(e.to)) continue;
        const a = state.nodes.get(e.from);
        const b = state.nodes.get(e.to);
        const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
        const kindClass = (e.kind === 'normal' ? '' : e.kind) + (e.inferred ? ' inferred' : '');
        path.setAttribute('class', 'arrow ' + kindClass);
        path.setAttribute('data-from', e.from);
        path.setAttribute('data-to', e.to);
        if (e.inferred) {
            const t = document.createElementNS('http://www.w3.org/2000/svg', 'title');
            t.textContent = '由 DDL 推断（FROM / JOIN，或 Distributed 引擎参数）— system.tables 未在此列出依赖';
            path.appendChild(t);
        }

        const x1 = a.x + a.w;
        const y1 = a.y + a.h / 2;
        const x2 = b.x;
        const y2 = b.y + b.h / 2;
        const dx = Math.max(40, (x2 - x1) / 2);
        path.setAttribute('d', 'M ' + x1 + ' ' + y1 + ' C ' + (x1 + dx) + ' ' + y1 + ', ' + (x2 - dx) + ' ' + y2 + ', ' + (x2 - 8) + ' ' + y2);
        svg.appendChild(path);

        /// 箭头。
        const ah = document.createElementNS('http://www.w3.org/2000/svg', 'polygon');
        ah.setAttribute('class', 'arrowhead ' + kindClass.replace(' inferred', ''));
        ah.setAttribute('data-from', e.from);
        ah.setAttribute('data-to', e.to);
        const angle = Math.atan2(y2 - y1, (x2 - 8) - (x1 + dx));
        const hx = x2 - 4, hy = y2;
        const len = 8, sp = 4;
        const p1x = hx - len * Math.cos(angle) + sp * Math.sin(angle);
        const p1y = hy - len * Math.sin(angle) - sp * Math.cos(angle);
        const p2x = hx - len * Math.cos(angle) - sp * Math.sin(angle);
        const p2y = hy - len * Math.sin(angle) + sp * Math.cos(angle);
        ah.setAttribute('points', hx + ',' + hy + ' ' + p1x + ',' + p1y + ' ' + p2x + ',' + p2y);
        svg.appendChild(ah);

        if (search) {
            if (!filtered.has(e.from) && !filtered.has(e.to)) {
                path.classList.add('dimmed');
                ah.classList.add('dimmed');
            }
        }
    }
}

function highlightSelection(key) {
    const ins = (state._incoming && state._incoming.get(key)) || [];
    const outs = (state._outgoing && state._outgoing.get(key)) || [];
    const relevant = new Set([key, ...ins, ...outs]);
    for (const el of document.querySelectorAll('.node')) {
        el.classList.remove('highlighted', 'dimmed', 'selected');
        if (el.dataset.key === key) el.classList.add('selected');
        else if (relevant.has(el.dataset.key)) el.classList.add('highlighted');
        else el.classList.add('dimmed');
    }
    for (const p of document.querySelectorAll('.arrow, .arrowhead')) {
        p.classList.remove('highlighted', 'dimmed');
        const from = p.getAttribute('data-from');
        const to = p.getAttribute('data-to');
        if (from === key || to === key) p.classList.add('highlighted');
        else p.classList.add('dimmed');
    }
}

function clearHighlight() {
    for (const el of document.querySelectorAll('.node'))
        el.classList.remove('highlighted', 'dimmed', 'selected');
    for (const p of document.querySelectorAll('.arrow, .arrowhead'))
        p.classList.remove('highlighted', 'dimmed');
    state.selectedKey = null;
    $('sidebar').classList.remove('open');
}

function selectNode(key) {
    state.selectedKey = key;
    highlightSelection(key);
    showSidebar(key);
}

function inferredNote() {
    const sp = document.createElement('span');
    sp.className = 'inferred-note';
    sp.textContent = '· 由 DDL 推断';
    return sp;
}

function showSidebar(key) {
    const n = state.nodes.get(key);
    if (!n) return;
    $('sidebar-title').textContent = n.displayName;
    const c = $('sidebar-content');
    c.innerHTML = '';

    const tbl = document.createElement('table');
    const rows = [
        ['数据库', n.database],
        ['名称', n.name],
        ['引擎', n.engineFull || n.engine],
    ];
    if (n.partitionKey) rows.push(['分区键', n.partitionKey]);
    if (n.sortingKey) rows.push(['排序键', n.sortingKey]);
    if (n.primaryKey && n.primaryKey !== n.sortingKey) rows.push(['主键', n.primaryKey]);
    if (n.samplingKey) rows.push(['采样键', n.samplingKey]);
    if (n.totalRows && Number(n.totalRows) > 0) rows.push(['行数', fmtRows(n.totalRows)]);
    if (n.totalBytes && Number(n.totalBytes) > 0) rows.push(['字节数', fmtBytes(n.totalBytes)]);
    if (n.comment) rows.push(['注释', n.comment]);
    if (n.schedule) {
        rows.push(['刷新计划', n.schedule.kind + ' ' + n.schedule.interval]);
        if (n.schedule.offset) rows.push(['刷新偏移', n.schedule.offset]);
        rows.push(['刷新抖动', n.schedule.randomize ? 'RANDOMIZE FOR ' + n.schedule.randomize : '无 — 严格按计划触发']);
    }
    if (n.refresh) {
        rows.push(['刷新状态', n.refresh.status]);
        if (n.refresh.last_success_time) rows.push(['上次刷新', n.refresh.last_success_time]);
        if (n.refresh.next_refresh_time) rows.push(['下次刷新', n.refresh.next_refresh_time]);
        if (n.refresh.exception) rows.push(['刷新错误', n.refresh.exception]);
    }
    if (n.dict) {
        const lt = dictLifetime(n);
        if (lt) rows.push(['字典生存时间', lt.min && lt.min !== lt.max ? lt.min + '–' + lt.max + ' s（在范围内随机）' : lt.max + ' s']);
        if (n.dict.status) rows.push(['字典状态', n.dict.status]);
        if (n.dict.source) rows.push(['字典来源', n.dict.source]);
        if (n.dict.last_exception) rows.push(['字典错误', n.dict.last_exception]);
    }
    for (const [k, v] of rows) {
        const tr = document.createElement('tr');
        const td1 = document.createElement('td'); td1.textContent = k;
        const td2 = document.createElement('td'); td2.textContent = v == null ? '' : String(v);
        tr.appendChild(td1); tr.appendChild(td2);
        tbl.appendChild(tr);
    }
    c.appendChild(tbl);

    if (n.columns.length) {
        const h = document.createElement('h3');
        h.textContent = '列（' + n.columns.length + '）';
        c.appendChild(h);
        const ct = document.createElement('table');
        for (const col of n.columns) {
            const tr = document.createElement('tr');
            const td1 = document.createElement('td');
            const nm = document.createElement('span');
            nm.className = 'column-name';
            nm.textContent = col.name;
            const roles = applyColRoles(nm, col);
            td1.appendChild(nm);
            for (const r of roles) {
                const tag = document.createElement('span');
                tag.className = 'role-tag ' + r;
                tag.textContent = ROLE_LABEL[r];
                td1.appendChild(tag);
            }
            const td2 = document.createElement('td');
            td2.textContent = col.type;
            td2.style.color = 'var(--muted)';
            td2.style.whiteSpace = 'normal';
            td2.style.wordBreak = 'break-word';
            tr.appendChild(td1); tr.appendChild(td2);
            ct.appendChild(tr);
        }
        c.appendChild(ct);
    }

    const ins = (state._incoming && state._incoming.get(key)) || [];
    const outs = (state._outgoing && state._outgoing.get(key)) || [];
    if (ins.length) {
        const h = document.createElement('h3');
        h.textContent = '读取自（' + ins.length + '）';
        c.appendChild(h);
        const wrap = document.createElement('div');
        wrap.className = 'related-table';
        for (const k of ins) {
            const a = document.createElement('a');
            a.href = '#';
            a.textContent = (state.nodes.get(k) && state.nodes.get(k).displayName) || k;
            a.onclick = (ev) => { ev.preventDefault(); selectNode(k); };
            wrap.appendChild(a);
            if (state.inferredEdges && state.inferredEdges.has(key + '\x00' + k)) wrap.appendChild(inferredNote());
            if (state.inferredEdges && state.inferredEdges.has(k + '\x00' + key)) wrap.appendChild(inferredNote());
        }
        c.appendChild(wrap);
    }
    if (outs.length) {
        const h = document.createElement('h3');
        h.textContent = '写入 / 被依赖于（' + outs.length + '）';
        c.appendChild(h);
        const wrap = document.createElement('div');
        wrap.className = 'related-table';
        for (const k of outs) {
            const a = document.createElement('a');
            a.href = '#';
            a.textContent = (state.nodes.get(k) && state.nodes.get(k).displayName) || k;
            a.onclick = (ev) => { ev.preventDefault(); selectNode(k); };
            wrap.appendChild(a);
        }
        c.appendChild(wrap);
    }

    if (n.indices && n.indices.length) {
        const h = document.createElement('h3');
        h.textContent = '跳数索引（' + n.indices.length + '）';
        c.appendChild(h);
        const it = document.createElement('table');
        for (const ix of n.indices) {
            const tr = document.createElement('tr');
            const td1 = document.createElement('td'); td1.textContent = ix.name;
            const td2 = document.createElement('td');
            td2.textContent = ix.type + '(' + ix.expr + ')  GRANULARITY ' + ix.granularity;
            td2.style.whiteSpace = 'normal';
            tr.appendChild(td1); tr.appendChild(td2);
            it.appendChild(tr);
        }
        c.appendChild(it);
    }

    if (n.createQuery) {
        const h = document.createElement('h3');
        h.textContent = 'CREATE 语句';
        c.appendChild(h);
        const pre = document.createElement('pre');
        /// 当版本支持时由服务器 pretty-print（formatQueryOrNull，每个子句一行）；
        /// 这里做高亮。引擎参数中的凭据显示为 '[HIDDEN]'：>= 23.x 由服务器掩码，
        /// 更旧或遗漏的则由 collector 的脱敏器处理。
        highlightSQL(pre, n.createQuery);
        c.appendChild(pre);
    }

    $('sidebar').classList.add('open');
}

function attachDrag(el, node) {
    let startX, startY, origX, origY, dragging = false;
    el.addEventListener('mousedown', (e) => {
        if (e.button !== 0) return;
        dragging = false;
        startX = e.clientX; startY = e.clientY;
        origX = node.x; origY = node.y;
        const onMove = (ev) => {
            const dx = (ev.clientX - startX) / state.zoom;
            const dy = (ev.clientY - startY) / state.zoom;
            if (!dragging && Math.abs(dx) + Math.abs(dy) > 4) dragging = true;
            if (!dragging) return;
            node.x = origX + dx;
            node.y = origY + dy;
            el.style.left = node.x + 'px';
            el.style.top = node.y + 'px';
            drawEdges();
        };
        const onUp = () => {
            window.removeEventListener('mousemove', onMove);
            window.removeEventListener('mouseup', onUp);
            if (dragging) {
                /// 拖拽后抑制点击。
                const stopClick = (ev) => { ev.stopPropagation(); el.removeEventListener('click', stopClick, true); };
                el.addEventListener('click', stopClick, true);
            }
        };
        window.addEventListener('mousemove', onMove);
        window.addEventListener('mouseup', onUp);
    });
}

function applyZoom() {
    $('canvas').style.transform = 'scale(' + state.zoom + ')';
}
$('zoom-in').onclick = () => { state.zoom = Math.min(2, state.zoom * 1.2); applyZoom(); };
$('zoom-out').onclick = () => { state.zoom = Math.max(0.2, state.zoom / 1.2); applyZoom(); };
$('zoom-reset').onclick = () => { state.zoom = 1; applyZoom(); };

$('relayout').onclick = render;
$('db-filter').onchange = (e) => { state.dbFilter = e.target.value; render(); };
$('toggle-columns').onclick = (e) => {
    state.showColumns = !state.showColumns;
    e.target.classList.toggle('active', state.showColumns);
    render();
};
let searchTimer;
$('search').addEventListener('input', () => {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(render, 200);
});
$('sidebar-close').onclick = clearHighlight;
$('viewport').addEventListener('click', clearHighlight);
window.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') clearHighlight();
});
/// 布局依赖视口宽度；frame 大小改变时重做。
let resizeTimer;
window.addEventListener('resize', () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(render, 150); });

state.showColumns = $('toggle-columns').classList.contains('active');

loadFromBundle();
</script>
</body>
</html>
`
