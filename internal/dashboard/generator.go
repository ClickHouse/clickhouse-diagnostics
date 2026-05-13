package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clickhouse-diagnostic/internal/alert"
	"clickhouse-diagnostic/pkg"
)

// Generator creates offline HTML diagnostic dashboards from ClickHouse data.
type Generator struct {
	client *pkg.ClickHouseClient
	mode   string
}

// NewGenerator creates a new Generator.
func NewGenerator(client *pkg.ClickHouseClient, mode string) *Generator {
	return &Generator{client: client, mode: strings.ToLower(mode)}
}

// chResult is the JSONCompact response envelope from ClickHouse.
type chResult struct {
	Meta []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"meta"`
	Data [][]json.RawMessage `json:"data"`
	Rows int                 `json:"rows"`
}

// records converts chResult rows to a slice of column→value maps.
func (r *chResult) records() []map[string]interface{} {
	if r == nil || r.Rows == 0 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, r.Rows)
	for _, row := range r.Data {
		rec := make(map[string]interface{}, len(r.Meta))
		for i, col := range r.Meta {
			if i < len(row) {
				var v interface{}
				_ = json.Unmarshal(row[i], &v)
				rec[col.Name] = v
			}
		}
		out = append(out, rec)
	}
	return out
}

// sysTable returns the correct system table reference for the current mode.
func (g *Generator) sysTable(table string) string {
	if g.mode == "cloud" {
		return fmt.Sprintf("clusterAllReplicas(default, system.%s)", table)
	}
	return "system." + table
}

// execJSON runs a SQL statement (no FORMAT clause) and parses the JSONCompact response.
func (g *Generator) execJSON(sql string) (*chResult, error) {
	raw, err := g.client.ExecuteQueryWithFormat(sql)
	if err != nil {
		return nil, err
	}
	var res chResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("json parse: %w (%.200s)", err, raw)
	}
	return &res, nil
}

// safeQuery runs a query, prints a warning on failure, and always returns records.
func (g *Generator) safeQuery(label, sql string) []map[string]interface{} {
	res, err := g.execJSON(sql)
	if err != nil {
		fmt.Printf("  [dashboard] %s: %v\n", label, err)
		return []map[string]interface{}{}
	}
	return res.records()
}

// Generate produces dashboard.html inside outputDir.
// alertResults may be nil or empty if alert evaluation was skipped.
func (g *Generator) Generate(outputDir string, alertResults []alert.Result) error {
	fmt.Println("\nGenerating HTML dashboard...")
	payload := g.collect()
	if alertResults == nil {
		alertResults = []alert.Result{}
	}
	payload["alerts"] = alertResults
	html := buildHTML(payload)
	dst := filepath.Join(outputDir, "dashboard.html")
	if err := os.WriteFile(dst, []byte(html), 0640); err != nil {
		return fmt.Errorf("write dashboard: %w", err)
	}
	fmt.Printf("Dashboard saved: %s\n", dst)
	return nil
}

// tablesListSQL builds the all-tables query (with size) for the current mode.
func (g *Generator) tablesListSQL() string {
	notSystem := "database NOT IN ('system','information_schema','INFORMATION_SCHEMA')"
	if g.mode == "cloud" {
		return fmt.Sprintf(`
			WITH t AS (
				SELECT database, name AS table_name, engine, partition_key, sorting_key, storage_policy
				FROM clusterAllReplicas(default, system.tables)
				WHERE %s
				GROUP BY database, name, engine, partition_key, sorting_key, storage_policy
			),
			p AS (
				SELECT database, table, count() AS parts,
					sum(rows) AS total_rows, sum(bytes_on_disk) AS bytes_on_disk
				FROM clusterAllReplicas(default, system.parts)
				WHERE active = 1
				GROUP BY database, table
			)
			SELECT t.database, t.table_name, t.engine,
				coalesce(p.parts, 0) AS parts,
				formatReadableSize(coalesce(p.total_rows, 0)) AS total_rows,
				formatReadableSize(coalesce(p.bytes_on_disk, 0)) AS size,
				coalesce(p.bytes_on_disk, 0) AS bytes_on_disk,
				t.partition_key, t.sorting_key, t.storage_policy
			FROM t LEFT JOIN p ON t.database = p.database AND t.table_name = p.table
			ORDER BY bytes_on_disk DESC
			LIMIT 2000`, notSystem)
	}
	return fmt.Sprintf(`
		SELECT t.database, t.name AS table_name, t.engine,
			coalesce(p.parts, 0) AS parts,
			formatReadableSize(coalesce(p.total_rows, 0)) AS total_rows,
			formatReadableSize(coalesce(p.bytes_on_disk, 0)) AS size,
			coalesce(p.bytes_on_disk, 0) AS bytes_on_disk,
			t.partition_key, t.sorting_key, t.storage_policy
		FROM system.tables AS t
		LEFT JOIN (
			SELECT database, table, count() AS parts,
				sum(rows) AS total_rows, sum(bytes_on_disk) AS bytes_on_disk
			FROM system.parts WHERE active = 1
			GROUP BY database, table
		) AS p ON t.database = p.database AND t.name = p.table
		WHERE t.%s
		ORDER BY bytes_on_disk DESC
		LIMIT 2000`, notSystem)
}

// dictionariesSQL builds the dictionaries query, omitting last_exception for gov.
func (g *Generator) dictionariesSQL() string {
	exceptionCol := "last_exception"
	if g.mode == "gov" {
		exceptionCol = "'' AS last_exception"
	}
	return fmt.Sprintf(`
		SELECT
			database, name, status, type,
			bytes_allocated,
			formatReadableSize(bytes_allocated) AS bytes_allocated_human,
			element_count, query_count,
			round(hit_rate * 100, 2) AS hit_rate_pct,
			round(found_rate * 100, 2) AS found_rate_pct,
			lifetime_min, lifetime_max,
			toString(last_successful_update_time) AS last_update,
			round(loading_duration, 2) AS loading_duration_s,
			%s
		FROM %s
		ORDER BY bytes_allocated DESC`, exceptionCol, g.sysTable("dictionaries"))
}

// collect gathers all metrics from ClickHouse and returns a JSON-ready map.
func (g *Generator) collect() map[string]interface{} {
	p := map[string]interface{}{
		"generated_at":    time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		"mode":            g.mode,
		"version":         "",
		"uptime":          "",
		"total_databases": 0,
		"total_tables":    0,
		"active_parts":    0,
		"total_size":      "N/A",
	}

	// version
	if r, err := g.execJSON("SELECT version() AS version"); err == nil && r.Rows > 0 {
		var v string
		_ = json.Unmarshal(r.Data[0][0], &v)
		p["version"] = v
	}

	// uptime
	if r, err := g.execJSON("SELECT formatReadableTimeDelta(uptime()) AS uptime"); err == nil && r.Rows > 0 {
		var v string
		_ = json.Unmarshal(r.Data[0][0], &v)
		p["uptime"] = v
	}

	// tables / databases summary
	if r, err := g.execJSON(fmt.Sprintf(
		`SELECT uniq(database) AS dbs, count() AS tbls
		 FROM %s
		 WHERE database NOT IN ('system','information_schema','INFORMATION_SCHEMA')`,
		g.sysTable("tables"),
	)); err == nil && r.Rows > 0 {
		var dbs, tbls float64
		_ = json.Unmarshal(r.Data[0][0], &dbs)
		_ = json.Unmarshal(r.Data[0][1], &tbls)
		p["total_databases"] = int(dbs)
		p["total_tables"] = int(tbls)
	}

	// active parts summary
	if r, err := g.execJSON(fmt.Sprintf(
		`SELECT count() AS parts, formatReadableSize(sum(bytes_on_disk)) AS sz
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')`,
		g.sysTable("parts"),
	)); err == nil && r.Rows > 0 {
		var parts float64
		_ = json.Unmarshal(r.Data[0][0], &parts)
		p["active_parts"] = int64(parts)
		var sz string
		_ = json.Unmarshal(r.Data[0][1], &sz)
		p["total_size"] = sz
	}

	// ── Storage ──────────────────────────────────────────────────────────────

	p["storage_by_db"] = g.safeQuery("storage_by_db", fmt.Sprintf(
		`SELECT database, count() AS parts, sum(rows) AS rows,
			sum(bytes_on_disk) AS bytes_on_disk,
			formatReadableSize(sum(bytes_on_disk)) AS size_human,
			round(if(sum(data_compressed_bytes)>0,
				sum(data_uncompressed_bytes)/sum(data_compressed_bytes), 0), 2) AS compression_ratio
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database ORDER BY bytes_on_disk DESC LIMIT 20`,
		g.sysTable("parts"),
	))

	p["engines_dist"] = g.safeQuery("engines_dist", fmt.Sprintf(
		`SELECT engine, count() AS count
		 FROM %s
		 WHERE database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY engine ORDER BY count DESC LIMIT 15`,
		g.sysTable("tables"),
	))

	// ── Tables explorer ───────────────────────────────────────────────────────

	p["tables_list"] = g.safeQuery("tables_list", g.tablesListSQL())

	// ── Query log ─────────────────────────────────────────────────────────────

	p["query_by_time"] = g.safeQuery("query_by_time", fmt.Sprintf(
		`SELECT toString(toStartOfHour(event_time)) AS time,
				query_kind, count() AS count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY AND type = 'QueryFinish'
		 GROUP BY time, query_kind ORDER BY time`,
		g.sysTable("query_log"),
	))

	p["query_by_kind"] = g.safeQuery("query_by_kind", fmt.Sprintf(
		`SELECT query_kind, count() AS count,
				round(avg(query_duration_ms), 0) AS avg_duration_ms,
				round(avg(memory_usage)/1048576, 2) AS avg_memory_mb
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY AND type = 'QueryFinish'
		 GROUP BY query_kind ORDER BY count DESC`,
		g.sysTable("query_log"),
	))

	// top slow queries (by avg duration, grouped by hash + kind + user)
	p["query_slow"] = g.safeQuery("query_slow", fmt.Sprintf(
		`SELECT toString(normalized_query_hash) AS hash,
				query_kind, user,
				count() AS executions,
				round(avg(query_duration_ms), 0) AS avg_duration_ms,
				max(query_duration_ms) AS max_duration_ms,
				round(avg(read_bytes)/1048576, 2) AS avg_read_mb,
				round(avg(memory_usage)/1048576, 2) AS avg_memory_mb,
				countIf(exception_code != 0) AS errors
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND type = 'QueryFinish' AND query_duration_ms > 0
		 GROUP BY hash, query_kind, user
		 ORDER BY avg_duration_ms DESC LIMIT 20`,
		g.sysTable("query_log"),
	))

	// top queries by bytes read
	p["query_heavy"] = g.safeQuery("query_heavy", fmt.Sprintf(
		`SELECT toString(normalized_query_hash) AS hash,
				query_kind, user,
				count() AS executions,
				round(avg(read_bytes)/1048576, 2) AS avg_read_mb,
				formatReadableSize(sum(read_bytes)) AS total_read,
				round(avg(query_duration_ms), 0) AS avg_duration_ms
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND type = 'QueryFinish'
		 GROUP BY hash, query_kind, user
		 ORDER BY avg_read_mb DESC LIMIT 20`,
		g.sysTable("query_log"),
	))

	// per-user summary
	p["query_by_user"] = g.safeQuery("query_by_user", fmt.Sprintf(
		`SELECT user, count() AS executions,
				round(avg(query_duration_ms), 0) AS avg_duration_ms,
				round(sum(read_bytes)/1073741824, 3) AS total_read_gb,
				round(sum(memory_usage)/1073741824, 3) AS total_memory_gb,
				countIf(exception_code != 0) AS error_count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY AND type = 'QueryFinish'
		 GROUP BY user ORDER BY executions DESC LIMIT 20`,
		g.sysTable("query_log"),
	))

	// exceptions
	p["exceptions"] = g.safeQuery("exceptions", fmt.Sprintf(
		`SELECT toString(exception_code) AS exception_code,
				any(exception) AS msg, count() AS count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY AND exception_code != 0
		 GROUP BY exception_code ORDER BY count DESC LIMIT 15`,
		g.sysTable("query_log"),
	))

	// ── Part log ──────────────────────────────────────────────────────────────

	p["part_log_by_time"] = g.safeQuery("part_log_by_time", fmt.Sprintf(
		`SELECT toString(toStartOfDay(event_time)) AS time,
				event_type, count() AS count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		 GROUP BY time, event_type ORDER BY time`,
		g.sysTable("part_log"),
	))

	p["part_log_by_type"] = g.safeQuery("part_log_by_type", fmt.Sprintf(
		`SELECT event_type, count() AS count,
				formatReadableSize(sum(size_in_bytes)) AS total_size
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		 GROUP BY event_type ORDER BY count DESC`,
		g.sysTable("part_log"),
	))

	// ── Dictionaries ──────────────────────────────────────────────────────────

	p["dictionaries"] = g.safeQuery("dictionaries", g.dictionariesSQL())

	// ── Crash log ─────────────────────────────────────────────────────────────

	p["crash_log"] = g.safeQuery("crash_log", fmt.Sprintf(
		`SELECT toString(event_time) AS event_time,
				signal, toString(thread_id) AS thread_id,
				query_id, version
		 FROM %s ORDER BY event_time DESC LIMIT 100`,
		g.sysTable("crash_log"),
	))

	// ── Pending work ──────────────────────────────────────────────────────────

	p["top_tables"] = g.safeQuery("top_tables", fmt.Sprintf(
		`SELECT database, table, count() AS parts,
			formatReadableSize(sum(rows)) AS total_rows,
			formatReadableSize(sum(bytes_on_disk)) AS compressed_size,
			round(if(sum(data_compressed_bytes)>0,
				sum(data_uncompressed_bytes)/sum(data_compressed_bytes), 0), 2) AS compression_ratio
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database, table
		 ORDER BY sum(bytes_on_disk) DESC LIMIT 20`,
		g.sysTable("parts"),
	))

	p["mutations"] = g.safeQuery("mutations", fmt.Sprintf(
		`SELECT database, table, mutation_id, command,
				toString(create_time) AS create_time, parts_to_do
		 FROM %s
		 WHERE parts_to_do > 0 AND is_killed = 0
		 ORDER BY parts_to_do DESC LIMIT 20`,
		g.sysTable("mutations"),
	))

	p["detached"] = g.safeQuery("detached", fmt.Sprintf(
		`SELECT database, table, count() AS count,
				formatReadableSize(sum(bytes_on_disk)) AS size,
				arrayStringConcat(groupUniqArray(reason), ', ') AS reasons
		 FROM %s
		 GROUP BY database, table
		 ORDER BY sum(bytes_on_disk) DESC LIMIT 20`,
		g.sysTable("detached_parts"),
	))

	// cluster nodes (cloud only)
	if g.mode == "cloud" {
		p["clusters"] = g.safeQuery("clusters",
			`SELECT cluster, host_name,
					toString(shard_num) AS shard, toString(replica_num) AS replica,
					toString(is_active) AS is_active, toString(errors_count) AS errors_count
			 FROM system.clusters
			 ORDER BY cluster, shard_num, replica_num`,
		)
	} else {
		p["clusters"] = []map[string]interface{}{}
	}

	p["replication_queue"] = g.safeQuery("replication_queue", fmt.Sprintf(
		`SELECT table, type, toString(is_currently_executing) AS executing, last_exception
		 FROM %s ORDER BY table LIMIT 50`,
		g.sysTable("replication_queue"),
	))

	// ── Replicas health ───────────────────────────────────────────────────────

	p["replicas"] = g.safeQuery("replicas", fmt.Sprintf(
		`SELECT database, table, is_leader, is_readonly, is_session_expired,
			 future_parts, parts_to_check, queue_size,
			 inserts_in_queue, merges_in_queue,
			 toString(queue_oldest_time) AS queue_oldest_time,
			 absolute_delay, total_replicas, active_replicas
		 FROM %s
		 ORDER BY absolute_delay DESC, is_readonly DESC`,
		g.sysTable("replicas"),
	))

	// ── Disk usage ────────────────────────────────────────────────────────────

	p["disks"] = g.safeQuery("disks", fmt.Sprintf(
		`SELECT name, path, type,
			 free_space, total_space,
			 formatReadableSize(free_space)       AS free_space_human,
			 formatReadableSize(total_space)      AS total_space_human,
			 if(total_space > 0,
			    round(free_space / total_space * 100, 1),
			    0) AS free_pct
		 FROM %s
		 ORDER BY total_space DESC`,
		g.sysTable("disks"),
	))

	// ── Server errors (cumulative error counters) ──────────────────────────────

	p["server_errors"] = g.safeQuery("server_errors",
		`SELECT name, code, value,
			 toString(last_error_time) AS last_error_time,
			 left(last_error_message, 300) AS last_error_message
		 FROM system.errors
		 WHERE value > 0
		 ORDER BY value DESC LIMIT 30`,
	)

	// ── High part-count tables (potential "too many parts") ───────────────────

	p["high_part_count"] = g.safeQuery("high_part_count", fmt.Sprintf(
		`SELECT database, table, partition_id, count() AS parts_count
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database, table, partition_id
		 HAVING parts_count > 100
		 ORDER BY parts_count DESC LIMIT 30`,
		g.sysTable("parts"),
	))

	// ── TTL activity (part_log, last 7 days) ──────────────────────────────────

	p["ttl_activity"] = g.safeQuery("ttl_activity", fmt.Sprintf(
		`SELECT toString(toStartOfDay(event_time)) AS day,
			 event_type, merge_reason,
			 count()                              AS events,
			 formatReadableSize(sum(size_in_bytes)) AS total_size
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND (merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')
		        OR event_type = 'RemovePart')
		 GROUP BY day, event_type, merge_reason
		 ORDER BY day`,
		g.sysTable("part_log"),
	))

	// ── Async insert activity (last 24 h, skip gov) ───────────────────────────

	if g.mode != "gov" {
		asyncTable := "system.asynchronous_insert_log"
		if g.mode == "cloud" {
			asyncTable = "clusterAllReplicas(default, merge(system, '^asynchronous_insert_log'))"
		}
		p["async_inserts"] = g.safeQuery("async_inserts", fmt.Sprintf(
			`SELECT toString(toStartOfHour(event_time)) AS hour,
				 status, count() AS flushes,
				 sum(rows) AS total_rows,
				 round(avg(flush_time_microseconds)/1000, 0) AS avg_flush_ms
			 FROM %s
			 WHERE event_time > now() - INTERVAL 24 HOUR
			 GROUP BY hour, status ORDER BY hour`,
			asyncTable,
		))
	} else {
		p["async_inserts"] = []map[string]interface{}{}
	}

	return p
}

