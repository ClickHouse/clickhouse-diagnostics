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

// schemaGraphFile is the page's file name inside the run folder, next to
// dashboard.html. Referenced from the dashboard's Schema tab and excluded from
// the Collected Files index like dashboard.html itself.
const schemaGraphFile = "schema_graph.html"

// schemaSysFilter excludes server-internal databases: they carry hundreds of
// tables that describe the server, not the customer's pipeline, and they are
// the bulk of the payload on a small deployment.
const schemaSysFilter = "database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema')"

// collectSchemaGraph runs the five queries the live schema page runs and
// returns the payload for schema_graph.html, or nil when system.tables could
// not be read at all (nothing to draw). Column names match the live page's
// queries so the ported JS reads them unchanged.
func (g *Generator) collectSchemaGraph(version string) map[string]interface{} {
	// target_database / target_table (26.6) name the table an MV writes to —
	// including the implicit .inner_id.<uuid> target of an MV declared with an
	// ENGINE, which dependencies_* does not carry. Older servers get empty
	// strings and the graph falls back to the dependency arrays.
	targetCols := "'' AS target_database, '' AS target_table"
	if g.hasColumn("tables", "target_table") {
		targetCols = "target_database, target_table"
	}
	tables := g.safeQuery("schema_tables", fmt.Sprintf(`
		SELECT database, name, engine, engine_full, create_table_query,
		       sorting_key, primary_key, partition_key, sampling_key,
		       total_rows, total_bytes, comment,
		       %s,
		       dependencies_database, dependencies_table,
		       loading_dependencies_database, loading_dependencies_table
		FROM system.tables
		WHERE %s
		ORDER BY database, name`, targetCols, schemaSysFilter))
	if len(tables) == 0 {
		return nil
	}
	columns := g.safeQuery("schema_columns", fmt.Sprintf(`
		SELECT database, table, name, type,
		       (is_in_primary_key OR is_in_sorting_key) AS is_key,
		       default_kind != '' AS has_default
		FROM system.columns
		WHERE %s
		ORDER BY database, table, position`, schemaSysFilter))
	// Definitions are shared across replicas, so system.dictionaries is read
	// directly in every mode: clusterAllReplicas would return one row per
	// replica and duplicate every dictionary node.
	dicts := g.safeQuery("schema_dictionaries", `
		SELECT database, name, source
		FROM system.dictionaries
		WHERE status IN ('LOADED', 'NOT_LOADED', 'LOADING')`)
	refreshes := []map[string]interface{}{}
	if g.hasTable("view_refreshes") {
		refreshes = g.safeQuery("schema_refreshes", `
			SELECT database, view, status,
			       toString(last_success_time) AS last_success_time,
			       toString(next_refresh_time) AS next_refresh_time,
			       exception
			FROM system.view_refreshes`)
	}

	// Credentials in DDL. Servers >= 23.x mask engine secrets as '[HIDDEN]'
	// themselves; older ones ship them verbatim, and no server masks an AWS
	// key id or a password in a SETTINGS clause. The same redactor the JSONL
	// collectors run through is applied here, so the sidebar's CREATE
	// statement can never show more than the bundle does.
	n := redactSchemaRecords(tables, "create_table_query", "engine_full")
	n += redactSchemaRecords(dicts, "source")
	if n > 0 {
		fmt.Printf("  [dashboard] schema graph: %d credential(s) replaced with [HIDDEN]\n", n)
	}

	return map[string]interface{}{
		"generated_at": time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		"version":      version,
		"mode":         g.mode,
		"tables":       tables,
		"columns":      columns,
		"dictionaries": dicts,
		"refreshes":    refreshes,
		"has_target":   strings.HasPrefix(targetCols, "target_"),
		"has_refresh":  g.hasTable("view_refreshes"),
	}
}

// redactSchemaRecords runs RedactSQLText over the named string fields of every
// record in place and returns the number of replacements.
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

// schemaGraphSummary is what dashboard.html embeds about the graph page: the
// file to load and enough counts to describe it before anyone opens it.
func schemaGraphSummary(sg map[string]interface{}) map[string]interface{} {
	tables, _ := sg["tables"].([]map[string]interface{})
	columns, _ := sg["columns"].([]map[string]interface{})
	dicts, _ := sg["dictionaries"].([]map[string]interface{})
	refreshes, _ := sg["refreshes"].([]map[string]interface{})
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
	}
}