// buildHTML serialises the payload into the HTML template.
func buildHTML(data map[string]interface{}) string {
	b, _ := json.Marshal(data)
	return strings.Replace(htmlTemplate, "/*DATA*/null", string(b), 1)
}

// htmlTemplate is the complete self-contained dashboard page.
// Chart.js is loaded from CDN; all ClickHouse data is embedded inline.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ClickHouse Diagnostic Dashboard</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"></script>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#f0f2f5;color:#1a1a2e;font-size:14px}
header{background:linear-gradient(135deg,#1a1a2e 0%,#16213e 100%);color:#fff;padding:16px 28px;display:flex;align-items:center;gap:14px;box-shadow:0 2px 8px rgba(0,0,0,.3);position:sticky;top:0;z-index:100}
header .logo{font-size:26px;font-weight:900;color:#FC4F05;letter-spacing:-1px}
header .logo span{color:#FFB627}
header h1{font-size:17px;font-weight:600;line-height:1.3}
header .meta{margin-left:auto;text-align:right;font-size:12px;opacity:.8;line-height:1.6}
nav{background:#fff;border-bottom:1px solid #e0e0e0;padding:0 28px;display:flex;gap:0;overflow-x:auto;position:sticky;top:57px;z-index:99;box-shadow:0 1px 3px rgba(0,0,0,.05)}
nav a{padding:12px 16px;color:#555;text-decoration:none;font-size:13px;font-weight:500;white-space:nowrap;border-bottom:3px solid transparent;display:block}
nav a:hover,nav a.active{color:#FC4F05;border-bottom-color:#FC4F05}
.badge{display:inline-block;padding:2px 9px;border-radius:12px;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:.5px;margin-top:2px}
.badge-cloud{background:#1e88e5;color:#fff}
.badge-onprem{background:#43a047;color:#fff}
.badge-gov{background:#8e24aa;color:#fff}
main{max-width:1600px;margin:0 auto;padding:20px 20px 40px}
section{margin-bottom:32px;scroll-margin-top:110px}
section h2{font-size:15px;font-weight:700;color:#1a1a2e;margin-bottom:14px;padding-bottom:8px;border-bottom:2px solid #FC4F05;display:flex;align-items:center;gap:8px}
.stats-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(155px,1fr));gap:12px;margin-bottom:28px}
.stat-card{background:#fff;border-radius:10px;padding:16px 14px;box-shadow:0 1px 4px rgba(0,0,0,.08);border-top:3px solid #FC4F05;text-align:center}
.stat-card .val{font-size:24px;font-weight:800;color:#1a1a2e;line-height:1.1}
.stat-card .lbl{font-size:11px;color:#777;text-transform:uppercase;letter-spacing:.5px;margin-top:4px}
.charts-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(460px,1fr));gap:18px}
.chart-card{background:#fff;border-radius:10px;padding:18px;box-shadow:0 1px 4px rgba(0,0,0,.08)}
.chart-card h3{font-size:12px;font-weight:700;color:#555;margin-bottom:12px;text-transform:uppercase;letter-spacing:.5px}
.chart-wrap{position:relative}
.h200{height:200px}.h260{height:260px}.h300{height:300px}.h360{height:360px}.h420{height:420px}
table.dt{width:100%;border-collapse:collapse;font-size:13px}
table.dt th{background:#f4f5f7;color:#444;font-weight:600;padding:9px 11px;text-align:left;border-bottom:2px solid #e0e0e0;white-space:nowrap;cursor:pointer;user-select:none}
table.dt th:hover{background:#ebebeb}
table.dt td{padding:7px 11px;border-bottom:1px solid #f0f0f0;vertical-align:top;max-width:400px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
table.dt tr:hover td{background:#fafafa}
table.dt .num{text-align:right;font-variant-numeric:tabular-nums}
.tbl-wrap{overflow-x:auto;border-radius:8px;border:1px solid #e0e0e0}
.no-data{color:#aaa;font-style:italic;padding:20px;text-align:center}
.alert-row td{background:#fff8e1 !important}
.error-row td{background:#ffebee !important}
.ok-badge{background:#e8f5e9;color:#2e7d32;padding:2px 8px;border-radius:10px;font-size:11px;font-weight:600}
.err-badge{background:#ffebee;color:#c62828;padding:2px 8px;border-radius:10px;font-size:11px;font-weight:600}
.warn-badge{background:#fff8e1;color:#e65100;padding:2px 8px;border-radius:10px;font-size:11px;font-weight:600}
/* search / filter bar */
.filter-bar{display:flex;flex-wrap:wrap;gap:10px;align-items:center;margin-bottom:12px;background:#fff;padding:12px 14px;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,.07)}
.filter-bar input[type=text]{flex:1;min-width:200px;padding:7px 12px;border:1px solid #d0d0d0;border-radius:6px;font-size:13px;outline:none}
.filter-bar input[type=text]:focus{border-color:#FC4F05}
.filter-bar select{padding:7px 10px;border:1px solid #d0d0d0;border-radius:6px;font-size:13px;background:#fff;outline:none}
.count-badge{font-size:12px;color:#777;white-space:nowrap}
.pagination{display:flex;gap:8px;align-items:center;justify-content:center;padding:12px;font-size:13px;color:#555}
.pagination button{padding:5px 12px;border:1px solid #d0d0d0;border-radius:5px;background:#fff;cursor:pointer;font-size:13px}
.pagination button:hover{background:#f0f2f5;border-color:#FC4F05;color:#FC4F05}
.pagination .cur{font-weight:700;color:#FC4F05}
/* subsection title */
.sub-title{font-size:13px;font-weight:700;color:#444;margin:20px 0 10px;text-transform:uppercase;letter-spacing:.4px}
footer{text-align:center;color:#aaa;font-size:12px;padding:20px;margin-top:8px}
@media(max-width:700px){.charts-grid{grid-template-columns:1fr}}
/* alerts */
.alert-ok{background:#e8f5e9;border:1px solid #a5d6a7;border-left:4px solid #4CAF50;padding:14px 20px;border-radius:8px;color:#2e7d32;font-weight:600;font-size:14px}
.alert-item{background:#fff;border-radius:8px;padding:14px 18px;margin-bottom:10px;border-left:4px solid #ccc;box-shadow:0 1px 3px rgba(0,0,0,.08)}
.alert-item.alert-critical{border-left-color:#f44336}
.alert-item.alert-warning{border-left-color:#FF9800}
.alert-item.alert-info{border-left-color:#2196F3}
.alert-item.alert-error{border-left-color:#9c27b0}
.alert-header{display:flex;align-items:center;gap:10px;margin-bottom:6px;flex-wrap:wrap}
.alert-title{font-weight:700;font-size:14px}
.alert-count{font-size:11px;font-weight:700;padding:2px 8px;border-radius:10px}
.alert-count.badge-critical{background:#ffebee;color:#c62828}
.alert-count.badge-warning{background:#fff3e0;color:#e65100}
.alert-count.badge-info{background:#e3f2fd;color:#1565c0}
.alert-count.badge-error{background:#f3e5f5;color:#6a1b9a}
.alert-desc{color:#666;font-size:12px;margin-bottom:8px;line-height:1.5}
.alert-messages{padding-left:18px;margin:0}
.alert-messages li{font-size:12px;color:#333;margin:3px 0;font-family:'SF Mono',monospace,monospace;word-break:break-word;white-space:pre-wrap}
.alert-err-msg{font-size:12px;color:#9c27b0;margin-top:6px;font-style:italic}
.alert-tags{display:flex;gap:6px;flex-wrap:wrap;margin-top:8px}
.alert-tag{background:#f0f2f5;color:#555;border-radius:10px;padding:1px 8px;font-size:11px}
.alert-summary-bar{display:flex;gap:12px;flex-wrap:wrap;margin-bottom:14px}
.alert-summary-chip{padding:6px 14px;border-radius:20px;font-size:13px;font-weight:600}
.chip-critical{background:#ffebee;color:#c62828}
.chip-warning{background:#fff3e0;color:#e65100}
.chip-info{background:#e3f2fd;color:#1565c0}
</style>
</head>
<body>

<header>
  <div class="logo">Click<span>House</span></div>
  <div>
    <h1>Diagnostic Dashboard</h1>
    <div id="hdr-badge"></div>
  </div>
  <div class="meta" id="hdr-meta"></div>
</header>

<nav id="main-nav">
  <a href="#sec-alerts" id="nav-alerts">Alerts</a>
  <a href="#sec-overview">Overview</a>
  <a href="#sec-storage">Storage</a>
  <a href="#sec-tables">Tables</a>
  <a href="#sec-queries">Query Activity</a>
  <a href="#sec-deepdive">Query Deep Dive</a>
  <a href="#sec-exceptions">Exceptions</a>
  <a href="#sec-partlog">Part Log</a>
  <a href="#sec-dicts">Dictionaries</a>
  <a href="#sec-crashlog" id="nav-crashlog" style="display:none">Crash Log</a>
  <a href="#sec-pending">Pending Work</a>
  <a href="#sec-replication">Replication</a>
  <a href="#sec-replicas" id="nav-replicas" style="display:none">Replicas</a>
  <a href="#sec-clusters" id="nav-clusters" style="display:none">Clusters</a>
  <a href="#sec-disks">Disks</a>
  <a href="#sec-server-errors">Server Errors</a>
  <a href="#sec-async-inserts" id="nav-async-inserts" style="display:none">Async Inserts</a>
</nav>

<main>

<!-- ── ALERTS ── -->
<section id="sec-alerts">
  <h2>🚨 Alert Summary</h2>
  <div id="alerts-summary-bar"></div>
  <div id="alerts-panel"></div>
</section>

<!-- ── OVERVIEW ── -->
<section id="sec-overview">
  <h2>📈 Overview</h2>
  <div class="stats-grid" id="stats-grid"></div>
</section>

<!-- ── STORAGE ── -->
<section id="sec-storage">
  <h2>📦 Storage</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>Data Size by Database</h3>
      <div class="chart-wrap h360"><canvas id="chart-storage-db"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Table Engine Distribution</h3>
      <div class="chart-wrap h260"><canvas id="chart-engines"></canvas></div>
    </div>
  </div>
  <div class="sub-title">Top 20 Tables by Size</div>
  <div class="tbl-wrap"><div id="tbl-top-tables"></div></div>
</section>

<!-- ── TABLES EXPLORER ── -->
<section id="sec-tables">
  <h2>📋 Tables Explorer</h2>
  <div class="filter-bar">
    <input type="text" id="tbl-search" placeholder="Search by name, engine, key…" oninput="tablesFilter()">
    <select id="tbl-db-filter" onchange="tablesFilter()"><option value="">All databases</option></select>
    <select id="tbl-engine-filter" onchange="tablesFilter()"><option value="">All engines</option></select>
    <span class="count-badge" id="tbl-count"></span>
  </div>
  <div class="tbl-wrap"><div id="tbl-explorer"></div></div>
  <div class="pagination" id="tbl-pagination"></div>
</section>

<!-- ── QUERY ACTIVITY ── -->
<section id="sec-queries">
  <h2>📊 Query Activity (last 7 days)</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>Queries per Hour by Type</h3>
      <div class="chart-wrap h260"><canvas id="chart-query-time"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Query Count by Kind</h3>
      <div class="chart-wrap h200"><canvas id="chart-query-kind"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Avg Duration &amp; Memory by Kind</h3>
      <div class="chart-wrap h200"><canvas id="chart-query-duration"></canvas></div>
    </div>
  </div>
</section>

<!-- ── QUERY DEEP DIVE ── -->
<section id="sec-deepdive">
  <h2>🔍 Query Deep Dive (last 7 days)</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>Top 20 Slowest Query Patterns (avg duration ms)</h3>
      <div class="chart-wrap h420"><canvas id="chart-slow-queries"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Top 20 Heaviest Reads (avg MB / query)</h3>
      <div class="chart-wrap h420"><canvas id="chart-heavy-reads"></canvas></div>
    </div>
    <div class="chart-card" style="grid-column:1/-1">
      <h3>Activity by User — Executions &amp; Errors (last 7 days)</h3>
      <div class="chart-wrap h200"><canvas id="chart-user-activity"></canvas></div>
    </div>
  </div>
  <div class="sub-title">Slow Query Details</div>
  <div class="tbl-wrap"><div id="tbl-slow-queries"></div></div>
  <div class="sub-title" style="margin-top:18px">User Summary</div>
  <div class="tbl-wrap"><div id="tbl-user-summary"></div></div>
</section>

<!-- ── EXCEPTIONS ── -->
<section id="sec-exceptions">
  <h2>⚠️ Exceptions (last 7 days)</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>Top Exception Codes</h3>
      <div class="chart-wrap h260"><canvas id="chart-exceptions"></canvas></div>
    </div>
    <div class="chart-card" style="display:flex;flex-direction:column">
      <h3>Exception Details</h3>
      <div class="tbl-wrap" style="max-height:320px;overflow-y:auto"><div id="tbl-exceptions"></div></div>
    </div>
  </div>
</section>

<!-- ── PART LOG ── -->
<section id="sec-partlog">
  <h2>🔧 Part Log Events (last 7 days)</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>Part Events per Day by Type</h3>
      <div class="chart-wrap h260"><canvas id="chart-partlog-time"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Part Event Type Distribution</h3>
      <div class="chart-wrap h260"><canvas id="chart-partlog-type"></canvas></div>
    </div>
  </div>
</section>

<!-- ── DICTIONARIES ── -->
<section id="sec-dicts">
  <h2>📖 Dictionaries</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>Status Distribution</h3>
      <div class="chart-wrap h200"><canvas id="chart-dict-status"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Bytes Allocated per Dictionary</h3>
      <div class="chart-wrap h300"><canvas id="chart-dict-bytes"></canvas></div>
    </div>
    <div class="chart-card" style="grid-column:1/-1">
      <h3>Lifetime Configuration — min &amp; max (seconds)</h3>
      <div class="chart-wrap h200"><canvas id="chart-dict-lifetime"></canvas></div>
    </div>
  </div>
  <div class="sub-title">Dictionary Details</div>
  <div class="tbl-wrap"><div id="tbl-dicts"></div></div>
</section>

<!-- ── CRASH LOG ── -->
<section id="sec-crashlog" style="display:none">
  <h2>💥 Crash Log</h2>
  <div class="tbl-wrap"><div id="tbl-crash-log"></div></div>
</section>

<!-- ── PENDING WORK ── -->
<section id="sec-pending">
  <h2>⏳ Pending Operations</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>Pending Mutations</h3>
      <div class="tbl-wrap"><div id="tbl-mutations"></div></div>
    </div>
    <div class="chart-card">
      <h3>Detached Parts</h3>
      <div class="tbl-wrap"><div id="tbl-detached"></div></div>
    </div>
  </div>
</section>

<!-- ── REPLICATION QUEUE ── -->
<section id="sec-replication">
  <h2>🔄 Replication Queue</h2>
  <div class="tbl-wrap"><div id="tbl-replication"></div></div>
</section>

<!-- ── CLUSTERS ── -->
<section id="sec-clusters" style="display:none">
  <h2>🌐 Cluster Nodes</h2>
  <div class="tbl-wrap"><div id="tbl-clusters"></div></div>
</section>

<!-- ── REPLICAS HEALTH ── -->
<section id="sec-replicas" style="display:none">
  <h2>🔁 Replicas Health</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>Replication Delay Distribution</h3>
      <div class="chart-wrap h260"><canvas id="chart-replica-delay"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Queue Size by Table (top 15)</h3>
      <div class="chart-wrap h260"><canvas id="chart-replica-queue"></canvas></div>
    </div>
  </div>
  <div class="sub-title">Replica Details</div>
  <div class="tbl-wrap"><div id="tbl-replicas"></div></div>
</section>

<!-- ── DISK USAGE ── -->
<section id="sec-disks">
  <h2>💾 Disk Usage</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>Free vs Used Space per Disk</h3>
      <div class="chart-wrap h220"><canvas id="chart-disk-usage"></canvas></div>
    </div>
  </div>
  <div class="sub-title">Disk Details</div>
  <div class="tbl-wrap"><div id="tbl-disks"></div></div>
</section>

<!-- ── SERVER ERRORS ── -->
<section id="sec-server-errors">
  <h2>🛑 Server Error Counters</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>Top 20 Error Codes (cumulative since last restart)</h3>
      <div class="chart-wrap h300"><canvas id="chart-server-errors"></canvas></div>
    </div>
  </div>
  <div class="sub-title">High Part-Count Partitions (&gt;100 parts — potential code 497 risk)</div>
  <div class="tbl-wrap"><div id="tbl-high-parts"></div></div>
  <div class="sub-title" style="margin-top:18px">TTL Activity (last 7 days)</div>
  <div class="tbl-wrap"><div id="tbl-ttl-activity"></div></div>
</section>

<!-- ── ASYNC INSERTS ── -->
<section id="sec-async-inserts" style="display:none">
  <h2>⚡ Async Insert Activity (last 24 h)</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>Async Flush Count per Hour by Status</h3>
      <div class="chart-wrap h260"><canvas id="chart-async-inserts"></canvas></div>
    </div>
  </div>
  <div class="tbl-wrap"><div id="tbl-async-inserts"></div></div>
</section>

</main>
<footer>Generated by ClickHouse Diagnostic Tool &mdash; requires Chart.js CDN</footer>

<script>
const DATA = /*DATA*/null;

// ── palette ──────────────────────────────────────────────────────────────────
const C = [
  '#FC4F05','#FFB627','#2196F3','#4CAF50','#9C27B0',
  '#00BCD4','#FF5722','#607D8B','#E91E63','#3F51B5',
  '#8BC34A','#FF9800','#795548','#009688','#CDDC39'
];
const alpha = (h,a) => h + Math.round(a*255).toString(16).padStart(2,'0');
const DICT_STATUS_COLOR = {
  LOADED:'#4CAF50', FAILED:'#f44336', LOADING:'#FF9800',
  NOT_LOADED:'#9E9E9E', UNKNOWN:'#2196F3'
};

// ── helpers ───────────────────────────────────────────────────────────────────
function fmt(n){
  if(n==null||n==='')return '—';
  if(typeof n==='number'){
    if(n>=1e12)return(n/1e12).toFixed(2)+' T';
    if(n>=1e9)return(n/1e9).toFixed(2)+' B';
    if(n>=1e6)return(n/1e6).toFixed(2)+' M';
    if(n>=1e3)return(n/1e3).toFixed(1)+' K';
    return n.toLocaleString();
  }
  return String(n);
}

function shortHash(h){
  const s=String(h||'');
  return s.length>8?'…'+s.slice(-8):s;
}

// pivot [{timeF, catF, valF}] → Chart.js datasets
function pivot(rows,tf,cf,vf){
  const times=[...new Set(rows.map(r=>r[tf]))].sort();
  const cats=[...new Set(rows.map(r=>r[cf]))];
  const lk={};
  rows.forEach(r=>{lk[r[tf]+'|'+r[cf]]=r[vf];});
  return{
    labels:times,
    datasets:cats.map((c,i)=>({
      label:c||'(unknown)',
      backgroundColor:alpha(C[i%C.length],.75),
      borderColor:C[i%C.length],
      borderWidth:1.5,
      data:times.map(t=>lk[t+'|'+c]||0)
    }))
  };
}

// render a plain HTML table (with optional row-class callback)
function renderTable(id, rows, cols, rowClass){
  const el=document.getElementById(id);
  if(!el)return;
  if(!rows||rows.length===0){el.innerHTML='<p class="no-data">No data</p>';return;}
  const keys=cols||Object.keys(rows[0]);
  let h='<table class="dt"><thead><tr>'+keys.map(k=>'<th>'+k+'</th>').join('')+'</tr></thead><tbody>';
  rows.forEach(r=>{
    const cls=rowClass?rowClass(r):'';
    h+='<tr'+(cls?' class="'+cls+'"':'')+'>'
      +keys.map(k=>'<td title="'+(r[k]??'')+'">'+(r[k]??'')+'</td>').join('')
      +'</tr>';
  });
  h+='</tbody></table>';
  el.innerHTML=h;
}

// format status badges for dictionaries
function dictStatusBadge(status){
  const cl={'LOADED':'ok-badge','FAILED':'err-badge','LOADING':'warn-badge',
            'NOT_LOADED':'warn-badge'}[status]||'warn-badge';
  return '<span class="'+cl+'">'+status+'</span>';
}

// ── searchable tables explorer ────────────────────────────────────────────────
(function(){
  const PAGE=50;
  let allData=[], filtered=[], page=0;
  const COLS=['database','table_name','engine','parts','total_rows','size',
              'partition_key','sorting_key','storage_policy'];

  function render(){
    const el=document.getElementById('tbl-explorer');
    const pg=document.getElementById('tbl-pagination');
    const cnt=document.getElementById('tbl-count');
    if(!el)return;
    if(!filtered.length){
      el.innerHTML='<p class="no-data">No tables match</p>';
      pg.innerHTML=''; cnt.textContent='0 tables';
      return;
    }
    const start=page*PAGE, end=Math.min(start+PAGE,filtered.length);
    const slice=filtered.slice(start,end);
    let h='<table class="dt"><thead><tr>'
      +COLS.map(k=>'<th>'+k+'</th>').join('')+'</tr></thead><tbody>';
    slice.forEach(r=>{
      h+='<tr>'+COLS.map(k=>'<td title="'+(r[k]??'')+'">'+(r[k]??'')+'</td>').join('')+'</tr>';
    });
    h+='</tbody></table>';
    el.innerHTML=h;
    cnt.textContent=filtered.length+' / '+allData.length+' tables';
    const total=Math.ceil(filtered.length/PAGE);
    if(total<=1){pg.innerHTML='';return;}
    let phtml='';
    if(page>0) phtml+='<button onclick="window._tblPg('+(page-1)+')">&#9664;</button>';
    phtml+='<span class="cur">'+( page+1)+' / '+total+'</span>';
    if(page<total-1) phtml+='<button onclick="window._tblPg('+(page+1)+')">&#9654;</button>';
    pg.innerHTML=phtml;
  }

  window._tblPg=function(p){page=p;render();};

  window.tablesFilter=function(){
    const srch=(document.getElementById('tbl-search').value||'').toLowerCase();
    const db=document.getElementById('tbl-db-filter').value;
    const eng=document.getElementById('tbl-engine-filter').value;
    filtered=allData.filter(r=>{
      if(db&&r.database!==db)return false;
      if(eng&&r.engine!==eng)return false;
      if(srch){
        return COLS.some(k=>String(r[k]??'').toLowerCase().includes(srch));
      }
      return true;
    });
    page=0; render();
  };

  window._tablesInit=function(data){
    allData=data||[];
    filtered=[...allData];
    // populate dropdowns
    const dbSel=document.getElementById('tbl-db-filter');
    const engSel=document.getElementById('tbl-engine-filter');
    [...new Set(allData.map(r=>r.database))].sort().forEach(d=>{
      dbSel.innerHTML+='<option value="'+d+'">'+d+'</option>';
    });
    [...new Set(allData.map(r=>r.engine))].sort().forEach(e=>{
      engSel.innerHTML+='<option value="'+e+'">'+e+'</option>';
    });
    render();
  };
})();

// ── alerts renderer ───────────────────────────────────────────────────────────
function renderAlerts(){
  const alerts=DATA.alerts||[];
  const fired=alerts.filter(a=>(a.rows&&a.rows.length>0)||a.error);
  const panel=document.getElementById('alerts-panel');
  const bar=document.getElementById('alerts-summary-bar');
  const navLink=document.getElementById('nav-alerts');

  // summary chips
  const counts={critical:0,warning:0,info:0};
  fired.forEach(a=>{const s=a.severity||'warning';if(counts[s]!==undefined)counts[s]++;});
  const total=fired.length;

  // badge the nav link
  if(total>0){
    navLink.innerHTML='Alerts <span style="background:#f44336;color:#fff;border-radius:10px;padding:1px 7px;font-size:11px;font-weight:700;margin-left:4px">'+total+'</span>';
  }

  // summary bar
  let barHtml='';
  if(counts.critical) barHtml+='<span class="alert-summary-chip chip-critical">🔴 '+counts.critical+' Critical</span>';
  if(counts.warning)  barHtml+='<span class="alert-summary-chip chip-warning">🟡 '+counts.warning+' Warning</span>';
  if(counts.info)     barHtml+='<span class="alert-summary-chip chip-info">🔵 '+counts.info+' Info</span>';
  if(bar) bar.innerHTML=barHtml?'<div class="alert-summary-bar">'+barHtml+'</div>':'';

  if(!panel) return;

  if(!fired.length){
    panel.innerHTML='<div class="alert-ok">✅ All '+alerts.length+' alert rule(s) evaluated — no issues detected</div>';
    return;
  }

  const SEV_ICON={critical:'🔴',warning:'🟡',info:'🔵'};
  const ORDER={critical:0,warning:1,info:2};
  const sorted=[...fired].sort((a,b)=>(ORDER[a.severity]||1)-(ORDER[b.severity]||1));

  let html='';
  sorted.forEach(a=>{
    const sev=a.severity||'warning';
    const icon=SEV_ICON[sev]||'🟡';
    const cls=a.error?'alert-error':'alert-'+sev;
    const cnt=a.error?'error':(a.rows||[]).length;
    const cntLabel=a.error?'query error':cnt+' instance'+(cnt===1?'':'s');

    html+='<div class="alert-item '+cls+'">';
    html+='<div class="alert-header">';
    html+=icon+' <span class="alert-title">'+(a.title||a.name)+'</span>';
    html+='<span class="alert-count badge-'+(a.error?'error':sev)+'">'+cntLabel+'</span>';
    if((a.tags||[]).length) html+='<span class="alert-tags">'+a.tags.map(t=>'<span class="alert-tag">'+t+'</span>').join('')+'</span>';
    html+='</div>'; // header

    if(a.description) html+='<div class="alert-desc">'+a.description.trim().replace(/\n/g,'<br>')+'</div>';

    if(a.error){
      html+='<div class="alert-err-msg">⚠ '+a.error+'</div>';
    } else if(a.message&&(a.rows||[]).length){
      html+='<ul class="alert-messages">';
      (a.rows||[]).forEach(row=>{
        let msg=a.message;
        Object.entries(row).forEach(([k,v])=>{msg=msg.split('{'+k+'}').join(String(v??''));});
        html+='<li>▸ '+msg+'</li>';
      });
      html+='</ul>';
    }

    html+='</div>'; // item
  });
  panel.innerHTML=html;
}

// ── main init ─────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded',function(){
  if(!DATA){document.body.innerHTML='<p style="padding:40px;color:red">No embedded data.</p>';return;}

  renderAlerts();

  // nav active highlight on scroll
  const secs=[...document.querySelectorAll('section[id]')];
  const navLinks=[...document.querySelectorAll('nav a')];
  window.addEventListener('scroll',function(){
    let cur='';
    secs.forEach(s=>{if(s.getBoundingClientRect().top<=130)cur=s.id;});
    navLinks.forEach(a=>{
      a.classList.toggle('active',a.getAttribute('href')==='#'+cur);
    });
  },{passive:true});

  // header
  document.getElementById('hdr-badge').innerHTML=
    '<span class="badge badge-'+DATA.mode+'">'+DATA.mode+'</span>';
  document.getElementById('hdr-meta').innerHTML=
    'Generated: '+DATA.generated_at+'<br>Version: '+(DATA.version||'N/A');

  // stats
  const sg=document.getElementById('stats-grid');
  sg.innerHTML=[
    '<div class="stat-card"><div class="val">'+(DATA.version||'N/A')+'</div><div class="lbl">Server Version</div></div>',
    '<div class="stat-card"><div class="val">'+(DATA.uptime||'N/A')+'</div><div class="lbl">Uptime</div></div>',
    '<div class="stat-card"><div class="val">'+fmt(DATA.total_databases)+'</div><div class="lbl">Databases</div></div>',
    '<div class="stat-card"><div class="val">'+fmt(DATA.total_tables)+'</div><div class="lbl">Tables</div></div>',
    '<div class="stat-card"><div class="val">'+fmt(DATA.active_parts)+'</div><div class="lbl">Active Parts</div></div>',
    '<div class="stat-card"><div class="val">'+(DATA.total_size||'N/A')+'</div><div class="lbl">Total Data Size</div></div>',
  ].join('');

  // ── Storage: size by database (horizontal bar) ───────────────────────────
  (function(){
    const rows=DATA.storage_by_db||[];
    if(!rows.length)return;
    new Chart(document.getElementById('chart-storage-db'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.database),
        datasets:[{
          label:'Compressed (bytes)',
          data:rows.map(r=>r.bytes_on_disk),
          backgroundColor:rows.map((_,i)=>alpha(C[i%C.length],.8)),
          borderColor:rows.map((_,i)=>C[i%C.length]),
          borderWidth:1
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>{
          const r=rows[ctx.dataIndex];
          return[' Size: '+r.size_human,' Rows: '+fmt(r.rows),' Compression: '+r.compression_ratio+'x'];
        }}}},
        scales:{x:{ticks:{callback:v=>v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
      }
    });
  })();

  // ── Storage: engine distribution (doughnut) ──────────────────────────────
  (function(){
    const rows=DATA.engines_dist||[];
    if(!rows.length)return;
    new Chart(document.getElementById('chart-engines'),{
      type:'doughnut',
      data:{labels:rows.map(r=>r.engine),datasets:[{data:rows.map(r=>r.count),backgroundColor:C.slice(0,rows.length),borderWidth:2}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{position:'right'}}}
    });
  })();

  // top tables
  renderTable('tbl-top-tables',DATA.top_tables||[],
    ['database','table','parts','total_rows','compressed_size','compression_ratio']);

  // ── Tables explorer ───────────────────────────────────────────────────────
  window._tablesInit(DATA.tables_list||[]);

  // ── Query: over time (line) ───────────────────────────────────────────────
  (function(){
    const rows=DATA.query_by_time||[];
    if(!rows.length)return;
    const d=pivot(rows,'time','query_kind','count');
    d.datasets.forEach(ds=>{ds.fill=false;ds.tension=0.3;ds.pointRadius=2;});
    new Chart(document.getElementById('chart-query-time'),{
      type:'line',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{ticks:{maxTicksLimit:12,maxRotation:45}},y:{beginAtZero:true}}}
    });
  })();

  // ── Query: count by kind (bar) ────────────────────────────────────────────
  (function(){
    const rows=DATA.query_by_kind||[];
    if(!rows.length)return;
    new Chart(document.getElementById('chart-query-kind'),{
      type:'bar',
      data:{labels:rows.map(r=>r.query_kind||'unknown'),
        datasets:[{label:'Count',data:rows.map(r=>r.count),
          backgroundColor:rows.map((_,i)=>alpha(C[i%C.length],.8)),
          borderColor:rows.map((_,i)=>C[i%C.length]),borderWidth:1}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{y:{beginAtZero:true}}}
    });
  })();

  // ── Query: avg duration + memory by kind (dual-axis bar) ─────────────────
  (function(){
    const rows=DATA.query_by_kind||[];
    if(!rows.length)return;
    new Chart(document.getElementById('chart-query-duration'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.query_kind||'unknown'),
        datasets:[
          {label:'Avg Duration (ms)',data:rows.map(r=>r.avg_duration_ms),backgroundColor:alpha('#FC4F05',.75),borderColor:'#FC4F05',borderWidth:1},
          {label:'Avg Memory (MB)',data:rows.map(r=>r.avg_memory_mb),backgroundColor:alpha('#2196F3',.75),borderColor:'#2196F3',borderWidth:1,yAxisID:'y2'}
        ]
      },
      options:{
        responsive:true,maintainAspectRatio:false,plugins:{legend:{position:'top'}},
        scales:{
          y:{beginAtZero:true,title:{display:true,text:'ms'}},
          y2:{beginAtZero:true,position:'right',title:{display:true,text:'MB'},grid:{drawOnChartArea:false}}
        }
      }
    });
  })();

  // ── Deep dive: slowest queries (horizontal bar) ───────────────────────────
  (function(){
    const rows=DATA.query_slow||[];
    if(!rows.length)return;
    const labels=rows.map(r=>r.query_kind+' | '+shortHash(r.hash)+' ('+r.user+')');
    new Chart(document.getElementById('chart-slow-queries'),{
      type:'bar',
      data:{
        labels:labels,
        datasets:[
          {label:'Avg Duration (ms)',data:rows.map(r=>r.avg_duration_ms),backgroundColor:alpha('#FC4F05',.8),borderColor:'#FC4F05',borderWidth:1},
          {label:'Max Duration (ms)',data:rows.map(r=>r.max_duration_ms),backgroundColor:alpha('#FFB627',.6),borderColor:'#FFB627',borderWidth:1}
        ]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'},tooltip:{callbacks:{
          afterLabel:ctx=>{
            const r=rows[ctx.dataIndex];
            return['Executions: '+r.executions,'Errors: '+r.errors,'Avg Read: '+r.avg_read_mb+' MB','Avg Mem: '+r.avg_memory_mb+' MB'];
          }
        }}},
        scales:{x:{beginAtZero:true,title:{display:true,text:'milliseconds'}}}
      }
    });
  })();

  // ── Deep dive: heaviest reads (horizontal bar) ────────────────────────────
  (function(){
    const rows=DATA.query_heavy||[];
    if(!rows.length)return;
    const labels=rows.map(r=>r.query_kind+' | '+shortHash(r.hash)+' ('+r.user+')');
    new Chart(document.getElementById('chart-heavy-reads'),{
      type:'bar',
      data:{
        labels:labels,
        datasets:[{
          label:'Avg Read (MB)',
          data:rows.map(r=>r.avg_read_mb),
          backgroundColor:rows.map((_,i)=>alpha(C[i%C.length],.8)),
          borderColor:rows.map((_,i)=>C[i%C.length]),
          borderWidth:1
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{
          label:ctx=>' Avg Read: '+ctx.raw+' MB',
          afterLabel:ctx=>{
            const r=rows[ctx.dataIndex];
            return['Executions: '+r.executions,'Total Read: '+r.total_read,'Avg Duration: '+r.avg_duration_ms+' ms'];
          }
        }}},
        scales:{x:{beginAtZero:true,title:{display:true,text:'MB per query (avg)'}}}
      }
    });
  })();

  // ── Deep dive: user activity (grouped bar) ────────────────────────────────
  (function(){
    const rows=DATA.query_by_user||[];
    if(!rows.length)return;
    new Chart(document.getElementById('chart-user-activity'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.user),
        datasets:[
          {label:'Executions',data:rows.map(r=>r.executions),backgroundColor:alpha('#2196F3',.8),borderColor:'#2196F3',borderWidth:1},
          {label:'Errors',data:rows.map(r=>r.error_count),backgroundColor:alpha('#f44336',.8),borderColor:'#f44336',borderWidth:1}
        ]
      },
      options:{
        responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'},tooltip:{callbacks:{
          afterLabel:ctx=>{
            if(ctx.datasetIndex!==0)return;
            const r=rows[ctx.dataIndex];
            return['Avg Duration: '+r.avg_duration_ms+' ms','Total Read: '+r.total_read_gb+' GB','Total Memory: '+r.total_memory_gb+' GB'];
          }
        }}},
        scales:{y:{beginAtZero:true}}
      }
    });
  })();

  // slow query table
  renderTable('tbl-slow-queries',DATA.query_slow||[],
    ['query_kind','user','executions','avg_duration_ms','max_duration_ms','avg_read_mb','avg_memory_mb','errors','hash'],
    r=>r.errors>0?'alert-row':'');

  // user summary table
  renderTable('tbl-user-summary',DATA.query_by_user||[],
    ['user','executions','avg_duration_ms','total_read_gb','total_memory_gb','error_count'],
    r=>r.error_count>0?'alert-row':'');

  // ── Exceptions ────────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.exceptions||[];
    if(!rows.length){
      document.getElementById('chart-exceptions').parentElement.innerHTML='<p class="no-data">No exceptions in the last 7 days</p>';
      return;
    }
    new Chart(document.getElementById('chart-exceptions'),{
      type:'bar',
      data:{
        labels:rows.map(r=>'Code '+r.exception_code),
        datasets:[{label:'Count',data:rows.map(r=>r.count),
          backgroundColor:alpha('#E91E63',.75),borderColor:'#E91E63',borderWidth:1}]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>' Count: '+ctx.raw}}},
        scales:{x:{beginAtZero:true}}
      }
    });
  })();
  renderTable('tbl-exceptions',DATA.exceptions||[],['exception_code','count','msg']);

  // ── Part log: over time (stacked bar) ────────────────────────────────────
  (function(){
    const rows=DATA.part_log_by_time||[];
    if(!rows.length)return;
    const d=pivot(rows,'time','event_type','count');
    d.datasets.forEach(ds=>{ds.stack='s';});
    new Chart(document.getElementById('chart-partlog-time'),{
      type:'bar',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true,ticks:{maxTicksLimit:14,maxRotation:45}},y:{stacked:true,beginAtZero:true}}}
    });
  })();

  // ── Part log: by type (doughnut) ─────────────────────────────────────────
  (function(){
    const rows=DATA.part_log_by_type||[];
    if(!rows.length)return;
    new Chart(document.getElementById('chart-partlog-type'),{
      type:'doughnut',
      data:{
        labels:rows.map(r=>r.event_type+' ('+r.total_size+')'),
        datasets:[{data:rows.map(r=>r.count),backgroundColor:C.slice(0,rows.length),borderWidth:2}]
      },
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{position:'right'}}}
    });
  })();

  // ── Dictionaries ──────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.dictionaries||[];
    if(!rows.length){
      ['chart-dict-status','chart-dict-bytes','chart-dict-lifetime'].forEach(id=>{
        const el=document.getElementById(id);
        if(el)el.parentElement.innerHTML='<p class="no-data">No dictionaries</p>';
      });
      document.getElementById('tbl-dicts').innerHTML='<p class="no-data">No dictionaries</p>';
      return;
    }

    // status distribution (pie)
    const statusMap={};
    rows.forEach(r=>{statusMap[r.status]=(statusMap[r.status]||0)+1;});
    const statLabels=Object.keys(statusMap);
    new Chart(document.getElementById('chart-dict-status'),{
      type:'pie',
      data:{
        labels:statLabels,
        datasets:[{data:statLabels.map(s=>statusMap[s]),
          backgroundColor:statLabels.map(s=>DICT_STATUS_COLOR[s]||'#607D8B'),borderWidth:2}]
      },
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{position:'right'}}}
    });

    // bytes allocated (horizontal bar)
    const topDicts=rows.slice(0,15);
    new Chart(document.getElementById('chart-dict-bytes'),{
      type:'bar',
      data:{
        labels:topDicts.map(r=>r.database+'.'+r.name),
        datasets:[{
          label:'Bytes Allocated',
          data:topDicts.map(r=>r.bytes_allocated),
          backgroundColor:topDicts.map((_,i)=>alpha(C[i%C.length],.8)),
          borderColor:topDicts.map((_,i)=>C[i%C.length]),borderWidth:1
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>' '+topDicts[ctx.dataIndex].bytes_allocated_human}}},
        scales:{x:{ticks:{callback:v=>v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
      }
    });

    // lifetime range (grouped bar)
    const lifeRows=rows.filter(r=>r.lifetime_max>0||r.lifetime_min>0).slice(0,20);
    if(lifeRows.length){
      new Chart(document.getElementById('chart-dict-lifetime'),{
        type:'bar',
        data:{
          labels:lifeRows.map(r=>r.name),
          datasets:[
            {label:'Lifetime Min (s)',data:lifeRows.map(r=>r.lifetime_min),backgroundColor:alpha('#2196F3',.75),borderColor:'#2196F3',borderWidth:1},
            {label:'Lifetime Max (s)',data:lifeRows.map(r=>r.lifetime_max),backgroundColor:alpha('#FF9800',.75),borderColor:'#FF9800',borderWidth:1}
          ]
        },
        options:{
          responsive:true,maintainAspectRatio:false,
          interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'}},
          scales:{x:{ticks:{maxRotation:45}},y:{beginAtZero:true,title:{display:true,text:'seconds'}}}
        }
      });
    } else {
      document.getElementById('chart-dict-lifetime').parentElement.innerHTML='<p class="no-data">No lifetime data</p>';
    }

    // dictionary detail table — render status as badge inline
    const el=document.getElementById('tbl-dicts');
    const cols=['database','name','status','type','bytes_allocated_human','element_count',
                'hit_rate_pct','found_rate_pct','lifetime_min','lifetime_max',
                'last_update','loading_duration_s','last_exception'];
    let h='<table class="dt"><thead><tr>'+cols.map(k=>'<th>'+k+'</th>').join('')+'</tr></thead><tbody>';
    rows.forEach(r=>{
      const isFailed=r.status==='FAILED'||String(r.last_exception||'').length>1;
      h+='<tr'+(isFailed?' class="error-row"':r.status==='LOADING'?' class="alert-row"':'')+'>';
      cols.forEach(k=>{
        if(k==='status') h+='<td>'+dictStatusBadge(r[k])+'</td>';
        else h+='<td title="'+(r[k]??'')+'">'+(r[k]??'')+'</td>';
      });
      h+='</tr>';
    });
    h+='</tbody></table>';
    el.innerHTML=h;
  })();

  // ── Crash log ─────────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.crash_log||[];
    if(rows.length>0){
      document.getElementById('sec-crashlog').style.display='';
      document.getElementById('nav-crashlog').style.display='';
      renderTable('tbl-crash-log',rows,
        ['event_time','signal','thread_id','query_id','version'],
        ()=>'error-row');
    }
  })();

  // ── Pending work ──────────────────────────────────────────────────────────
  renderTable('tbl-mutations',DATA.mutations||[],
    ['database','table','mutation_id','parts_to_do','create_time','command']);
  renderTable('tbl-detached',DATA.detached||[],
    ['database','table','count','size','reasons']);
  renderTable('tbl-replication',DATA.replication_queue||[],
    ['table','type','executing','last_exception'],
    r=>r.last_exception&&r.last_exception.length>1?'error-row':'');

  // ── Clusters (cloud only) ────────────────────────────────────────────────
  if(DATA.mode==='cloud'&&(DATA.clusters||[]).length>0){
    document.getElementById('sec-clusters').style.display='';
    document.getElementById('nav-clusters').style.display='';
    renderTable('tbl-clusters',DATA.clusters,
      ['cluster','shard','replica','host_name','is_active','errors_count'],
      r=>r.errors_count&&r.errors_count!=='0'?'alert-row':'');
  }

  // ── Replicas health ───────────────────────────────────────────────────────
  (function(){
    const rows=DATA.replicas||[];
    if(!rows.length)return;
    document.getElementById('sec-replicas').style.display='';
    document.getElementById('nav-replicas').style.display='';

    // delay distribution (bar)
    const delayBuckets={'0s':0,'<10s':0,'<60s':0,'<5m':0,'≥5m':0};
    rows.forEach(r=>{
      const d=Number(r.absolute_delay||0);
      if(d===0)delayBuckets['0s']++;
      else if(d<10)delayBuckets['<10s']++;
      else if(d<60)delayBuckets['<60s']++;
      else if(d<300)delayBuckets['<5m']++;
      else delayBuckets['≥5m']++;
    });
    const bLabels=Object.keys(delayBuckets);
    const bData=bLabels.map(k=>delayBuckets[k]);
    const bColors=['#4CAF50','#8BC34A','#FFB627','#FF5722','#f44336'];
    new Chart(document.getElementById('chart-replica-delay'),{
      type:'bar',
      data:{labels:bLabels,datasets:[{label:'Replicas',data:bData,
        backgroundColor:bColors,borderColor:bColors,borderWidth:1}]},
      options:{responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false}},scales:{y:{beginAtZero:true,ticks:{stepSize:1}}}}
    });

    // queue size horizontal bar (top 15)
    const topQ=[...rows].sort((a,b)=>Number(b.queue_size||0)-Number(a.queue_size||0)).slice(0,15);
    if(topQ.some(r=>Number(r.queue_size||0)>0)){
      new Chart(document.getElementById('chart-replica-queue'),{
        type:'bar',
        data:{
          labels:topQ.map(r=>r.database+'.'+r.table),
          datasets:[
            {label:'Queue Size',data:topQ.map(r=>r.queue_size||0),backgroundColor:alpha('#FC4F05',.75),borderColor:'#FC4F05',borderWidth:1},
            {label:'Inserts In Queue',data:topQ.map(r=>r.inserts_in_queue||0),backgroundColor:alpha('#2196F3',.75),borderColor:'#2196F3',borderWidth:1},
            {label:'Merges In Queue',data:topQ.map(r=>r.merges_in_queue||0),backgroundColor:alpha('#4CAF50',.75),borderColor:'#4CAF50',borderWidth:1}
          ]
        },
        options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
          interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'}},scales:{x:{beginAtZero:true}}}
      });
    } else {
      document.getElementById('chart-replica-queue').parentElement.innerHTML='<p class="no-data">No queue entries</p>';
    }

    renderTable('tbl-replicas',rows,
      ['database','table','is_readonly','is_session_expired','is_leader',
       'queue_size','inserts_in_queue','merges_in_queue','future_parts',
       'parts_to_check','absolute_delay','active_replicas','total_replicas'],
      r=>r.is_readonly?'error-row':(Number(r.absolute_delay||0)>60?'alert-row':''));
  })();

  // ── Disk usage ────────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.disks||[];
    const WARN_PCT=15, CRIT_PCT=5;
    if(rows.length){
      new Chart(document.getElementById('chart-disk-usage'),{
        type:'bar',
        data:{
          labels:rows.map(r=>r.name),
          datasets:[
            {label:'Used',data:rows.map(r=>Number(r.total_space||0)-Number(r.free_space||0)),
              backgroundColor:rows.map(r=>Number(r.free_pct||100)<CRIT_PCT?alpha('#f44336',.8):Number(r.free_pct||100)<WARN_PCT?alpha('#FF9800',.8):alpha('#2196F3',.75)),
              borderColor:rows.map(r=>Number(r.free_pct||100)<CRIT_PCT?'#f44336':Number(r.free_pct||100)<WARN_PCT?'#FF9800':'#2196F3'),
              borderWidth:1},
            {label:'Free',data:rows.map(r=>Number(r.free_space||0)),
              backgroundColor:alpha('#4CAF50',.6),borderColor:'#4CAF50',borderWidth:1}
          ]
        },
        options:{
          responsive:true,maintainAspectRatio:false,
          interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'},tooltip:{callbacks:{
            label:ctx=>{
              const r=rows[ctx.dataIndex];
              if(ctx.datasetIndex===0)return' Used: '+r.total_space_human+' total, '+r.free_pct+'% free';
              return' Free: '+r.free_space_human;
            }
          }}},
          scales:{x:{stacked:true},y:{stacked:true,beginAtZero:true,
            ticks:{callback:v=>v>=1e12?(v/1e12).toFixed(1)+'T':v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
        }
      });
    }
    renderTable('tbl-disks',rows,
      ['name','type','free_space_human','total_space_human','free_pct','path'],
      r=>Number(r.free_pct||100)<CRIT_PCT?'error-row':Number(r.free_pct||100)<WARN_PCT?'alert-row':'');
  })();

  // ── Server errors ─────────────────────────────────────────────────────────
  (function(){
    const rows=(DATA.server_errors||[]).slice(0,20);
    if(rows.length){
      new Chart(document.getElementById('chart-server-errors'),{
        type:'bar',
        data:{
          labels:rows.map(r=>r.name||'code '+r.code),
          datasets:[{label:'Total count (since restart)',
            data:rows.map(r=>r.value),
            backgroundColor:rows.map((_,i)=>alpha(C[i%C.length],.8)),
            borderColor:rows.map((_,i)=>C[i%C.length]),borderWidth:1}]
        },
        options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
          plugins:{legend:{display:false},tooltip:{callbacks:{
            afterLabel:ctx=>{
              const r=rows[ctx.dataIndex];
              return['Code: '+r.code,'Last seen: '+r.last_error_time,'Msg: '+(r.last_error_message||'').slice(0,120)];
            }
          }}},
          scales:{x:{beginAtZero:true}}}
      });
    } else {
      document.getElementById('chart-server-errors').parentElement.innerHTML='<p class="no-data">No server errors recorded</p>';
    }

    renderTable('tbl-high-parts',DATA.high_part_count||[],
      ['database','table','partition_id','parts_count'],
      r=>Number(r.parts_count||0)>=500?'error-row':Number(r.parts_count||0)>=300?'alert-row':'');

    renderTable('tbl-ttl-activity',DATA.ttl_activity||[],
      ['day','event_type','merge_reason','events','total_size']);
  })();

  // ── Async inserts (non-gov only) ──────────────────────────────────────────
  (function(){
    const rows=DATA.async_inserts||[];
    if(!rows.length)return;
    document.getElementById('sec-async-inserts').style.display='';
    document.getElementById('nav-async-inserts').style.display='';
    const d=pivot(rows,'hour','status','flushes');
    d.datasets.forEach(ds=>{ds.stack='s';});
    new Chart(document.getElementById('chart-async-inserts'),{
      type:'bar',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true,ticks:{maxRotation:45}},y:{stacked:true,beginAtZero:true}}}
    });
    renderTable('tbl-async-inserts',rows,
      ['hour','status','flushes','total_rows','avg_flush_ms']);
  })();
});
</script>
</body>
</html>`