// buildSchemaGraphHTML serialises the payload into the graph page. json.Marshal
// HTML-escapes <, > and & by default, so a create_table_query containing
// "</script>" (a comment, a string literal) cannot terminate the inline script.
func buildSchemaGraphHTML(data map[string]interface{}) string {
	b, _ := json.Marshal(data)
	return strings.Replace(schemaGraphTemplate, "/*DATA*/null", string(b), 1)
}

// schemaGraphTemplate is the complete page: its own head, the token layer it
// shares with dashboard.html, and the graph's CSS/markup/script.
var schemaGraphTemplate = schemaGraphHead + themeTokensCSS + schemaGraphTail

const schemaGraphHead = `<!DOCTYPE html>
<!--
  Table dependency graph for a clickhouse-diagnostic bundle.

  Adapted from ClickHouse programs/server/schema.html
  (https://github.com/ClickHouse/ClickHouse/blob/master/programs/server/schema.html)
  Copyright ClickHouse, Inc. Licensed under the Apache License, Version 2.0.
  Modified to render from JSON embedded at collection time: no server, no
  network, no credentials. See internal/dashboard/schema_graph.go.
-->
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Schema Graph — ClickHouse Diagnostic</title>
<script>
// Stamp the theme before first paint. The key is the one dashboard.html
// writes, and file:// documents in one folder share localStorage, so the graph
// opens in whatever theme the dashboard is showing.
try{var _t=localStorage.getItem("chdiag-theme");if(_t)document.documentElement.setAttribute("data-cui-theme",_t);}catch(e){}
</script>
<style>
`

const schemaGraphTail = `
/* ── schema.html's own palette, expressed in the shared tokens ─────────────
   The live page defines colours directly; here every base colour is a token
   so light/dark follow the dashboard, and only the engine palette — the
   meaning-bearing node colours — keeps its own values (light and dark). The
   edge colour is --edge rather than the dashboard's hyperlink token, --link, which
   the sidebar's cross-references reuse as-is. */
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
.column-name.key{color:var(--column-key);font-weight:600}
.column-name.default{color:var(--column-default)}
.column-type{color:var(--column-type);font-size:0.72rem;text-align:right;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:50%}
.node-stats{padding:0.2rem 0.55rem;background:var(--table-header-bg);border-top:1px solid var(--border-color);border-radius:0 0 5px 5px;font-size:0.72rem;color:var(--muted);display:flex;justify-content:space-between;font-family:var(--click-font-mono)}

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

#empty{position:absolute;top:50%;left:50%;transform:translate(-50%,-50%);color:var(--muted);font-size:1rem;text-align:center;font-style:italic;max-width:36rem}
#empty.hidden{display:none}
#empty .hint{font-size:0.85rem;margin-top:0.5rem;opacity:.75}

.arrow{fill:none;stroke:var(--edge);stroke-width:1.4px;opacity:.85}
.arrow.mv{stroke:var(--edge-mv)}
.arrow.dict{stroke:var(--edge-dict)}
.arrow.distributed{stroke:var(--edge-distributed)}
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
  <h1><span class="brand">ClickHouse</span>Schema Graph <span class="meta" id="hdr-meta"></span></h1>
  <div class="controls">
    <a id="back-link" href="dashboard.html">← Dashboard</a>
    <button id="theme-toggle" class="theme-toggle" type="button" aria-label="Toggle colour theme"></button>
  </div>
</div>

<div class="container">
  <div class="section">
    <div class="filter-row">
      <input id="search" type="text" placeholder="🔍 Search table or column…" spellcheck="false">
      <select id="db-filter"><option value="">All databases</option></select>
      <button id="toggle-columns" class="btn-toggle active" type="button">Columns</button>
      <button id="relayout" class="btn btn-secondary" type="button">Re-layout</button>
      <span id="status"></span>
    </div>
    <div class="legend">
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-mt)"></span>MergeTree / Table</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-mv)"></span>Materialized View</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-rmv)"></span>Refreshable MV</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-dict)"></span>Dictionary</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-distributed)"></span>Distributed</span>
      <span class="legend-item"><span class="legend-swatch" style="background:var(--header-view)"></span>View</span>
      <span class="legend-item"><span class="legend-line" style="background:var(--edge-mv)"></span>MV flow</span>
      <span class="legend-item"><span class="legend-line" style="background:var(--edge-dict)"></span>Dictionary source</span>
      <span class="legend-item"><span class="legend-line" style="background:var(--edge-distributed)"></span>Distributed shard</span>
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
        The bundle has no user tables to draw (the system and INFORMATION_SCHEMA databases are excluded).
        <div class="hint">Click a node to see its details, drag a node to move it.</div>
      </div>
    </div>
  </div>
</div>

<aside id="sidebar">
  <h2><span id="sidebar-title"></span><button class="close" id="sidebar-close" type="button">×</button></h2>
  <div id="sidebar-content"></div>
</aside>

<div id="zoom-controls">
  <button id="zoom-out" title="Zoom out" type="button">−</button>
  <button id="zoom-reset" title="Reset zoom" type="button">⌂</button>
  <button id="zoom-in" title="Zoom in" type="button">+</button>
</div>

<script>
'use strict';

const DATA = /*DATA*/null;

const $ = id => document.getElementById(id);

// ── theme ─────────────────────────────────────────────────────────────────────
// Shared with dashboard.html through the same localStorage key. When this
// page is inside the dashboard's iframe the two documents are separate
// (opaque) origins under file:// and cannot script each other, but they DO
// share localStorage, so the dashboard's toggle reaches us as a 'storage'
// event and ours reaches it the same way.
function currentTheme() {
    const stamped = document.documentElement.getAttribute('data-cui-theme');
    if (stamped) return stamped;
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}
function applyTheme(t) {
    document.documentElement.setAttribute('data-cui-theme', t);
    $('theme-toggle').textContent = t === 'dark' ? '☀ Light' : '☾ Dark';
}
function setTheme(t) {
    applyTheme(t);
    try { localStorage.setItem('chdiag-theme', t); } catch (e) {}
}
applyTheme(currentTheme());
$('theme-toggle').onclick = () => setTheme(currentTheme() === 'dark' ? 'light' : 'dark');
window.addEventListener('storage', e => { if (e.key === 'chdiag-theme' && e.newValue) applyTheme(e.newValue); });

// Inside the dashboard's frame the back link would navigate the frame onto a
// second copy of the dashboard; hide it there.
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

/// Unambiguous composite key for a (database, table) pair. We cannot use
/// db + '.' + t because both halves are arbitrary ClickHouse identifiers (any
/// byte is legal in a backquoted name), so the resulting string is not
/// invertible — a database called a.b and a table called c would collide with
/// database a and table b.c. JSON.stringify on a two-element array gives a
/// one-to-one encoding regardless of the characters inside.
function tableKey(db, t) { return JSON.stringify([db, t]); }

/// Mirrors backQuoteIfNeed in ClickHouse (src/IO/WriteHelpers.h): bare identifier
/// when it matches [A-Za-z_][A-Za-z0-9_]* and is not a reserved word, otherwise
/// backquoted with the same character set the server escapes.
const QUOTED_NAME_RESERVED = /^(distinct|all|table|select|from|values)$/i;
const QUOTED_NAME_ESCAPE = { '\\': '\\\\', '` + "`" + `': '\\` + "`" + `', '\b': '\\b', '\f': '\\f',
                             '\n': '\\n', '\r': '\\r', '\t': '\\t', '\0': '\\0' };
function backQuoteIfNeed(s) {
    if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(s) && !QUOTED_NAME_RESERVED.test(s)) return s;
    return '` + "`" + `' + s.replace(/[\\` + "`" + `\b\f\n\r\t\0]/g, ch => QUOTED_NAME_ESCAPE[ch]) + '` + "`" + `';
}
function quotedFullName(db, t) { return backQuoteIfNeed(db) + '.' + backQuoteIfNeed(t); }

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

/// Short engine label for node headers — full engine names like
/// "MaterializedView" hide the actual table name in the header.
function engineLabel(n) {
    if (n.kind === 'rmv') return 'RMV';
    if (n.kind === 'mv') return 'MV';
    return n.engine || '';
}

// ── load from the embedded payload ────────────────────────────────────────────
// This is where the live page ran five queries against the server. The same
// five result sets are embedded at collection time; the column names match
// the live queries, so everything downstream reads them unchanged.
function loadFromBundle() {
    const d = DATA || {};
    const tables = d.tables || [];
    const columns = d.columns || [];
    const dicts = d.dictionaries || [];
    const refreshes = d.refreshes || [];

    state.tables = tables;
    state.columnsByTable = new Map();
    for (const c of columns) {
        const k = tableKey(c.database, c.table);
        if (!state.columnsByTable.has(k)) state.columnsByTable.set(k, []);
        state.columnsByTable.get(k).push(c);
    }
    state.dictSources = new Map();
    for (const x of dicts) state.dictSources.set(tableKey(x.database, x.name), x.source);
    state.refreshes = new Map();
    for (const r of refreshes) state.refreshes.set(tableKey(r.database, r.view), r);

    const meta = [];
    if (d.version) meta.push('ClickHouse ' + d.version);
    if (d.generated_at) meta.push('collected ' + d.generated_at);
    $('hdr-meta').textContent = meta.length ? '· ' + meta.join(' · ') : '';

    setStatus(tables.length + ' tables, ' + columns.length + ' columns from the bundle'
        + (d.has_target === false ? ' · MV targets inferred (server < 26.6)' : ''));
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
            dictSource: state.dictSources.get(key),
            refresh: state.refreshes.get(key),
            x: 0, y: 0, w: 0, h: 0,
            depth: 0,
        };
        nodes.set(key, node);
        if (!nodesByDb.has(t.database)) nodesByDb.set(t.database, []);
        nodesByDb.get(t.database).push(node);
    }

    /// Edges: for each table, draw arrows source -> target.
    /// Use loading_dependencies (depends_on) and dependents (already gives forward direction).
    const edges = [];
    const seen = new Set();
    function addEdge(from, to, kind) {
        if (!nodes.has(from) || !nodes.has(to)) return;
        if (from === to) return;
        const k = from + '\x00' + to;
        if (seen.has(k)) return;
        seen.add(k);
        edges.push({ from, to, kind });
    }

    for (const node of nodes.values()) {
        /// MV/RMV: SELECT FROM dependsOn, write to dependents
        const isMv = node.kind === 'mv' || node.kind === 'rmv';
        for (const dep of node.dependsOn) {
            if (isMv) addEdge(dep, node.key, 'mv');
            else if (node.kind === 'dict') addEdge(dep, node.key, 'dict');
            else if (node.kind === 'distributed') addEdge(dep, node.key, 'distributed');
            else addEdge(dep, node.key, 'normal');
        }
        for (const dep of node.dependents) {
            /// "Tables that depend on me"; for MV the target table is here.
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
        /// MV -> destination table. Exposed by system.tables.target_table (26.6+),
        /// covering both the explicit TO target and the implicit .inner_id.* table
        /// created by MATERIALIZED VIEW ... ENGINE = ... AS SELECT ...
        if (isMv && node.targetTable) {
            addEdge(node.key, tableKey(node.targetDatabase || node.database, node.targetTable), 'mv');
        }
    }

    state.nodes = nodes;
    state.nodesByDb = nodesByDb;
    state.edges = edges;
}

function updateDbFilter() {
    const sel = $('db-filter');
    const cur = sel.value;
    sel.innerHTML = '<option value="">All databases</option>';
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

/// Compute longest-path layering for a subgraph (one database).
function layoutDatabase(dbNodes, edges) {
    const inDb = new Set(dbNodes.map(n => n.key));
    /// Restrict edges to within database; cross-DB edges are drawn but ignored for layering.
    const adjOut = new Map();
    const adjIn = new Map();
    for (const n of dbNodes) { adjOut.set(n.key, []); adjIn.set(n.key, []); }
    for (const e of edges) {
        if (inDb.has(e.from) && inDb.has(e.to)) {
            adjOut.get(e.from).push(e.to);
            adjIn.get(e.to).push(e.from);
        }
    }

    /// Longest-path layering (topological), tolerate cycles by capping depth.
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

    /// Group by depth.
    const layers = new Map();
    let maxDepth = 0;
    for (const n of dbNodes) {
        const d = depth.get(n.key) || 0;
        if (!layers.has(d)) layers.set(d, []);
        layers.get(d).push(n);
        if (d > maxDepth) maxDepth = d;
    }

    /// Order within layer alphabetically initially, then apply barycenter for crossing reduction.
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

/// Find connected components in nodeSet, treating state.edges as undirected
/// for grouping. Each returned component is the list of node keys.
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

/// Layout strategy:
///   1. Each database gets one section holding all of its tables. Multi-node
///      dependency chains are laid out as layered graphs and stacked top-to-bottom,
///      deepest first — the longest MV graph rises to the top.
///   2. A database's standalone (single-node) tables are packed into a grid directly
///      below its chains, grouped by engine kind and sorted by db.name.
///   3. Database sections are then ordered the same way and wrap to fill the viewport.
function layoutAll() {
    const visibleDbs = Array.from(state.nodesByDb.keys())
        .filter(db => !state.dbFilter || state.dbFilter === db)
        .sort();

    const LAYER_W = 260;
    const NODE_GAP_X = 60;     /// horizontal gap between layers (= length of inter-layer arrow)
    const NODE_GAP_Y = 36;     /// vertical gap between nodes inside a column / grid row
    const CHAIN_GAP_Y = 60;    /// vertical gap between stacked chains in the same DB section
    const DB_PADDING = 16;
    const DB_PADDING_TOP = 32; /// space for the section label
    const DB_GAP = 28;

    /// Pre-size nodes (estimate; the real height is measured after the first render).
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

        /// 1) Layered layout per chain; chains stacked top-to-bottom.
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

        /// 2) Independent tables of this database, packed into a grid below its
        ///    chains, grouped by engine kind and sorted by db.name within a group.
        let singlesBlockH = 0;
        if (singles.length) {
            const kindOrder = ['mt', 'view', 'mv', 'rmv', 'dict', 'distributed', 'other'];
            singles.sort((a, b) => {
                const ra = kindOrder.indexOf(a.kind), rb = kindOrder.indexOf(b.kind);
                const pa = ra < 0 ? 99 : ra, pb = rb < 0 ? 99 : rb;
                if (pa !== pb) return pa - pb;
                return a.key.localeCompare(b.key);
            });

            /// Column count grows ~sqrt with the table count, so a database with many
            /// independent tables forms a compact block instead of one tall column —
            /// capped so a single section never grows wider than the viewport.
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

    /// Order DB sections: longest top chain first, then by size, then alpha.
    dbSections.sort((a, b) =>
        (b.sortDepth - a.sortDepth) ||
        (b.sortSize - a.sortSize) ||
        a.db.localeCompare(b.db));

    /// Pack DB sections in a wrapping grid that fills viewport width.
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
    /// Remove existing nodes/sections (keep svg).
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
        /// Every node lives inside its own database section, so the header shows
        /// just the table name; the fully-qualified name is on hover and in the sidebar.
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
                if (c.is_key === 1 || c.is_key === true) cn.classList.add('key');
                else if (c.has_default === 1 || c.has_default === true) cn.classList.add('default');
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
                more.textContent = '… ' + (n.columns.length - maxShow) + ' more';
                cols.appendChild(more);
            }
            el.appendChild(cols);
        }

        if (n.totalRows && Number(n.totalRows) > 0) {
            const stats = document.createElement('div');
            stats.className = 'node-stats';
            const r = document.createElement('span');
            r.textContent = fmtRows(n.totalRows) + ' rows';
            const b = document.createElement('span');
            b.textContent = fmtBytes(n.totalBytes);
            stats.appendChild(r);
            stats.appendChild(b);
            el.appendChild(stats);
        }

        el.addEventListener('click', e => {
            e.stopPropagation();
            selectNode(n.key);
        });

        attachDrag(el, n);

        canvas.appendChild(el);
        maxX = Math.max(maxX, n.x + n.w);
        maxY = Math.max(maxY, n.y + n.h);
    }

    /// Now after nodes are in DOM, measure actual heights (column lists can vary slightly).
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

    /// Update section bounds based on actual node sizes. Resolve nodes via the
    /// section index stamped on each .db-group so the box is sized from exactly
    /// the nodes the layout assigned to that section.
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

    /// Build adjacency for highlighting.
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
        const kindClass = e.kind === 'normal' ? '' : e.kind;
        path.setAttribute('class', 'arrow ' + kindClass);
        path.setAttribute('data-from', e.from);
        path.setAttribute('data-to', e.to);

        const x1 = a.x + a.w;
        const y1 = a.y + a.h / 2;
        const x2 = b.x;
        const y2 = b.y + b.h / 2;
        const dx = Math.max(40, (x2 - x1) / 2);
        path.setAttribute('d', 'M ' + x1 + ' ' + y1 + ' C ' + (x1 + dx) + ' ' + y1 + ', ' + (x2 - dx) + ' ' + y2 + ', ' + (x2 - 8) + ' ' + y2);
        svg.appendChild(path);

        /// Arrowhead.
        const ah = document.createElementNS('http://www.w3.org/2000/svg', 'polygon');
        ah.setAttribute('class', 'arrowhead ' + kindClass);
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

function showSidebar(key) {
    const n = state.nodes.get(key);
    if (!n) return;
    $('sidebar-title').textContent = n.displayName;
    const c = $('sidebar-content');
    c.innerHTML = '';

    const tbl = document.createElement('table');
    const rows = [
        ['Database', n.database],
        ['Name', n.name],
        ['Engine', n.engineFull || n.engine],
    ];
    if (n.partitionKey) rows.push(['Partition by', n.partitionKey]);
    if (n.sortingKey) rows.push(['Order by', n.sortingKey]);
    if (n.primaryKey && n.primaryKey !== n.sortingKey) rows.push(['Primary key', n.primaryKey]);
    if (n.samplingKey) rows.push(['Sample by', n.samplingKey]);
    if (n.totalRows && Number(n.totalRows) > 0) rows.push(['Rows', fmtRows(n.totalRows)]);
    if (n.totalBytes && Number(n.totalBytes) > 0) rows.push(['Bytes', fmtBytes(n.totalBytes)]);
    if (n.comment) rows.push(['Comment', n.comment]);
    if (n.refresh) {
        rows.push(['Refresh status', n.refresh.status]);
        if (n.refresh.last_success_time) rows.push(['Last refresh', n.refresh.last_success_time]);
        if (n.refresh.next_refresh_time) rows.push(['Next refresh', n.refresh.next_refresh_time]);
        if (n.refresh.exception) rows.push(['Refresh error', n.refresh.exception]);
    }
    if (n.dictSource) rows.push(['Dictionary source', n.dictSource]);
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
        h.textContent = 'Columns (' + n.columns.length + ')';
        c.appendChild(h);
        const ct = document.createElement('table');
        for (const col of n.columns) {
            const tr = document.createElement('tr');
            const td1 = document.createElement('td');
            td1.textContent = col.name;
            if (col.is_key === 1 || col.is_key === true) td1.style.color = 'var(--column-key)';
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
        h.textContent = 'Reads from (' + ins.length + ')';
        c.appendChild(h);
        const wrap = document.createElement('div');
        wrap.className = 'related-table';
        for (const k of ins) {
            const a = document.createElement('a');
            a.textContent = (state.nodes.get(k) && state.nodes.get(k).displayName) || k;
            a.onclick = () => selectNode(k);
            wrap.appendChild(a);
        }
        c.appendChild(wrap);
    }
    if (outs.length) {
        const h = document.createElement('h3');
        h.textContent = 'Writes to / depended on by (' + outs.length + ')';
        c.appendChild(h);
        const wrap = document.createElement('div');
        wrap.className = 'related-table';
        for (const k of outs) {
            const a = document.createElement('a');
            a.textContent = (state.nodes.get(k) && state.nodes.get(k).displayName) || k;
            a.onclick = () => selectNode(k);
            wrap.appendChild(a);
        }
        c.appendChild(wrap);
    }

    if (n.createQuery) {
        const h = document.createElement('h3');
        h.textContent = 'CREATE statement';
        c.appendChild(h);
        const pre = document.createElement('pre');
        /// Credentials in engine arguments read '[HIDDEN]': masked by the server on
        /// >= 23.x, and by the collector's redactor for anything older or missed.
        pre.textContent = n.createQuery;
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
                /// Suppress the click after a drag.
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
/// The layout depends on the viewport width; redo it when the frame is resized.
let resizeTimer;
window.addEventListener('resize', () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(render, 150); });

state.showColumns = $('toggle-columns').classList.contains('active');

loadFromBundle();
</script>
</body>
</html>
`
