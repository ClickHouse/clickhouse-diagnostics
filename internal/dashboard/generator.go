package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/alert"
	"clickhouse-diagnostic/internal/hostinfo"
	"clickhouse-diagnostic/internal/query"
	"clickhouse-diagnostic/pkg"
)

// Generator creates offline HTML diagnostic dashboards from ClickHouse data.
type Generator struct {
	client        *pkg.ClickHouseClient
	mode          string
	serverVersion internal.Version
	analysis      query.AnalysisOpts
	analysisDir   string
	hostInfo      *hostinfo.Report
	probeCache    map[string]bool // memoized hasColumn/hasTable results
}

// NewGenerator creates a new Generator.
func NewGenerator(client *pkg.ClickHouseClient, mode string) *Generator {
	return &Generator{client: client, mode: strings.ToLower(mode)}
}

// WithServerVersion records the connected server version so the
// query-analysis section resolves the same version-directory overrides
// as AnalysisCollector.Collect (see collectAnalysis). Returns the
// receiver for chaining.
func (g *Generator) WithServerVersion(v internal.Version) *Generator {
	g.serverVersion = v
	return g
}

// WithHostInfo attaches the host facts already collected for host_info.json,
// so the dashboard can show the OS/kernel/CPU/memory context beside what the
// system tables report. Nil (host-info skipped, or a non-Linux run) simply
// omits the panel. Returns the receiver for chaining.
func (g *Generator) WithHostInfo(r *hostinfo.Report) *Generator {
	g.hostInfo = r
	return g
}

// hostChecks evaluates the collected tunables against the checks CLICKHOUSE
// ITSELF makes at startup, and returns one row per check.
//
// The bar for a "warning" here is deliberately narrow: the server has to warn
// about it too. Everything in this list maps to a real check in
// programs/server/Server.cpp::sanityChecks (or, for max_map_count,
// src/Common/Exception.cpp), so the dashboard never invents a recommendation
// ClickHouse does not make.
//
// Deliberately NOT flagged: vm.swappiness. It is common tuning folklore for
// databases, but ClickHouse neither checks it at startup nor documents a
// target, so it is reported as a plain fact.
func hostChecks(r *hostinfo.Report) []map[string]interface{} {
	if r == nil {
		return nil
	}
	t := r.Tunables
	out := []map[string]interface{}{}
	add := func(name, value, status, note string) {
		// A tunable we could not read must never render as a healthy one.
		if value == "" {
			value, status = "(unreadable)", "unknown"
		}
		out = append(out, map[string]interface{}{
			"setting": name, "value": value, "status": status, "note": note,
		})
	}

	thpStatus, thpNote := "ok", `ClickHouse warns at startup when this is "always"`
	if strings.Contains(t.TransparentHugepages, "always") {
		thpStatus = "warning"
		thpNote = `ClickHouse warns at startup: transparent hugepages set to "always"`
	}
	add("transparent_hugepages", t.TransparentHugepages, thpStatus, thpNote)
	add("transparent_hugepages_defrag", t.THPDefrag, "info", "")

	ocStatus, ocNote := "ok", ""
	if strings.TrimSpace(t.OvercommitMemory) == "2" {
		ocStatus = "warning"
		ocNote = "ClickHouse warns at startup: memory overcommit is disabled (mode 2)"
	}
	add("vm.overcommit_memory", t.OvercommitMemory, ocStatus, ocNote)

	add("vm.max_map_count", t.MaxMapCount, "info",
		"ClickHouse errors when its live mappings exceed 90% of this")
	add("vm.swappiness", t.Swappiness, "info",
		"reported as a fact: ClickHouse does not check or document a target")
	add("fs.nr_open", t.FsNrOpen, "info", "")
	add("fs.file-max", t.FsFileMax, "info", "")
	add("cgroup memory limit", t.CgroupMemoryLimit, "info", "")
	add("cgroup cpu max", t.CgroupCPUMax, "info", "")
	// Join only when something was read: "" + " / " + "" trims to "/", which
	// would sneak past add()'s empty-value guard and render as healthy.
	nofile := ""
	if t.ClickHouseNofileSoft != "" || t.ClickHouseNofileHard != "" {
		nofile = strings.TrimSpace(t.ClickHouseNofileSoft + " / " + t.ClickHouseNofileHard)
	}
	add("open files (soft/hard)", nofile, "info", "")
	add("max processes (soft)", t.ClickHouseNprocSoft, "info",
		"RLIMIT_NPROC — the OS cap on tasks, not the max_threads setting")

	// The one memory check ClickHouse makes at startup.
	if r.Memory.Available && r.Memory.AvailableBytes > 0 {
		const twoGiB = 2 << 30
		status, note := "ok", ""
		if r.Memory.AvailableBytes < twoGiB {
			status = "warning"
			note = "ClickHouse warns at startup: available memory below 2 GiB"
		}
		add("available memory", formatBytes(r.Memory.AvailableBytes), status, note)
	}
	return out
}

// WithAnalysis attaches query-analysis options + the directory holding
// the .sql files. When opts is enabled, Generate adds a "Query
// Analysis" section to the dashboard, rendered from the same SQL the
// AnalysisCollector writes to disk. Returns the receiver for chaining.
func (g *Generator) WithAnalysis(opts query.AnalysisOpts, dir string) *Generator {
	g.analysis = opts
	g.analysisDir = dir
	return g
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
// Delegated to the shared template helper — see internal/query/template.go
// for the cloud-shared-tables allowlist (single source of truth).
func (g *Generator) sysTable(table string) string {
	return query.SysTable(g.mode, table)
}

// parseUInt64 extracts an integer from a JSONCompact cell. ClickHouse
// serialises UInt64/Int64 as JSON strings by default
// (output_format_json_quote_64bit_integers=1), so a direct unmarshal into
// float64 fails silently. This helper accepts both forms.
func parseUInt64(raw json.RawMessage) int64 {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	var n float64
	_ = json.Unmarshal(raw, &n)
	return int64(n)
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

// scalarCount runs sql (expected to return a single count()) and returns
// the value. count() is UInt64, JSON-rendered as a quoted string, so both
// string and numeric forms are parsed.
func (g *Generator) scalarCount(sql string) (int64, error) {
	r, err := g.execJSON(sql)
	if err != nil {
		return 0, err
	}
	if r == nil || r.Rows == 0 || len(r.Data) == 0 || len(r.Data[0]) == 0 {
		return 0, nil
	}
	var s string
	if json.Unmarshal(r.Data[0][0], &s) == nil {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n, nil
	}
	var f float64
	if json.Unmarshal(r.Data[0][0], &f) == nil {
		return int64(f), nil
	}
	return 0, nil
}

// probe runs a schema-existence count query (memoized by key) and reports
// whether the object exists.
//
// On a probe ERROR it fails OPEN — assumes the object is present and prints
// a [dashboard] warning. A transient probe failure must not silently
// downgrade the dashboard to its oldest-server shape (this tool exists to
// diagnose; a silent downgrade would be the wrong default). If the object
// really is absent, the subsequent full query fails through safeQuery,
// which surfaces its own visible warning. A genuine zero count means
// "absent" and selects the fallback.
func (g *Generator) probe(key, sql string) bool {
	// No client to probe with (e.g. unit tests building SQL in isolation):
	// assume present so we emit the full, modern query.
	if g.client == nil {
		return true
	}
	if g.probeCache == nil {
		g.probeCache = map[string]bool{}
	}
	if v, ok := g.probeCache[key]; ok {
		return v
	}
	n, err := g.scalarCount(sql)
	if err != nil {
		fmt.Printf("  [dashboard] schema probe %q failed: %v (assuming present)\n", key, err)
		g.probeCache[key] = true
		return true
	}
	present := n > 0
	g.probeCache[key] = present
	return present
}

// hasColumn / hasTable let the dashboard's live queries stay compatible
// across the supported version range (ClickHouse 22.8+) without
// hard-coding per-column "added-in" versions. The dashboard builds its
// SQL dynamically (unlike the .sql query files, which use the
// version-directory mechanism), so a runtime schema check is both
// simpler and more robust — it also covers optional tables that may be
// disabled by config on any version. Schema is identical across
// replicas, so these check the local system tables directly.
func (g *Generator) hasColumn(table, column string) bool {
	// Guard against a privilege-blind probe. ClickHouse filters
	// system.columns by grant SILENTLY — a user without SELECT on
	// system.<table> gets zero rows, not ACCESS_DENIED. That reads as
	// "column absent" and would degrade every gated panel to its
	// oldest-server shape on a modern server, with no warning (observed
	// on a Cloud 26.2 service with only database-level grants).
	//
	// A table that really exists always has columns, so "I can't see ANY
	// column of this table" means the probe cannot answer — fail open.
	if !g.probeCanSeeColumns(table) {
		return true
	}
	return g.probe(table+"."+column, fmt.Sprintf(
		"SELECT count() FROM system.columns WHERE database='system' AND table='%s' AND name='%s'",
		table, column))
}

// probeCanSeeColumns reports whether system.columns exposes any column of
// the given system table to the current user. Warns once per table when it
// cannot, since every hasColumn() answer for that table is then a guess.
func (g *Generator) probeCanSeeColumns(table string) bool {
	key := "visible:" + table
	if g.probeCache != nil {
		if v, ok := g.probeCache[key]; ok {
			return v
		}
	}
	visible := g.probe(key, fmt.Sprintf(
		"SELECT count() FROM system.columns WHERE database='system' AND table='%s'", table))
	if !visible {
		// Don't attribute the cause: this probe reads the LOCAL
		// system.columns, so zero rows can mean a missing SELECT grant OR —
		// in cloud — a table that only exists on a remote replica. Both lead
		// to the same fail-open, so state the effect and let the operator
		// judge the cause.
		fmt.Printf("  [dashboard] cannot inspect columns of system.%s locally "+
			"— assuming modern columns are present\n", table)
	}
	return visible
}

func (g *Generator) hasTable(table string) bool {
	// Unlike column schema (identical across replicas), a table's
	// *existence* can be per-replica — system.crash_log only materialises
	// on the node that crashed. In cloud mode we probe every replica so a
	// crash on a non-serving node is at least DETECTED (the probe returns
	// true and the panel query runs).
	//
	// Honest limit: in that partial-existence case the guarded panel
	// query — clusterAllReplicas(default, system.<table>) — still raises
	// UNKNOWN_TABLE on the replicas that lack the table, so the outcome
	// is a visible [dashboard] error rather than the crashed node's rows.
	// That is the intended "surface, don't hide" tradeoff: rendering the
	// rows would need a fault-tolerant per-node fan-out.
	//
	// (system.tables is listed in SharedSystemTables — see
	// internal/query/template.go — because its user-table rows are the
	// same everywhere; that's about row contents, not the existence of
	// per-node system log tables, which is what we probe here.)
	tablesRef := "system.tables"
	if g.mode == "cloud" {
		tablesRef = "clusterAllReplicas(default, system.tables)"
	}
	// Same privilege-blindness guard as hasColumn: system.tables is
	// filtered by grant without erroring, and a server always has SOME
	// system tables — so zero rows means the probe can't answer. Fail open
	// and let the panel query surface its own error.
	//
	// Warn only on the first table, not once per guarded panel: the probe is
	// memoized but a bare Printf after it would repeat the same line for
	// crash_log, asynchronous_insert_log, …
	const visKey = "visible:system-tables"
	_, alreadyProbed := g.probeCache[visKey]
	if !g.probe(visKey, fmt.Sprintf(
		"SELECT count() FROM %s WHERE database='system'", tablesRef)) {
		if !alreadyProbed {
			fmt.Printf("  [dashboard] cannot read %s — assuming the system log "+
				"tables exist (check SELECT grants if panels look empty)\n", tablesRef)
		}
		return true
	}
	return g.probe("table:"+table, fmt.Sprintf(
		"SELECT count() FROM %s WHERE database='system' AND name='%s'", tablesRef, table))
}

// Generate produces dashboard.html inside outputDir.
// alertResults may be nil or empty if alert evaluation was skipped.
func (g *Generator) Generate(outputDir string, alertResults []alert.Result) error {
	fmt.Println("\nGenerating HTML dashboard...")
	payload := g.collect()
	// Needs outputDir, so it happens here rather than in collect().
	payload["bundle_files"] = collectBundleFiles(outputDir)
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

// tablesListSQL builds the all-tables query (with size). system.tables and
// system.parts are shared across replicas, so the same query works for
// cloud, onprem, and gov.
func (g *Generator) tablesListSQL() string {
	notSystem := "database NOT IN ('system','information_schema','INFORMATION_SCHEMA')"
	return fmt.Sprintf(`
		SELECT t.database, t.name AS table_name, t.engine,
			coalesce(p.parts, 0) AS parts,
			-- total_rows is a ROW COUNT, not a byte size: formatReadableSize would
			-- render 1,992,000 rows as "1.90 MiB". formatReadableQuantity exists
			-- well below the 22.8 floor.
			formatReadableQuantity(coalesce(p.total_rows, 0)) AS total_rows,
			formatReadableSize(coalesce(p.bytes_on_disk, 0)) AS size,
			coalesce(p.bytes_on_disk, 0) AS bytes_on_disk,
			t.partition_key, t.sorting_key, t.storage_policy
		FROM %s AS t
		LEFT JOIN (
			SELECT database, table, count() AS parts,
				sum(rows) AS total_rows, sum(bytes_on_disk) AS bytes_on_disk
			FROM %s WHERE active = 1
			GROUP BY database, table
		) AS p ON t.database = p.database AND t.name = p.table
		WHERE t.%s
		ORDER BY bytes_on_disk DESC
		LIMIT 2000`, g.sysTable("tables"), g.sysTable("parts"), notSystem)
}

// sampleQueryCol returns the projection for a representative SQL text per
// normalized_query_hash — or a redaction stub in gov mode.
//
// Every row in one of these groups shares a normalized_query_hash, i.e. the
// same query SHAPE, so any(query) is a genuine example of the group rather
// than an arbitrary mix; only the literals belong to whichever execution was
// picked. It is truncated because a pathological query can be megabytes and
// this text is embedded directly in the HTML.
//
// Gov mode gets a stub: queries.gov/*.sql hashes database and table names, and
// query_log.query is the raw SQL the customer ran against those same names, so
// shipping it here would undo the hashing.
func (g *Generator) sampleQueryCol() string {
	if g.mode == "gov" {
		return "'' AS sample_query"
	}
	return "left(any(query), 600) AS sample_query"
}

// querySlowSQL builds the slowest-query-patterns panel.
//
// hash is the FULL normalized_query_hash, as a string. It is a UInt64, so it
// must never reach the page as a JSON number: JavaScript rounds past 2^53 and
// would silently corrupt the identifier the reader is meant to copy.
func (g *Generator) querySlowSQL() string {
	// Same window contract as queryByUserSQL: exception rows must be IN the
	// window or countIf(exception_code != 0) is structurally always zero —
	// failures log as ExceptionWhileProcessing/ExceptionBeforeStart, never
	// QueryFinish. Cost aggregates stay QueryFinish-only (sum/greatest rather
	// than avgIf, so an all-failure shape yields 0, not a JSON-hostile nan).
	return fmt.Sprintf(
		`SELECT toString(normalized_query_hash) AS hash,
				query_kind, user,
				countIf(type = 'QueryFinish') AS executions,
				round(sumIf(query_duration_ms, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1), 0) AS avg_duration_ms,
				maxIf(query_duration_ms, type = 'QueryFinish') AS max_duration_ms,
				round(sumIf(read_bytes, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1) / 1048576, 2) AS avg_read_mb,
				round(sumIf(memory_usage, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1) / 1048576, 2) AS avg_memory_mb,
				countIf(exception_code != 0) AS errors,
				%s
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND ((type = 'QueryFinish' AND query_duration_ms > 0)
				OR type IN ('ExceptionWhileProcessing', 'ExceptionBeforeStart'))
		 GROUP BY hash, query_kind, user
		 ORDER BY avg_duration_ms DESC LIMIT 20`,
		g.sampleQueryCol(), g.sysTable("query_log"),
	)
}

// queryHeavySQL builds the heaviest-reads panel. Same full-hash and
// sample-query contract as querySlowSQL.
func (g *Generator) queryHeavySQL() string {
	return fmt.Sprintf(
		`SELECT toString(normalized_query_hash) AS hash,
				query_kind, user,
				count() AS executions,
				round(avg(read_bytes)/1048576, 2) AS avg_read_mb,
				formatReadableSize(sum(read_bytes)) AS total_read,
				round(avg(query_duration_ms), 0) AS avg_duration_ms,
				%s
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND type = 'QueryFinish'
		 GROUP BY hash, query_kind, user
		 ORDER BY avg_read_mb DESC LIMIT 20`,
		g.sampleQueryCol(), g.sysTable("query_log"),
	)
}

// textLogRowCap bounds what the Logs panel embeds.
//
// This number is the whole reason the panel is safe. The raw server logs are
// tail-copied into the bundle at up to 50 MiB PER FILE (--logs-max-mb), and
// inlining that into a self-contained HTML would produce a document the
// browser must parse as one string literal before it paints anything. So the
// panel embeds bounded, structured, already-triaged rows from system.text_log
// and the Collected Files panel links to the raw files.
const textLogRowCap = 1000

// textLogMessageCap trims each message. system.text_log messages are unbounded
// (stack traces, whole queries); at the row cap an untrimmed column is the
// difference between a ~350 KiB payload and a multi-megabyte one.
const textLogMessageCap = 300

// textLogSQL builds the Logs panel query: recent Warning-and-worse only.
//
// Info/Debug/Trace are deliberately excluded. They are the bulk of the volume
// and the reason a naive log panel is unusable; anyone who needs them wants the
// raw file, which is linked under Collected Files.
func (g *Generator) textLogSQL() string {
	host := ""
	if g.hasColumn("text_log", "hostname") {
		host = "hostname,"
	}
	// Filter and slice in a SUBQUERY, format in the outer SELECT.
	//
	// The analyzer resolves WHERE-clause identifiers against SELECT aliases, so
	// a flat `SELECT toString(event_time) AS event_time ... WHERE event_time >
	// now() - INTERVAL 24 HOUR` compares String to DateTime and dies with
	// NO_COMMON_TYPE (code 386) — verified against 26.4.5.143. That is the same
	// trap already documented for exception_code in the exceptions panel; here
	// the inner query keeps event_time a DateTime so the predicate is typed,
	// and only the outer projection stringifies it.
	//
	// Ordering the outer query by the stringified value is still chronological:
	// 'YYYY-MM-DD hh:mm:ss' sorts lexicographically in time order.
	//
	// leftUTF8 rather than left: left() counts bytes, so truncating a message
	// mid-character leaves invalid UTF-8. json.Marshal masks that as U+FFFD
	// here rather than breaking the page, but the archived collectors write
	// the server's bytes straight to disk and cannot afford it, so both use
	// the same function.
	return fmt.Sprintf(
		`SELECT toString(event_time) AS event_time,
				%s level, logger_name,
				leftUTF8(message, %d) AS message
		 FROM (
			SELECT event_time, %s level, logger_name, message
			FROM %s
			WHERE event_time > now() - INTERVAL 24 HOUR
			  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
			ORDER BY event_time DESC
			LIMIT %d
		 )
		 ORDER BY event_time DESC`,
		host, textLogMessageCap, host, g.sysTable("text_log"), textLogRowCap,
	)
}

// collectBundleFiles indexes every file the run wrote next to dashboard.html,
// so the dashboard can point at the rest of the bundle instead of swallowing
// it. Nothing here is embedded: the raw server logs alone are tail-copied at up
// to 50 MiB per file (--logs-max-mb), and inlining that would produce a
// document the browser cannot open.
//
// Paths are bundle-relative, so the links resolve from the extracted archive
// and simply 404 if someone mails dashboard.html on its own — which is why the
// panel says the files live beside it.
func collectBundleFiles(outputDir string) []map[string]interface{} {
	out := []map[string]interface{}{}
	err := filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal
		}
		rel, relErr := filepath.Rel(outputDir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// The dashboard does not list itself, and OS turds are not artifacts.
		if rel == "dashboard.html" || strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		dir := path0(rel)
		out = append(out, map[string]interface{}{
			"file":  rel,
			"group": dir,
			"kind":  strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), "."),
			"size":  formatBytes(info.Size()),
			"bytes": info.Size(),
			"href":  rel,
		})
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		gi, gj := out[i]["group"].(string), out[j]["group"].(string)
		if gi != gj {
			return gi < gj
		}
		return out[i]["file"].(string) < out[j]["file"].(string)
	})
	return out
}

// path0 returns the top-level folder of a bundle-relative path, or "" for a
// file sitting at the bundle root.
func path0(rel string) string {
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

// formatBytes renders a byte count in the largest sensible unit.
func formatBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for n := b / u; n >= u && exp < 3; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGT"[exp])
}

// topTablesSQL builds the largest-tables panel.
//
// total_rows uses formatReadableQuantity, NOT formatReadableSize: the latter is
// the byte formatter and renders 1,992,000 rows as "1.90 MiB".
func (g *Generator) topTablesSQL() string {
	return fmt.Sprintf(
		`SELECT database, table, count() AS parts,
			formatReadableQuantity(sum(rows)) AS total_rows,
			formatReadableSize(sum(bytes_on_disk)) AS compressed_size,
			round(if(sum(data_compressed_bytes)>0,
				sum(data_uncompressed_bytes)/sum(data_compressed_bytes), 0), 2) AS compression_ratio
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database, table
		 ORDER BY sum(bytes_on_disk) DESC LIMIT 20`,
		g.sysTable("parts"),
	)
}

// queryByUserSQL builds the per-user activity summary.
//
// The window must include the exception types, not just QueryFinish: a failed
// query is logged as ExceptionWhileProcessing (threw during execution) or
// ExceptionBeforeStart (threw during parsing/analysis, e.g. UNKNOWN_TABLE) and
// NEVER as QueryFinish. Scoping the whole query to QueryFinish made
// countIf(exception_code != 0) structurally always 0, so the panel's error
// column — and the alert-row highlight keyed on it — could never fire.
//
// executions and the cost aggregates stay QueryFinish-only so their meaning is
// unchanged. Average duration is sum/count rather than avgIf so a user with
// only failures yields 0 instead of a JSON-hostile nan.
func (g *Generator) queryByUserSQL() string {
	return fmt.Sprintf(
		`SELECT user,
				countIf(type = 'QueryFinish') AS executions,
				round(sumIf(query_duration_ms, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1), 0) AS avg_duration_ms,
				round(sumIf(read_bytes, type = 'QueryFinish')/1073741824, 3) AS total_read_gb,
				round(sumIf(memory_usage, type = 'QueryFinish')/1073741824, 3) AS total_memory_gb,
				countIf(exception_code != 0) AS error_count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND type IN ('QueryFinish', 'ExceptionWhileProcessing', 'ExceptionBeforeStart')
		 GROUP BY user ORDER BY executions DESC LIMIT 20`,
		g.sysTable("query_log"),
	)
}

// dictionariesSQL builds the dictionaries query, omitting last_exception
// for gov. In cloud mode the query goes through clusterAllReplicas so
// we get one row per (dictionary, replica) — dictionary RUNTIME state
// (status, bytes_allocated, hit_rate, query_count) is per-pod even
// though the definition is shared via Keeper, and a dict that's been
// queried on pod A but not on pod B will appear LOADED on A and
// NOT_LOADED on B. hostname() is evaluated remotely on each replica so
// we can label each row with the pod that produced it.
//
// Column choice tracks the system.dictionaries reference:
//
//	https://clickhouse.com/docs/operations/system-tables/dictionaries
//
// In gov mode we additionally redact `source`, `origin`, `comment`
// and `last_exception` — these can carry connection strings, file
// paths, exception text and user comments that reveal schema or
// infrastructure details that gov mode is otherwise hashing.
//
// NOTE: currently unreachable — cmd/main.go refuses to generate the
// dashboard in gov mode at all, because the other ~20 panels select raw
// identifiers this redaction doesn't cover. Kept (like the gov branches
// in the async panel and collectAnalysis) for the hashed-gov-dashboard
// follow-up that would restore it; it is NOT a live guarantee today.
func (g *Generator) dictionariesSQL() string {
	exceptionCol, sourceCol, originCol, commentCol := "last_exception", "source", "origin", "comment"
	if g.mode == "gov" {
		exceptionCol = "'' AS last_exception"
		sourceCol = "'' AS source"
		originCol = "'' AS origin"
		commentCol = "'' AS comment"
	}
	// error_count was added to system.dictionaries after 22.8; fall back
	// to 0 so the dashboard column still renders on older servers.
	errorCountCol := "error_count"
	if !g.hasColumn("dictionaries", "error_count") {
		errorCountCol = "0 AS error_count"
	}
	return fmt.Sprintf(`
		SELECT
			hostname()                                          AS hostname,
			database, name, status, type, toString(uuid)        AS uuid,
			bytes_allocated,
			formatReadableSize(bytes_allocated)                 AS bytes_allocated_human,
			element_count, query_count, %s,
			round(hit_rate * 100, 2)                            AS hit_rate_pct,
			round(found_rate * 100, 2)                          AS found_rate_pct,
			round(load_factor, 4)                               AS load_factor,
			arrayStringConcat(key.names, ', ')                  AS key_names,
			arrayStringConcat(key.types, ', ')                  AS key_types,
			arrayStringConcat(attribute.names, ', ')            AS attribute_names,
			arrayStringConcat(attribute.types, ', ')            AS attribute_types,
			lifetime_min, lifetime_max,
			toString(loading_start_time)                        AS loading_start_time,
			toString(last_successful_update_time)               AS last_update,
			round(loading_duration, 2)                          AS loading_duration_s,
			%s, %s, %s, %s
		FROM %s
		ORDER BY database, name, hostname`,
		errorCountCol, sourceCol, originCol, commentCol, exceptionCol, g.sysTable("dictionaries"))
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
		p["total_databases"] = parseUInt64(r.Data[0][0])
		p["total_tables"] = parseUInt64(r.Data[0][1])
	}

	// active parts summary
	if r, err := g.execJSON(fmt.Sprintf(
		`SELECT count() AS parts, formatReadableSize(sum(bytes_on_disk)) AS sz
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')`,
		g.sysTable("parts"),
	)); err == nil && r.Rows > 0 {
		p["active_parts"] = parseUInt64(r.Data[0][0])
		var sz string
		_ = json.Unmarshal(r.Data[0][1], &sz)
		p["total_size"] = sz
	}

	// ── Storage ──────────────────────────────────────────────────────────────

	// Compute each aggregate once and reference its alias in derived columns:
	// CH 25.12's analyzer rejects repeated sum(bytes_on_disk) in the same
	// SELECT — the second occurrence is treated as a nested aggregate,
	// raising ILLEGAL_AGGREGATION.
	p["storage_by_db"] = g.safeQuery("storage_by_db", fmt.Sprintf(
		`SELECT database, count() AS parts, sum(rows) AS rows,
			sum(bytes_on_disk) AS bytes_total,
			formatReadableSize(bytes_total) AS size_human,
			round(if(sum(data_compressed_bytes)>0,
				sum(data_uncompressed_bytes)/sum(data_compressed_bytes), 0), 2) AS compression_ratio
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database ORDER BY bytes_total DESC LIMIT 20`,
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

	// Host facts, straight from the same Report that becomes host_info.json —
	// round-tripped through JSON so the dashboard shows exactly the shape the
	// file does rather than a second, drifting projection of it.
	if g.hostInfo != nil {
		if raw, err := json.Marshal(g.hostInfo); err == nil {
			var hi map[string]interface{}
			if err := json.Unmarshal(raw, &hi); err == nil {
				p["host_info"] = hi
			}
		}
		p["host_checks"] = hostChecks(g.hostInfo)
	}

	// Logs: bounded Warning+ rows only. text_log is opt-in (it is off by
	// default in OSS), so an absent table simply hides the panel.
	if g.hasTable("text_log") {
		p["text_log"] = g.safeQuery("text_log", g.textLogSQL())
		p["text_log_row_cap"] = textLogRowCap
	}

	p["query_slow"] = g.safeQuery("query_slow", g.querySlowSQL())
	p["query_heavy"] = g.safeQuery("query_heavy", g.queryHeavySQL())

	p["query_by_user"] = g.safeQuery("query_by_user", g.queryByUserSQL())

	// exceptions — return exception_code as the raw integer. In CH 25.12 the
	// analyzer resolves WHERE-clause identifiers against SELECT aliases, so
	// aliasing toString(exception_code) AS exception_code makes
	// `WHERE exception_code != 0` compare a String to a UInt8 → NO_COMMON_TYPE.
	p["exceptions"] = g.safeQuery("exceptions", fmt.Sprintf(
		`SELECT exception_code,
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

	// system.crash_log only exists once the server has recorded a crash,
	// so it is absent on a healthy instance of any version — skip the
	// panel rather than emit a spurious "table doesn't exist" warning.
	if g.hasTable("crash_log") {
		p["crash_log"] = g.safeQuery("crash_log", fmt.Sprintf(
			`SELECT toString(event_time) AS event_time,
					signal, toString(thread_id) AS thread_id,
					query_id, version
			 FROM %s ORDER BY event_time DESC LIMIT 100`,
			g.sysTable("crash_log"),
		))
	} else {
		p["crash_log"] = []map[string]interface{}{}
	}

	// ── Pending work ──────────────────────────────────────────────────────────

	p["top_tables"] = g.safeQuery("top_tables", g.topTablesSQL())

	// is_killed was added to system.mutations after 22.8; only filter on
	// it when present, otherwise show all in-progress mutations.
	killedFilter := ""
	if g.hasColumn("mutations", "is_killed") {
		killedFilter = "AND is_killed = 0"
	}
	p["mutations"] = g.safeQuery("mutations", fmt.Sprintf(
		`SELECT database, table, mutation_id, command,
				toString(create_time) AS create_time, parts_to_do
		 FROM %s
		 WHERE parts_to_do > 0 %s
		 ORDER BY parts_to_do DESC LIMIT 20`,
		g.sysTable("mutations"), killedFilter,
	))

	// bytes_on_disk was added to system.detached_parts in 22.11; fall
	// back to a count-only view (no size) on older servers.
	detachedSize, detachedOrder := "formatReadableSize(sum(bytes_on_disk)) AS size", "sum(bytes_on_disk)"
	if !g.hasColumn("detached_parts", "bytes_on_disk") {
		detachedSize, detachedOrder = "'n/a' AS size", "count()"
	}
	p["detached"] = g.safeQuery("detached", fmt.Sprintf(
		`SELECT database, table, count() AS count,
				%s,
				arrayStringConcat(groupUniqArray(reason), ', ') AS reasons
		 FROM %s
		 GROUP BY database, table
		 ORDER BY %s DESC LIMIT 20`,
		detachedSize, g.sysTable("detached_parts"), detachedOrder,
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

	// system.disks is per-replica, so in cloud mode this fans out and every
	// replica contributes its own (largely identical) disk list — measured
	// 11 disks → 33 rows on a 3-replica service. Without a host label those
	// read as duplicates. hostName() is evaluated on the replica that
	// produced the row, exactly as dictionariesSQL does.
	p["disks"] = g.safeQuery("disks", fmt.Sprintf(
		`SELECT hostName() AS hostname,
			 name, path, type,
			 free_space, total_space,
			 formatReadableSize(free_space)       AS free_space_human,
			 formatReadableSize(total_space)      AS total_space_human,
			 if(total_space > 0,
			    round(free_space / total_space * 100, 1),
			    0) AS free_pct
		 FROM %s
		 ORDER BY hostname, total_space DESC`,
		g.sysTable("disks"),
	))

	// ── Server errors (cumulative error counters) ──────────────────────────────

	// system.errors holds PER-REPLICA counters, so hard-coding system.errors
	// here showed only the serving node's errors and hid the other replicas'
	// entirely (measured on a 3-replica Cloud service). Fan out via
	// sysTable, then aggregate to cluster-wide totals: this panel renders as
	// one bar per error, so emitting a row per replica would produce
	// duplicate same-labelled bars instead of a single meaningful count.
	p["server_errors"] = g.safeQuery("server_errors", fmt.Sprintf(
		// Aggregate under a NON-colliding alias in a subquery, then rename to
		// `value` outside. Aliasing sum(value) AS value in the same SELECT
		// shadows the source column, and the analyzer then resolves the bare
		// `value` inside argMax()/WHERE against the aggregate →
		// ILLEGAL_AGGREGATION. Same subquery trick as
		// alerts/disk_space_low.yaml.
		`SELECT name, code, total AS value, last_error_time, last_error_message
		 FROM (
			 SELECT name, code,
				 sum(value)                                   AS total,
				 toString(max(last_error_time))               AS last_error_time,
				 left(argMax(last_error_message, value), 300) AS last_error_message
			 FROM %s
			 GROUP BY name, code
			 HAVING total > 0
		 )
		 ORDER BY value DESC LIMIT 30`,
		g.sysTable("errors"),
	))

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

	// system.asynchronous_insert_log was added in 22.10; skip the panel
	// on older servers where the table doesn't exist.
	if g.mode != "gov" && g.hasTable("asynchronous_insert_log") {
		asyncTable := "system.asynchronous_insert_log"
		if g.mode == "cloud" {
			asyncTable = "clusterAllReplicas(default, merge(system, '^asynchronous_insert_log'))"
		}
		// total_bytes exists since the table's inception (22.10); the `rows`
		// column was added in 23.4 (absent on 22.10–23.3). Always report
		// bytes and add rows when available, so no rendered column is ever
		// blank (the front-end shows both); 'n/a' reads more honestly than a
		// literal 0 when the column is unavailable.
		rowsCol := "'n/a' AS total_rows"
		if g.hasColumn("asynchronous_insert_log", "rows") {
			rowsCol = "toString(sum(rows)) AS total_rows"
		}
		// Queue→flush latency in ms. We avoid dateDiff('millisecond', …):
		// the sub-second unit errors on older servers (22.12: BAD_ARGUMENTS).
		// event_time_microseconds / flush_time_microseconds are DateTime64(6)
		// and exist since 22.10, so subtracting them as floats gives true
		// sub-second latency across the whole supported range.
		p["async_inserts"] = g.safeQuery("async_inserts", fmt.Sprintf(
			`SELECT toString(toStartOfHour(event_time)) AS hour,
				 status, count() AS flushes,
				 %s,
				 formatReadableSize(sum(bytes)) AS total_bytes,
				 round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0) AS avg_flush_ms
			 FROM %s
			 WHERE event_time > now() - INTERVAL 24 HOUR
			 GROUP BY hour, status ORDER BY hour`,
			rowsCol, asyncTable,
		))
	} else {
		p["async_inserts"] = []map[string]interface{}{}
	}

	// ── Query analysis (optional) ──────────────────────────────────────────
	g.collectAnalysis(p)

	return p
}

// collectAnalysis runs the query-analysis SQL files (same set the
// AnalysisCollector writes to disk), embedding the JSONCompact output
// into the dashboard payload so the front-end can render the "Query
// Analysis" section. No-op when WithAnalysis was not called or when
// opts.Enabled() is false.
//
// Each file's content is template-substituted in memory, the trailing
// `FORMAT Native` clause stripped, then sent to ClickHouse with
// `FORMAT JSONCompact` (via safeQuery). If a file still has unbound
// placeholders after substitution (e.g. single-id files when only
// --normalized-query-hash was given), it is silently skipped — the
// missing key is then absent from the payload and the front-end hides
// the corresponding card.
func (g *Generator) collectAnalysis(p map[string]interface{}) {
	if !g.analysis.Enabled() || g.analysisDir == "" {
		return
	}
	// A zero server version sorts below every version directory, so
	// FindVersionedFiles would return only root files and the dashboard
	// would run the 22.8 baseline on a modern server (the round-1
	// regression). cmd/main.go always calls WithServerVersion after a
	// successful version probe, so this only trips if a future caller
	// forgets — warn loudly rather than silently degrade.
	if g.serverVersion == (internal.Version{}) {
		fmt.Println("  [dashboard] warning: server version unknown; query-analysis panels fall back to the oldest (root) query variants")
	}
	vars := query.Vars{
		Mode:                g.mode,
		QueryID:             g.analysis.QueryID,
		NormalizedQueryHash: g.analysis.NormalizedQueryHash,
		From:                g.analysis.From,
		To:                  g.analysis.To,
	}

	// Map of payload key → .sql filename. Keys match the JS fetches
	// in the dashboard template (DATA.qa_*).
	files := map[string]string{
		"qa_details":          "query_details.sql",
		"qa_profile":          "profile_events.sql",
		"qa_text_parts":       "text_log_parts.sql",
		"qa_text_full":        "text_log_full.sql",
		"qa_tables":           "tables_for_query.sql",
		"qa_fast_slow":        "fast_slow_query_ids.sql",
		"qa_pe_compare":       "profile_events_compare.sql",
		"qa_by_host":          "hash_by_host.sql",
		"qa_summary":          "hash_summary.sql",
		"qa_failed_over_time": "failed_over_time.sql",
		"qa_failed":           "failed_queries.sql",
		"qa_executions":       "executions_timeline.sql",
	}

	// Resolve version-directory overrides exactly as AnalysisCollector
	// does, so the dashboard renders the SAME variant that the archive
	// writes to disk. Reading the root file directly would run the
	// downgraded 22.8 baseline on modern servers. (On a resolver ERROR
	// the two paths intentionally differ: Collect aborts the archive
	// bundle, while the dashboard warns and degrades to root files so
	// the section isn't entirely blank.)
	resolved := map[string]string{} // base name → full path
	vf, err := query.FindVersionedFiles(g.analysisDir, g.serverVersion, ".sql")
	if err != nil {
		// Don't silently blank the whole Query Analysis section on a
		// resolver error (unreadable dir, broken symlink, …): warn and
		// fall back to the root files below.
		fmt.Printf("  [dashboard] query-analysis file resolution failed: %v (falling back to root files)\n", err)
	}
	for _, f := range vf {
		resolved[f.Name] = f.FullPath
	}
	for key, fname := range files {
		path, ok := resolved[fname]
		if !ok {
			// Fall back to the root file. For a version-dir-only file
			// gated above this server there is no root, so os.ReadFile
			// fails below and we skip it — matching Collect().
			path = filepath.Join(g.analysisDir, fname)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			// A resolved path failing to read (deleted/permissions race)
			// is diagnostic-worthy — log like Collect does. A version-dir-
			// only file below its gate simply has no root path and lands
			// here silently by design.
			if ok {
				fmt.Printf("  [dashboard] %s: read %s: %v\n", key, path, err)
			}
			continue
		}
		sql := query.Apply(string(raw), vars)
		if len(query.UnboundPlaceholders(sql)) > 0 {
			continue
		}
		// Strip trailing `FORMAT Native` so safeQuery can append
		// JSONCompact via the standard execJSON path.
		sql = stripTrailingFormat(sql)
		if err := query.ValidateQueryContent(sql); err != nil {
			fmt.Printf("  [dashboard] %s: blocked analysis query in %s: %v\n", key, fname, err)
			continue
		}
		rows := g.safeQuery(key, sql)
		// Defence in depth for gov mode: the dashboard JSON is part
		// of the support archive, so any field that the JS would
		// otherwise hide should also be cleared from the embedded
		// payload. The `query` and `exception` columns in
		// system.query_log contain raw text the customer ran
		// (referencing the same table names gov-mode hashes
		// elsewhere); empty them out before embedding.
		if g.mode == "gov" {
			for _, r := range rows {
				for _, sensitive := range []string{"query", "exception", "sample_query", "sample_exception"} {
					if _, ok := r[sensitive]; ok {
						r[sensitive] = "(redacted in gov mode)"
					}
				}
			}
		}
		p[key] = rows
	}

	p["qa_enabled"] = true
	p["qa_query_id"] = g.analysis.QueryID
	p["qa_hash"] = g.analysis.NormalizedQueryHash
	p["qa_from"] = g.analysis.From.UTC().Format(time.RFC3339)
	p["qa_to"] = g.analysis.To.UTC().Format(time.RFC3339)
	// qa_mode gates the rendering of the focus query's SQL text: in
	// gov mode we still hash database/table names in queries.gov/*.sql,
	// but query_log.query stores the raw SQL the customer ran, which
	// references the same names. Exposing it in the dashboard would
	// defeat the hashing. JS hides the card when mode == 'gov'.
	p["qa_mode"] = g.mode
}

// stripTrailingFormat removes a trailing `FORMAT <name>` clause (and
// any whitespace / semicolons after it). The dashboard always wants
// JSONCompact, which the underlying execJSON wrapper appends.
func stripTrailingFormat(sql string) string {
	t := strings.TrimRight(sql, " \t\r\n;")
	idx := strings.LastIndex(strings.ToUpper(t), "FORMAT")
	if idx == -1 {
		return t
	}
	tail := strings.TrimSpace(t[idx:])
	parts := strings.Fields(tail)
	if len(parts) == 2 && strings.EqualFold(parts[0], "FORMAT") {
		return strings.TrimRight(t[:idx], " \t\r\n")
	}
	return t
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
<script>/* stamp the saved theme before first paint so the page never flashes */
try{var _t=localStorage.getItem("chdiag-theme");if(_t)document.documentElement.setAttribute("data-cui-theme",_t);}catch(e){}
</script>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"></script>
<style>
/* ─────────────────────────────────────────────────────────────────────────────
   Click UI token layer.

   Click UI ships as a React library (@clickhouse/click-ui + ClickUIProvider),
   which this report cannot use: it is one self-contained HTML file opened
   offline from a tarball, with no build step. The design system's documented
   escape hatch for exactly that case is its token layer — CSS custom
   properties, themed via data-cui-theme on <html>. So the values below are
   vendored verbatim from ClickHouse/click-ui tokens/themes/{primitives,light,
   dark}.json; every hex is a real token, none is eyeballed.

   Surfaces are chosen so charts sit on #ffffff (light) / #1F1F1C (dark) — the
   two surfaces the categorical palette in the script below was validated
   against. Do not repoint --surface-card without re-running that validation.
   ───────────────────────────────────────────────────────────────────────── */
:root{
  color-scheme:light;
  /* primitives actually referenced */
  --click-palette-neutral-0:#ffffff;
  --click-palette-neutral-750:#1F1F1C;
  --click-palette-neutral-725:#282828;
  --click-palette-neutral-900:#151515;
  --click-palette-slate-50:#f6f7fa;
  --click-palette-slate-100:#e6e7e9;
  --click-palette-slate-600:#696e79;
  --click-palette-slate-900:#161517;
  --click-palette-brand-300:#FAFF69;

  /* global semantic tokens (light.json) */
  --click-global-color-background-default:#ffffff;
  --click-global-color-background-muted:#f6f7fa;
  --click-global-color-text-default:#161517;
  --click-global-color-text-muted:#696e79;
  --click-global-color-stroke-default:#e6e7e9;
  --click-global-color-accent-default:#151515;

  /* report-level surface roles, expressed in tokens */
  --surface-page:var(--click-global-color-background-muted);
  --surface-card:var(--click-global-color-background-default);
  --surface-sunken:var(--click-palette-slate-50);
  --surface-hover:var(--click-palette-slate-50);
  --ink:var(--click-global-color-text-default);
  --ink-muted:var(--click-global-color-text-muted);
  --stroke:var(--click-global-color-stroke-default);

  /* chrome that stays dark in both themes (ClickHouse product header) */
  --header-bg:var(--click-palette-neutral-900);
  --header-ink:var(--click-palette-neutral-0);
  --header-logo:var(--click-palette-brand-300);

  /* status ramp — semantic tokens, reserved meaning, never used for a series */
  --status-critical:#f10000;        /* danger.500  */
  --status-warning:#F55A00;         /* warning.500 */
  --status-good:#008A0B;            /* success.700 */
  --status-info:#1D64EC;            /* info.500    */
  --status-neutral:#696e79;         /* slate.600 — "the rule itself errored" */
  --status-critical-bg:#ffdddd;     /* danger.50   */
  --status-warning-bg:#FFE2D1;      /* warning.50  */
  --status-good-bg:#E5FFE8;         /* success.50  */
  --status-info-bg:#E7EFFD;         /* info.50     */
  --status-neutral-bg:#f6f7fa;      /* slate.50    */
  --status-critical-ink:#910000;    /* danger.700  */
  --status-warning-ink:#A33C00;     /* warning.700 */
  --status-good-ink:#008A0B;        /* success.700 */
  --status-info-ink:#0D3E9B;        /* info.700    */
  --status-neutral-ink:#53575f;     /* slate.700   */
  --link:#1D64EC;                   /* info.500    */

  /* type (primitives.json → typography.font.*) */
  --click-font-regular:"Inter","SF Pro Display",-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Oxygen,Ubuntu,Cantarell,"Open Sans","Helvetica Neue",sans-serif;
  --click-font-mono:"Inconsolata",Consolas,"SFMono Regular",ui-monospace,monospace;
  --click-font-size-0:0.625rem; --click-font-size-1:0.75rem; --click-font-size-2:0.875rem;
  --click-font-size-3:1rem;     --click-font-size-4:1.125rem; --click-font-size-5:1.25rem;
  --click-font-size-6:2rem;
  --click-font-weight-1:400; --click-font-weight-2:500; --click-font-weight-3:600; --click-font-weight-4:700;
  --click-line-height-1:150%; --click-line-height-2:160%;

  /* space + border + shadow (primitives.json → spaces/border/shadow) */
  --click-space-1:0.25rem; --click-space-2:0.5rem;  --click-space-3:0.75rem;
  --click-space-4:1rem;    --click-space-5:1.5rem;  --click-space-6:2rem; --click-space-7:2.5rem;
  --click-radii-1:0.25rem; --click-radii-2:0.5rem;  --click-radii-3:0.75rem; --click-radii-full:9999px;
  --click-border-width-1:1px;
  --click-shadow-1:0 4px 6px -1px rgba(0,0,0,.08), 0 2px 4px -1px rgba(0,0,0,.06);
  --click-shadow-5:0 2px 2px 0 rgba(0,0,0,.03);
  --click-transition-smooth:150ms;
}

/* Dark theme. Declared under both scopes so the OS setting and the in-page
   toggle each win where they should: :where() keeps the media block at zero
   specificity so an explicit light stamp beats OS-dark. */
@media (prefers-color-scheme:dark){
  :root:where(:not([data-cui-theme="light"])){
    color-scheme:dark;
    --click-global-color-background-default:#1F1F1C;
    --click-global-color-background-muted:#282828;
    --click-global-color-text-default:#ffffff;
    --click-global-color-text-muted:#b3b6bd;
    --click-global-color-stroke-default:#323232;
    --click-global-color-accent-default:#FAFF69;
    --surface-page:var(--click-palette-neutral-900);
    --surface-card:var(--click-global-color-background-default);
    --surface-sunken:var(--click-global-color-background-muted);
    --surface-hover:var(--click-global-color-background-muted);
    --status-neutral:#808691;
    --status-critical-bg:#300000; --status-warning-bg:#471A00; --status-good-bg:#004206;
    --status-info-bg:#061C47;     --status-neutral-bg:#282828;
    --status-critical-ink:#ffbaba; --status-warning-ink:#FFCBAD; --status-good-ink:#99FFA1;
    --status-info-ink:#A1BEF7;     --status-neutral-ink:#b3b6bd;
    --link:#A1BEF7;                /* info.200 — info.500 is too dark to read here */
    --click-shadow-1:0 4px 6px -1px rgba(0,0,0,.5), 0 2px 4px -1px rgba(0,0,0,.4);
    --click-shadow-5:0 2px 2px 0 rgba(0,0,0,.3);
  }
}
:root[data-cui-theme="dark"]{
  color-scheme:dark;
  --click-global-color-background-default:#1F1F1C;
  --click-global-color-background-muted:#282828;
  --click-global-color-text-default:#ffffff;
  --click-global-color-text-muted:#b3b6bd;
  --click-global-color-stroke-default:#323232;
  --click-global-color-accent-default:#FAFF69;
  --surface-page:var(--click-palette-neutral-900);
  --surface-card:var(--click-global-color-background-default);
  --surface-sunken:var(--click-global-color-background-muted);
  --surface-hover:var(--click-global-color-background-muted);
  --status-neutral:#808691;
  --status-critical-bg:#300000; --status-warning-bg:#471A00; --status-good-bg:#004206;
  --status-info-bg:#061C47;     --status-neutral-bg:#282828;
  --status-critical-ink:#ffbaba; --status-warning-ink:#FFCBAD; --status-good-ink:#99FFA1;
  --status-info-ink:#A1BEF7;     --status-neutral-ink:#b3b6bd;
  --link:#A1BEF7;                /* info.200 — info.500 is too dark to read here */
  --click-shadow-1:0 4px 6px -1px rgba(0,0,0,.5), 0 2px 4px -1px rgba(0,0,0,.4);
  --click-shadow-5:0 2px 2px 0 rgba(0,0,0,.3);
}

*{box-sizing:border-box;margin:0;padding:0}
body{font-family:var(--click-font-regular);background:var(--surface-page);color:var(--ink);font-size:var(--click-font-size-2);line-height:var(--click-line-height-1)}
/* Header and nav stick as ONE band. They used to stick separately, with the
   nav pinned at a hardcoded top:53px that had to equal the header's height —
   it did not (the header measures ~74px), so once scrolled the header covered
   the top 21px of the nav and its labels were sliced in half. A single sticky
   wrapper removes the constant instead of re-tuning it. --topbar-h is measured
   at runtime and drives scroll-margin and the scroll-spy, so nothing else
   hardcodes this height either. */
.topbar{position:sticky;top:0;z-index:100}
header{background:var(--header-bg);color:var(--header-ink);padding:var(--click-space-3) var(--click-space-6);display:flex;align-items:center;gap:var(--click-space-3)}
header .logo{font-size:var(--click-font-size-5);font-weight:var(--click-font-weight-4);color:var(--header-logo);letter-spacing:-.5px}
header h1{font-size:var(--click-font-size-4);font-weight:var(--click-font-weight-3);line-height:1.3}
header .meta{margin-left:auto;text-align:right;font-size:var(--click-font-size-1);opacity:.75;line-height:var(--click-line-height-2)}
#theme-toggle{margin-left:var(--click-space-4);background:transparent;color:var(--header-ink);border:var(--click-border-width-1) solid rgba(255,255,255,.25);border-radius:var(--click-radii-full);padding:var(--click-space-1) var(--click-space-3);font:inherit;font-size:var(--click-font-size-1);cursor:pointer;white-space:nowrap;transition:background var(--click-transition-smooth)}
#theme-toggle:hover{background:rgba(255,255,255,.12)}
nav{background:var(--surface-card);border-bottom:var(--click-border-width-1) solid var(--stroke);padding:0 var(--click-space-6);display:flex;overflow-x:auto}
nav a{padding:var(--click-space-3) var(--click-space-4);color:var(--ink-muted);text-decoration:none;font-size:var(--click-font-size-1);font-weight:var(--click-font-weight-2);white-space:nowrap;border-bottom:2px solid transparent;display:block}
nav a:hover,nav a.active{color:var(--ink);border-bottom-color:var(--ink)}
.badge{display:inline-block;padding:2px var(--click-space-2);border-radius:var(--click-radii-full);font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);text-transform:uppercase;letter-spacing:.5px;margin-top:2px}
.badge-cloud{background:var(--status-info);color:#fff}
.badge-onprem{background:var(--status-good);color:#fff}
.badge-gov{background:#8800CC;color:#fff}
main{max-width:1600px;margin:0 auto;padding:var(--click-space-5) var(--click-space-5) var(--click-space-7)}
section{margin-bottom:var(--click-space-6);scroll-margin-top:calc(var(--topbar-h, 124px) + var(--click-space-2))}
section h2{font-size:var(--click-font-size-3);font-weight:var(--click-font-weight-3);color:var(--ink);margin-bottom:var(--click-space-3);padding-bottom:var(--click-space-2);border-bottom:var(--click-border-width-1) solid var(--stroke);display:flex;align-items:center;gap:var(--click-space-2)}
.stats-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(155px,1fr));gap:var(--click-space-3);margin-bottom:var(--click-space-5)}
.stat-card{background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-2);padding:var(--click-space-4);box-shadow:var(--click-shadow-5)}
.stat-card .val{font-size:var(--click-font-size-6);font-weight:var(--click-font-weight-3);color:var(--ink);line-height:1.15;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}
.stat-card .val.val-md{font-size:var(--click-font-size-5)}
.stat-card .val.val-sm{font-size:var(--click-font-size-4)}
.stat-card .lbl{font-size:var(--click-font-size-1);color:var(--ink-muted);margin-top:var(--click-space-1)}
.charts-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(460px,1fr));gap:var(--click-space-4)}
.chart-card{background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-2);padding:var(--click-space-4);box-shadow:var(--click-shadow-5)}
.chart-card h3{font-size:var(--click-font-size-2);font-weight:var(--click-font-weight-3);color:var(--ink);margin-bottom:var(--click-space-3)}
.chart-wrap{position:relative}
.h200{height:200px}.h220{height:220px}.h260{height:260px}.h300{height:300px}.h360{height:360px}.h420{height:420px}
table.dt{width:100%;border-collapse:collapse;font-size:var(--click-font-size-1);color:var(--ink)}
table.dt th{background:var(--surface-sunken);color:var(--ink-muted);font-weight:var(--click-font-weight-3);padding:var(--click-space-2) var(--click-space-3);text-align:left;border-bottom:var(--click-border-width-1) solid var(--stroke);white-space:nowrap;cursor:pointer;user-select:none}
table.dt th:hover{color:var(--ink)}
table.dt td{padding:var(--click-space-2) var(--click-space-3);border-bottom:var(--click-border-width-1) solid var(--stroke);vertical-align:top;max-width:400px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
table.dt tr:hover td{background:var(--surface-hover)}
table.dt .num{text-align:right;font-variant-numeric:tabular-nums}
table.dt a{color:var(--link);text-decoration:none}
table.dt a:hover{text-decoration:underline}
.tbl-wrap{overflow-x:auto;border-radius:var(--click-radii-2);border:var(--click-border-width-1) solid var(--stroke);background:var(--surface-card)}
.no-data{color:var(--ink-muted);font-style:italic;padding:var(--click-space-5);text-align:center}
.alert-row td{background:var(--status-warning-bg) !important}
.error-row td{background:var(--status-critical-bg) !important}
.ok-badge,.err-badge,.warn-badge{padding:2px var(--click-space-2);border-radius:var(--click-radii-full);font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);white-space:nowrap}
.ok-badge{background:var(--status-good-bg);color:var(--status-good-ink)}
.err-badge{background:var(--status-critical-bg);color:var(--status-critical-ink)}
.warn-badge{background:var(--status-warning-bg);color:var(--status-warning-ink)}
/* search / filter bar */
.filter-bar{display:flex;flex-wrap:wrap;gap:var(--click-space-2);align-items:center;margin-bottom:var(--click-space-3);background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);padding:var(--click-space-3);border-radius:var(--click-radii-2)}
.filter-bar input[type=text],.filter-bar select{padding:var(--click-space-2) var(--click-space-3);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);font:inherit;font-size:var(--click-font-size-1);background:var(--surface-card);color:var(--ink);outline:none}
.filter-bar input[type=text]{flex:1;min-width:200px}
.filter-bar input[type=text]:focus,.filter-bar select:focus{border-color:var(--ink-muted)}
.count-badge{font-size:var(--click-font-size-1);color:var(--ink-muted);white-space:nowrap}
.pagination{display:flex;gap:var(--click-space-2);align-items:center;justify-content:center;padding:var(--click-space-3);font-size:var(--click-font-size-1);color:var(--ink-muted)}
.pagination button{padding:var(--click-space-1) var(--click-space-3);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);background:var(--surface-card);color:var(--ink);cursor:pointer;font:inherit;font-size:var(--click-font-size-1)}
.pagination button:hover{background:var(--surface-hover)}
.pagination .cur{font-weight:var(--click-font-weight-3);color:var(--ink)}
/* subsection title */
#tbl-host-tunables td:last-child,#tbl-host-os td:last-child{white-space:normal;max-width:38ch}
#tbl-host-procs td:last-child{white-space:normal;max-width:60ch;font-family:var(--click-font-mono);font-size:var(--click-font-size-0)}
.host-note{color:var(--ink-muted);font-size:var(--click-font-size-1);margin-top:var(--click-space-2)}
.sql-peek{margin-top:var(--click-space-3);background:var(--surface-sunken);color:var(--ink);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);padding:var(--click-space-3);font-family:var(--click-font-mono);font-size:var(--click-font-size-1);line-height:var(--click-line-height-2);white-space:pre-wrap;word-break:break-word;overflow:auto;max-height:220px}
.sub-title{font-size:var(--click-font-size-2);font-weight:var(--click-font-weight-3);color:var(--ink);margin:var(--click-space-5) 0 var(--click-space-2)}
footer{text-align:center;color:var(--ink-muted);font-size:var(--click-font-size-1);padding:var(--click-space-5);margin-top:var(--click-space-2)}
@media(max-width:700px){.charts-grid{grid-template-columns:1fr}}
/* alerts */
.alert-ok{background:var(--status-good-bg);border:var(--click-border-width-1) solid var(--status-good);border-left:4px solid var(--status-good);padding:var(--click-space-3) var(--click-space-5);border-radius:var(--click-radii-2);color:var(--status-good-ink);font-weight:var(--click-font-weight-3)}
.alert-skipped{background:var(--surface-sunken);border:var(--click-border-width-1) solid var(--stroke);border-left:4px solid var(--status-neutral);padding:var(--click-space-2) var(--click-space-5);border-radius:var(--click-radii-2);color:var(--ink-muted);font-size:var(--click-font-size-1);margin-top:var(--click-space-2)}
.alert-item{background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-2);padding:var(--click-space-3) var(--click-space-4);margin-bottom:var(--click-space-2);border-left:4px solid var(--status-neutral);box-shadow:var(--click-shadow-5)}
.alert-item.alert-critical{border-left-color:var(--status-critical)}
.alert-item.alert-warning{border-left-color:var(--status-warning)}
.alert-item.alert-info{border-left-color:var(--status-info)}
.alert-item.alert-error{border-left-color:var(--status-neutral)}
.alert-header{display:flex;align-items:center;gap:var(--click-space-2);margin-bottom:var(--click-space-1);flex-wrap:wrap}
.alert-title{font-weight:var(--click-font-weight-3);font-size:var(--click-font-size-2)}
/* Status never rides on colour alone: every severity ships an icon + label. */
.alert-icon{font-style:normal;line-height:1}
.alert-count{font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);padding:2px var(--click-space-2);border-radius:var(--click-radii-full)}
.alert-count.badge-critical{background:var(--status-critical-bg);color:var(--status-critical-ink)}
.alert-count.badge-warning{background:var(--status-warning-bg);color:var(--status-warning-ink)}
.alert-count.badge-info{background:var(--status-info-bg);color:var(--status-info-ink)}
.alert-count.badge-error{background:var(--status-neutral-bg);color:var(--status-neutral-ink)}
.alert-desc{color:var(--ink-muted);font-size:var(--click-font-size-1);margin-bottom:var(--click-space-2);line-height:var(--click-line-height-2)}
.alert-messages{padding-left:18px;margin:0}
.alert-messages li{font-size:var(--click-font-size-1);color:var(--ink);margin:3px 0;font-family:var(--click-font-mono);word-break:break-word;white-space:pre-wrap}
.alert-err-msg{font-size:var(--click-font-size-1);color:var(--ink-muted);margin-top:var(--click-space-1);font-style:italic}
.alert-tags{display:flex;gap:var(--click-space-1);flex-wrap:wrap;margin-top:var(--click-space-2)}
.alert-tag{background:var(--surface-sunken);border:var(--click-border-width-1) solid var(--stroke);color:var(--ink-muted);border-radius:var(--click-radii-full);padding:1px var(--click-space-2);font-size:var(--click-font-size-0)}
.alert-summary-bar{display:flex;gap:var(--click-space-2);flex-wrap:wrap;margin-bottom:var(--click-space-3)}
.alert-summary-chip{padding:var(--click-space-1) var(--click-space-3);border-radius:var(--click-radii-full);font-size:var(--click-font-size-1);font-weight:var(--click-font-weight-3);display:inline-flex;align-items:center;gap:var(--click-space-1)}
.chip-critical{background:var(--status-critical-bg);color:var(--status-critical-ink)}
.chip-error{background:var(--status-neutral-bg);color:var(--status-neutral-ink)}
.chip-warning{background:var(--status-warning-bg);color:var(--status-warning-ink)}
.chip-info{background:var(--status-info-bg);color:var(--status-info-ink)}
</style>
</head>
<body>

<div class="topbar">
<header>
  <div class="logo">ClickHouse</div>
  <div>
    <h1>Diagnostic Dashboard</h1>
    <div id="hdr-badge"></div>
  </div>
  <div class="meta" id="hdr-meta"></div>
  <button id="theme-toggle" type="button" aria-label="Toggle colour theme"></button>
</header>

<nav id="main-nav">
  <a href="#sec-alerts" id="nav-alerts">Alerts</a>
  <a href="#sec-qa" id="nav-qa" style="display:none">Query Analysis</a>
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
  <a href="#sec-logs" id="nav-logs" style="display:none">Logs</a>
  <a href="#sec-files" id="nav-files" style="display:none">Collected Files</a>
  <a href="#sec-server-errors">Server Errors</a>
  <a href="#sec-async-inserts" id="nav-async-inserts" style="display:none">Async Inserts</a>
</nav>
</div><!-- .topbar -->

<main>

<!-- ── ALERTS ── -->
<section id="sec-alerts">
  <h2>🚨 Alert Summary</h2>
  <div id="alerts-summary-bar"></div>
  <div id="alerts-panel"></div>
</section>

<!-- ── QUERY ANALYSIS ── -->
<section id="sec-qa" style="display:none">
  <h2>🔍 Query Analysis</h2>
  <div id="qa-focus" class="alert-item" style="border-left-color:var(--status-info);margin-bottom:var(--click-space-4)"></div>

  <!-- Query text card — hidden in gov mode -->
  <div id="qa-query-card" class="chart-card" style="display:none;margin-bottom:18px">
    <h3>Focus query — SQL text</h3>
    <pre id="qa-query-text" style="background:var(--surface-sunken);color:var(--ink);border:var(--click-border-width-1) solid var(--stroke);padding:var(--click-space-3);border-radius:var(--click-radii-1);overflow:auto;max-height:280px;font-family:var(--click-font-mono);font-size:var(--click-font-size-1);white-space:pre-wrap;line-height:var(--click-line-height-2)"></pre>
  </div>

  <!-- Per-execution scatters — one dot per individual query execution.
       Five charts share this shape; each picks a different metric off
       the qa_executions array and uses the same adaptive-unit + colour
       logic (green succ, red cross failure, tooltip with query_id). -->
  <div class="charts-grid">
    <div class="chart-card">
      <h3>Per-execution duration</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-scatter"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Per-execution memory usage</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-mem"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Per-execution user CPU</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-cpu"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Per-execution read rows</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-rrows"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Per-execution read bytes</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-rbytes"></canvas></div>
    </div>
  </div>

  <!-- Count charts — bucketed per MINUTE. Per-execution doesn't apply
       here (count of "one" per dot would be uninformative); minute is
       the finest useful grain. -->
  <div class="charts-grid" style="margin-top:18px">
    <div class="chart-card">
      <h3>Executions per minute</h3>
      <div class="chart-wrap h260"><canvas id="chart-qa-execs"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Failed queries per minute, by error type</h3>
      <div class="chart-wrap h260"><canvas id="chart-qa-failed"></canvas></div>
    </div>
  </div>

  <!-- Single-execution charts -->
  <div class="charts-grid" style="margin-top:18px">
    <div class="chart-card">
      <h3>Top 30 ProfileEvents (slowest execution)</h3>
      <div class="chart-wrap h420"><canvas id="chart-qa-profile"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Fast vs Slow — ProfileEvents (top 30 by |delta|)</h3>
      <div class="chart-wrap h420"><canvas id="chart-qa-compare"></canvas></div>
    </div>
  </div>

  <div class="sub-title">Per-host distribution (hash)</div>
  <div class="tbl-wrap"><div id="tbl-qa-host"></div></div>
  <div class="sub-title" style="margin-top:18px">Failed queries — per-table × per-error breakdown</div>
  <div class="tbl-wrap"><div id="tbl-qa-failed"></div></div>
  <div class="sub-title" style="margin-top:18px">Tables referenced by the focus query</div>
  <div class="tbl-wrap"><div id="tbl-qa-tables"></div></div>
  <div class="sub-title" style="margin-top:18px">Parts / marks / streams scanned (text_log, slowest execution)</div>
  <div class="tbl-wrap"><div id="tbl-qa-parts"></div></div>
  <div class="sub-title" style="margin-top:18px">Full text_log for the slowest execution</div>
  <div class="tbl-wrap" style="max-height:480px;overflow-y:auto"><div id="tbl-qa-textlog"></div></div>
</section>

<!-- ── OVERVIEW ── -->
<section id="sec-overview">
  <h2>📈 Overview</h2>
  <div class="stats-grid" id="stats-grid"></div>

  <!-- Host facts: the same content as host_info.json, shown beside what the
       system tables report so the OS context does not need a second file. -->
  <div id="host-block" style="display:none">
    <div class="sub-title">Host &mdash; OS, kernel and hardware</div>
    <div class="charts-grid">
      <div class="chart-card">
        <h3>Machine</h3>
        <div class="tbl-wrap"><div id="tbl-host-os"></div></div>
      </div>
      <div class="chart-card">
        <h3>ClickHouse-relevant tunables</h3>
        <div class="tbl-wrap"><div id="tbl-host-tunables"></div></div>
        <p class="host-note">Only settings ClickHouse itself checks at startup are flagged; the rest are reported as facts.</p>
      </div>
    </div>
    <div class="sub-title">Top processes by resident memory</div>
    <div class="tbl-wrap"><div id="tbl-host-procs"></div></div>
    <div id="host-notes"></div>
  </div>
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
      <h3>Avg Duration by Kind (ms)</h3>
      <div class="chart-wrap h200"><canvas id="chart-query-duration"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Avg Memory by Kind (MB)</h3>
      <div class="chart-wrap h200"><canvas id="chart-query-memory"></canvas></div>
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
      <pre class="sql-peek" id="peek-slow-queries" style="display:none"></pre>
    </div>
    <div class="chart-card">
      <h3>Top 20 Heaviest Reads (avg MB / query)</h3>
      <div class="chart-wrap h420"><canvas id="chart-heavy-reads"></canvas></div>
      <pre class="sql-peek" id="peek-heavy-reads" style="display:none"></pre>
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

<!-- ── LOGS ── -->
<section id="sec-logs" style="display:none">
  <h2>📜 Logs</h2>
  <p class="host-note" id="logs-scope"></p>
  <div class="filter-bar">
    <input type="text" id="log-search" placeholder="Search message or logger…" oninput="logsFilter()">
    <select id="log-level-filter" onchange="logsFilter()"><option value="">All levels</option></select>
    <span class="count-badge" id="log-count"></span>
  </div>
  <div class="tbl-wrap"><div id="tbl-logs"></div></div>
  <div class="pagination" id="log-pagination"></div>
</section>

<!-- ── COLLECTED FILES ── -->
<section id="sec-files" style="display:none">
  <h2>📁 Collected Files</h2>
  <p class="host-note" id="files-scope"></p>
  <div class="filter-bar">
    <input type="text" id="file-search" placeholder="Filter by name…" oninput="filesFilter()">
    <select id="file-group-filter" onchange="filesFilter()"><option value="">All folders</option></select>
    <span class="count-badge" id="file-count"></span>
  </div>
  <div class="tbl-wrap"><div id="tbl-files"></div></div>
  <p class="host-note">Links open the file from the extracted bundle, beside this page. Nothing here is embedded — the raw server logs alone are tail-copied at up to 50&nbsp;MiB each, which would make this page unopenable.</p>
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
//
// Five categorical slots, every hex a Click UI token. The set is deliberately
// MODE-INVARIANT — the same five work on the light card (#ffffff) and the dark
// card (#1F1F1C) — which is what lets a theme switch be a Chart.update() rather
// than a teardown and rebuild.
//
// Validated, not eyeballed (dataviz validate_palette.js, both modes):
//   lightness band PASS · chroma floor PASS · contrast >=3:1 PASS
//   adjacent CVD dE 20.1 (target >=8) · normal-vision dE 31.3 (floor >=15)
// All-pairs forms (scatter, donut, small multiples) only clear the gates for
// the FIRST THREE slots, so those forms fold past three — see foldSeries.
//
// Do not extend this array. A sixth generated hue is indistinguishable from an
// existing slot under CVD; the tail folds into OTHER instead. Slots are
// assigned by entity index and never by rank, so filtering cannot repaint the
// survivors.
const C = [
  '#089B83',  // 1 teal     — teal.600
  '#AA00FF',  // 2 violet   — violet.500
  '#B28800',  // 3 amber    — sunrise.600
  '#CC0099',  // 4 fuchsia  — fuchsia.600
  '#959900'   // 5 olive    — brand.600
];
const OTHER = '#808691';        // slate.500 — the de-emphasis / folded-tail gray
const ALL_PAIRS_CAP = 3;        // scatter/donut/small-multiple ceiling

// Ordinal ramp for ordered buckets (delay bands). One hue, monotone lightness,
// stepped per mode so the pale end still clears 2:1 on its own surface —
// validated with validate_palette.js --ordinal in both modes. This is the one
// palette that is NOT mode-invariant, so it repaints on a theme switch.
const ORDINAL = {
  light:['#6D9BF3','#437EEF','#1D64EC','#104EC6','#0D3E9B'],  // info.300→700
  dark: ['#A1BEF7','#6D9BF3','#437EEF','#1D64EC','#104EC6']   // info.200→600
};

// Status ramp. Reserved meaning — never a series colour, and never carried by
// hue alone (every use ships an icon + label).
const STATUS = {
  critical:'#f10000',  // danger.500
  warning: '#F55A00',  // warning.500
  good:    '#008A0B',  // success.700
  info:    '#1D64EC',  // info.500
  neutral: '#808691'   // slate.500
};
const alpha = (h,a) => h + Math.round(a*255).toString(16).padStart(2,'0');
const DICT_STATUS_COLOR = {
  LOADED:STATUS.good, FAILED:STATUS.critical, LOADING:STATUS.warning,
  NOT_LOADED:STATUS.neutral, UNKNOWN:STATUS.info
};

// Slot by index — folds instead of cycling.
const slot = (i) => i < C.length ? C[i] : OTHER;

// Keep the top 'cap' categories by weight and fold the rest into "Other", so a
// chart never needs a sixth hue. Returns the surviving category order.
function foldSeries(cats, weight, cap){
  cap = cap || C.length;
  if(cats.length <= cap) return cats;
  const keep = [...cats].sort((a,b)=>(weight(b)||0)-(weight(a)||0)).slice(0,cap-1);
  const ordered = cats.filter(c=>keep.includes(c));
  ordered.push('Other');
  return ordered;
}

// ── theme ─────────────────────────────────────────────────────────────────────
const themeVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
const CHARTS = [];
function mkChart(el, cfg){
  if(!el) return null;
  const c = new Chart(el, cfg);
  CHARTS.push(c);
  return c;
}

// Chart.js reads these at draw time, so retheming the axes, ticks, legend and
// grid is a defaults swap plus an update — no chart is rebuilt.
function applyChartTheme(){
  const ink=themeVar('--ink-muted'), stroke=themeVar('--stroke');
  Chart.defaults.color        = ink;
  Chart.defaults.borderColor  = stroke;
  Chart.defaults.font.family  = themeVar('--click-font-regular');
  Chart.defaults.font.size    = 11;
  // Grid and axis lines are chrome, not data — they take the stroke token and
  // sit behind the marks. Without this Chart.js falls back to defaults.color
  // and paints a near-white grid straight over the series in dark mode.
  Chart.defaults.scale.grid.color       = stroke;
  Chart.defaults.scale.grid.tickColor   = stroke;
  Chart.defaults.scale.grid.drawTicks   = false;
  Chart.defaults.scale.grid.z           = -1;
  Chart.defaults.scale.border.color     = stroke;
  Chart.defaults.scale.ticks.color      = ink;
  Chart.defaults.scale.ticks.padding    = 6;
  Chart.defaults.plugins.legend.labels.color        = ink;
  Chart.defaults.plugins.legend.labels.boxWidth     = 10;
  Chart.defaults.plugins.legend.labels.boxHeight    = 10;
  Chart.defaults.plugins.legend.labels.usePointStyle= true;
  Chart.defaults.plugins.tooltip.backgroundColor = themeVar('--surface-card');
  Chart.defaults.plugins.tooltip.titleColor      = themeVar('--ink');
  Chart.defaults.plugins.tooltip.bodyColor       = themeVar('--ink');
  Chart.defaults.plugins.tooltip.borderColor     = stroke;
  Chart.defaults.plugins.tooltip.borderWidth     = 1;
  // Thin marks, with the 4px radius on the data end only (the baseline end
  // stays square so the bar reads as anchored).
  Chart.defaults.datasets.bar.maxBarThickness = 22;
  Chart.defaults.datasets.bar.borderSkipped   = 'start';
  Chart.defaults.datasets.bar.borderRadius    = 4;
  Chart.defaults.elements.line.borderWidth    = 2;
  Chart.defaults.elements.point.radius        = 4;
  Chart.defaults.elements.point.hoverRadius   = 6;
}

// The 2px spacer between touching fills is painted in the surface colour, so it
// is the one series-side value that has to follow the theme.
function repaintSurfaceGaps(){
  applyChartTheme();
  const surf=themeVar('--surface-card'), ink=themeVar('--ink-muted'),
        stroke=themeVar('--stroke'), fg=themeVar('--ink');
  const ramp = ORDINAL[currentTheme()] || ORDINAL.light;
  CHARTS.forEach(c=>{
    Object.values((c.options && c.options.scales) || {}).forEach(sc=>{
      if(!sc) return;
      if(sc.grid)   { sc.grid.color=stroke; sc.grid.tickColor=stroke; }
      if(sc.border) { sc.border.color=stroke; }
      if(sc.ticks)  { sc.ticks.color=ink; }
      if(sc.title)  { sc.title.color=ink; }
    });
    const pl=(c.options && c.options.plugins) || {};
    if(pl.legend && pl.legend.labels) pl.legend.labels.color=ink;
    if(pl.tooltip){
      pl.tooltip.backgroundColor=surf; pl.tooltip.titleColor=fg;
      pl.tooltip.bodyColor=fg; pl.tooltip.borderColor=stroke;
    }
    c.data.datasets.forEach(ds=>{
      if(ds._surfaceGap) ds.borderColor=surf;
      if(ds._ordinal){
        ds.backgroundColor=ds.data.map((_,i)=>ramp[Math.min(i,ramp.length-1)]);
        ds.borderColor=ds.backgroundColor;
      }
    });
    c.update('none');
  });
}

function currentTheme(){
  const stamped = document.documentElement.getAttribute('data-cui-theme');
  if(stamped) return stamped;
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function setTheme(t){
  document.documentElement.setAttribute('data-cui-theme', t);
  try{ localStorage.setItem('chdiag-theme', t); }catch(e){}
  const btn=document.getElementById('theme-toggle');
  if(btn) btn.textContent = t==='dark' ? '☀ Light' : '☾ Dark';
  // repaintSurfaceGaps re-applies Chart defaults and updates every chart —
  // one pass, not the double layout+paint this used to do.
  repaintSurfaceGaps();
}

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

// The full normalized_query_hash. It arrives as a string precisely so it can be
// shown and copied whole — truncating it to the last 8 digits (what this used
// to do) made it useless for grepping query_log, which is the one thing a
// reader wants a hash for.
function fullHash(h){ return String(h==null?'':h); }

// A one-line fingerprint of the query for an axis label. Collapses the SQL onto
// a single line and clips it; the whole statement is a click away, and the full
// hash is always in the tooltip, so nothing is lost by clipping here.
function queryLabel(r, max){
  const q=String(r.sample_query||'').replace(/\s+/g,' ').trim();
  if(!q || q==='(redacted in gov mode)') return fullHash(r.hash);
  return q.length>max ? q.slice(0,max-1)+'…' : q;
}

// Show the full SQL for a clicked bar underneath its chart.
function bindQueryPeek(chart, rows, peekId){
  const el=document.getElementById(peekId);
  if(!chart||!el)return;
  const has=rows.some(r=>r.sample_query && r.sample_query!=='(redacted in gov mode)');
  if(!has)return;
  el.textContent='Click a bar to show the query behind it.';
  el.style.display='';
  chart.options.onClick=(evt,els)=>{
    if(!els||!els.length)return;
    const r=rows[els[0].index];
    el.textContent='-- normalized_query_hash: '+fullHash(r.hash)
      +'   user: '+(r.user||'?')+'   executions: '+(r.executions||'?')
      +'\n-- one representative execution; literals differ between runs\n\n'
      +String(r.sample_query||'').trim();
  };
  chart.canvas.style.cursor='pointer';
  chart.update('none');
}

// pivot [{timeF, catF, valF}] → Chart.js datasets.
//
// Categories past the palette length fold into "Other" rather than cycling a
// sixth hue, and each category keeps its slot regardless of magnitude so the
// colour follows the entity and not its rank.
function pivot(rows,tf,cf,vf){
  const times=[...new Set(rows.map(r=>r[tf]))].sort();
  const raw=[...new Set(rows.map(r=>r[cf]))];
  const total={};
  rows.forEach(r=>{ total[r[cf]]=(total[r[cf]]||0)+Number(r[vf]||0); });
  const cats=foldSeries(raw, c=>total[c], C.length);
  const kept=new Set(cats);
  const lk={};
  rows.forEach(r=>{
    const c=kept.has(r[cf])?r[cf]:'Other';
    const k=r[tf]+'|'+c;
    lk[k]=(lk[k]||0)+Number(r[vf]||0);
  });
  return{
    labels:times,
    datasets:cats.map((c,i)=>({
      label:c===''?'(unknown)':c,
      backgroundColor:c==='Other'?OTHER:slot(i),
      borderColor:c==='Other'?OTHER:slot(i),
      borderWidth:1.5,
      borderRadius:4,
      data:times.map(t=>lk[t+'|'+c]||0)
    }))
  };
}

// Give a stacked chart the 2px surface spacer between touching segments.
function stackWithGaps(d){
  const surf=themeVar('--surface-card');
  d.datasets.forEach(ds=>{
    ds.stack='s';
    ds.borderColor=surf;
    ds.borderWidth=2;
    ds.borderRadius=4;
    ds._surfaceGap=true;
  });
  return d;
}

// render a plain HTML table (with optional row-class callback)
// Escape everything that reaches the DOM as text.
//
// Cell values here are customer-controlled: SQL text, exception messages,
// table and column names, and now whole log lines. Interpolating those raw
// (which this did) means a message containing a quote breaks the title
// attribute and one containing markup is parsed as markup. Nothing in the
// bundle is trusted input just because it came from the customer's own server.
function esc(v){
  return String(v??'')
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

// render a plain HTML table (with optional row-class callback).
//
// htmlCols names the columns whose values are markup THIS FILE built (a status
// badge, a link) and must not be escaped. Everything else is escaped. Passing a
// customer-controlled column in htmlCols is a bug.
function renderTable(id, rows, cols, rowClass, htmlCols){
  const el=document.getElementById(id);
  if(!el)return;
  if(!rows||rows.length===0){el.innerHTML='<p class="no-data">No data</p>';return;}
  const keys=cols||Object.keys(rows[0]);
  const raw=new Set(htmlCols||[]);
  let h='<table class="dt"><thead><tr>'+keys.map(k=>'<th>'+esc(k)+'</th>').join('')+'</tr></thead><tbody>';
  rows.forEach(r=>{
    const cls=rowClass?rowClass(r):'';
    h+='<tr'+(cls?' class="'+esc(cls)+'"':'')+'>'
      +keys.map(k=>raw.has(k)
        ? '<td>'+(r[k]??'')+'</td>'
        : '<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>').join('')
      +'</tr>';
  });
  h+='</tbody></table>';
  el.innerHTML=h;
}

// format status badges for dictionaries
function dictStatusBadge(status){
  const cl={'LOADED':'ok-badge','FAILED':'err-badge','LOADING':'warn-badge',
            'NOT_LOADED':'warn-badge'}[status]||'warn-badge';
  return '<span class="'+cl+'">'+esc(status)+'</span>';
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
      +COLS.map(k=>'<th>'+esc(k)+'</th>').join('')+'</tr></thead><tbody>';
    slice.forEach(r=>{
      // database / table_name / partition_key / sorting_key are customer DDL.
      h+='<tr>'+COLS.map(k=>'<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>').join('')+'</tr>';
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
      dbSel.innerHTML+='<option value="'+esc(d)+'">'+esc(d)+'</option>';
    });
    [...new Set(allData.map(r=>r.engine))].sort().forEach(e=>{
      engSel.innerHTML+='<option value="'+esc(e)+'">'+esc(e)+'</option>';
    });
    render();
  };
})();

// ── alerts renderer ───────────────────────────────────────────────────────────
function renderAlerts(){
  const alerts=DATA.alerts||[];
  const fired=alerts.filter(a=>(a.rows&&a.rows.length>0)||a.error);
  const skipped=alerts.filter(a=>a.skipped);
  // Mirror alert.Summarize exactly: "evaluated" excludes BOTH skipped and
  // errored rules, so this number can never disagree with the CLI's.
  // (Today the banner using it only renders when erroredRules is empty, so
  // the subtraction is a no-op there — but keeping two different
  // definitions of one number is how the "11 fired" bug happened.)
  const evaluated=alerts.length-skipped.length-alerts.filter(a=>a.error).length;
  const skipNote=skipped.length
    ? '<div class="alert-skipped">ℹ️ '+skipped.length+' rule(s) not applicable on this server (table not present): '+skipped.map(a=>a.name).join(', ')+'</div>'
    : '';
  const panel=document.getElementById('alerts-panel');
  const bar=document.getElementById('alerts-summary-bar');
  const navLink=document.getElementById('nav-alerts');

  // summary chips. Rules that ERRORED are counted separately from real
  // findings — bucketing them by severity would report e.g. a missing
  // SELECT grant as N critical/warning production problems.
  const counts={critical:0,warning:0,info:0};
  const erroredRules=fired.filter(a=>a.error);
  fired.filter(a=>!a.error).forEach(a=>{const s=a.severity||'warning';if(counts[s]!==undefined)counts[s]++;});
  // The badge counts real findings only. Including errored rules would
  // render a red "11" identical to eleven critical findings when the true
  // state is one permissions problem — the same conflation the CLI's
  // fired/errored split removed. Broken rules get the ⚠ chip instead.
  const total=counts.critical+counts.warning+counts.info;

  // badge the nav link
  if(total>0){
    navLink.innerHTML='Alerts <span style="background:var(--status-critical);color:#fff;border-radius:var(--click-radii-full);padding:1px 7px;font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);margin-left:4px">'+total+'</span>';
  }

  // summary bar
  let barHtml='';
  if(counts.critical) barHtml+='<span class="alert-summary-chip chip-critical">🔴 '+counts.critical+' Critical</span>';
  if(counts.warning)  barHtml+='<span class="alert-summary-chip chip-warning">🟡 '+counts.warning+' Warning</span>';
  if(counts.info)     barHtml+='<span class="alert-summary-chip chip-info">🔵 '+counts.info+' Info</span>';
  if(erroredRules.length) barHtml+='<span class="alert-summary-chip chip-error">⚠ '+erroredRules.length+' Could not run</span>';
  if(bar) bar.innerHTML=barHtml?'<div class="alert-summary-bar">'+barHtml+'</div>':'';

  if(!panel) return;

  if(!fired.length){
    panel.innerHTML='<div class="alert-ok">✅ '+evaluated+' alert rule(s) evaluated — no issues detected</div>'+skipNote;
    return;
  }

  const SEV_ICON={critical:'🔴',warning:'🟡',info:'🔵'};
  const ORDER={critical:0,warning:1,info:2};
  const sorted=[...fired].sort((a,b)=>(ORDER[a.severity]||1)-(ORDER[b.severity]||1));

  let html='';
  sorted.forEach(a=>{
    const sev=a.severity||'warning';
    // A rule whose QUERY broke is not a severity-N finding — show ⚠ so it
    // can't be misread as a red critical at the top of the list.
    const icon=a.error?'⚠':(SEV_ICON[sev]||'🟡');
    const cls=a.error?'alert-error':'alert-'+sev;
    const cnt=a.error?'error':(a.rows||[]).length;
    const cntLabel=a.error?'query error':cnt+' instance'+(cnt===1?'':'s');

    html+='<div class="alert-item '+cls+'">';
    html+='<div class="alert-header">';
    html+=icon+' <span class="alert-title">'+esc(a.title||a.name)+'</span>';
    html+='<span class="alert-count badge-'+(a.error?'error':sev)+'">'+cntLabel+'</span>';
    if((a.tags||[]).length) html+='<span class="alert-tags">'+a.tags.map(t=>'<span class="alert-tag">'+esc(t)+'</span>').join('')+'</span>';
    html+='</div>'; // header

    if(a.description) html+='<div class="alert-desc">'+esc(a.description.trim()).replace(/\n/g,'<br>')+'</div>';

    if(a.error){
      // a.error is raw server exception text — customer-influenced.
      html+='<div class="alert-err-msg">⚠ '+esc(a.error)+'</div>';
    } else if(a.message&&(a.rows||[]).length){
      html+='<ul class="alert-messages">';
      (a.rows||[]).forEach(row=>{
        let msg=a.message;
        // Row values are customer data (table names, partition ids, raw
        // messages) substituted into the rule's message template — the
        // template is ours, the values are not. Escape the whole line.
        Object.entries(row).forEach(([k,v])=>{msg=msg.split('{'+k+'}').join(String(v??''));});
        html+='<li>▸ '+esc(msg)+'</li>';
      });
      html+='</ul>';
    }

    html+='</div>'; // item
  });
  panel.innerHTML=html+skipNote;
}

// ── main init ─────────────────────────────────────────────────────────────────
// Adaptive duration helper used by every duration axis / tooltip in
// the Query Analysis section. The original requirement: render in
// seconds when the value is short, switch to minutes once the peak
// crosses 200 seconds, so a long-running query doesn't show up as
// "180000 ms" in a tooltip.
function pickDurationUnit(maxMs){
  if(maxMs > 200000){
    return {factor: 1/60000, label: 'min', precision: 2};
  }
  return {factor: 1/1000, label: 'sec', precision: 2};
}
function fmtDuration(ms, unit){
  if(ms==null) return '—';
  return (Number(ms) * unit.factor).toFixed(unit.precision) + ' ' + unit.label;
}

// pickByteUnit chooses MiB / GiB / TiB based on the largest value in
// the series. KiB is skipped because at the scale ClickHouse reports
// (read_bytes, memory_usage) a per-execution metric below 1 MiB is
// usually noise and would look like "0.001 MB" anyway.
function pickByteUnit(maxBytes){
  if(maxBytes >= 1024**4) return {factor: 1/(1024**4), label:'TiB', precision:2};
  if(maxBytes >= 1024**3) return {factor: 1/(1024**3), label:'GiB', precision:2};
  return {factor: 1/(1024**2), label:'MiB', precision:2};
}
function fmtBytes(b, unit){
  if(b==null) return '—';
  return (Number(b) * unit.factor).toFixed(unit.precision) + ' ' + unit.label;
}

// renderQaScatter draws a per-execution scatter for one numeric metric
// off the qa_executions array. Five charts in the Query Analysis
// section share this shape — colour-coded success vs failure, tooltip
// shows query_id + hostname + the metric in human units.
//
// opts:
//   ySelect(row) → number     project a metric from one execution
//   yUnit(maxVal) → unit obj  pick the axis unit ({factor,label,precision})
//   yLabel(unit)  → string    title for the Y axis
//   yFmt(value, unit) → string formatter for tooltip
function renderQaScatter(canvasId, rows, opts){
  const succ=[], fail=[];
  let maxY=0;
  rows.forEach(r=>{
    const t=Date.parse(r.ts);
    const y=opts.ySelect(r);
    if(y>maxY) maxY=y;
    const pt={x:t,y:y,query_id:r.query_id,exception_code:r.exception_code,hostname:r.hostname};
    if(Number(r.exception_code)===0 && r.type==='QueryFinish'){ succ.push(pt); }
    else { fail.push(pt); }
  });
  const u=opts.yUnit(maxY);
  mkChart(document.getElementById(canvasId),{
    type:'scatter',
    data:{datasets:[
      // Outcome is a state, not an identity, so it wears the reserved status
      // ramp — and the failure marks carry a distinct glyph so the meaning
      // never rests on colour alone.
      {label:'succeeded',data:succ,backgroundColor:STATUS.good,borderColor:STATUS.good,pointRadius:4},
      {label:'failed',data:fail,backgroundColor:STATUS.critical,borderColor:STATUS.critical,pointRadius:5,pointStyle:'crossRot'}
    ]},
    options:{responsive:true,maintainAspectRatio:false,
      plugins:{
        legend:{position:'top'},
        tooltip:{callbacks:{label:c=>{
          const p=c.raw;
          const when=new Date(p.x).toISOString().replace('T',' ').replace(/\..*/,'');
          const lines=[when+' · '+opts.yFmt(p.y,u),'query_id: '+(p.query_id||''),'host: '+(p.hostname||'')];
          if(Number(p.exception_code)!==0) lines.push('exception_code: '+p.exception_code);
          return lines;
        }}}
      },
      scales:{
        x:{type:'linear',
          title:{display:true,text:'event_time (UTC)'},
          ticks:{callback:v=>new Date(v).toISOString().replace('T',' ').replace(/\..*/,'').slice(5,16)}},
        y:{type:'linear',beginAtZero:true,
          title:{display:true,text:opts.yLabel(u)},
          ticks:{callback:v=>(v*u.factor).toFixed(u.precision)}}
      }}
  });
}

// ── query analysis renderer ───────────────────────────────────────────────────
function renderQueryAnalysis(){
  if(!DATA.qa_enabled) return;
  document.getElementById('sec-qa').style.display='';
  document.getElementById('nav-qa').style.display='';

  // Focus query SQL text — only when not in gov mode (the query text
  // contains real database/table names that gov-mode hashes elsewhere).
  if(DATA.qa_mode !== 'gov'){
    const txt=(((DATA.qa_details||[])[0])||{}).query;
    if(txt){
      document.getElementById('qa-query-text').textContent=txt;
      document.getElementById('qa-query-card').style.display='';
    }
  }

  // Focus card — what the analysis is scoped to and which execution
  // the single-id queries (ProfileEvents, text_log, tables, parts)
  // were filtered against. In hash-only mode this is the slowest
  // execution for the hash (auto-derived in the pre-flight); in
  // --query-id mode it's the user-supplied UUID.
  const det=(DATA.qa_details||[])[0]||{};
  const fast=(DATA.qa_fast_slow||[])[0]||{};
  const focus=document.getElementById('qa-focus');
  let h='<div class="alert-header">';
  h+='<span class="alert-title">Focus query_id: '+esc(DATA.qa_query_id||'(none)')+'</span>';
  h+='<span class="alert-tags"><span class="alert-tag">hash '+esc(DATA.qa_hash||'')+'</span>';
  h+='<span class="alert-tag">window '+(DATA.qa_from||'')+' → '+(DATA.qa_to||'')+'</span></span>';
  h+='</div>';
  if(det.query_kind){
    h+='<div class="alert-desc">';
    h+='kind: <b>'+esc(det.query_kind)+'</b> · user: <b>'+esc(det.user||'?')+'</b> · duration: <b>'+fmt(det.query_duration_ms)+' ms</b>';
    h+=' · read: <b>'+fmt(det.read_rows)+' rows / '+esc(det.memory_usage_human||'?')+'</b>';
    if(det.exception_code && Number(det.exception_code)!==0){
      h+=' · <span style="color:var(--status-critical-ink)">exception '+esc(det.exception_code)+'</span>';
    }
    h+='</div>';
  }
  if(fast.slow_query_id){
    h+='<div class="alert-desc">';
    h+='hash executions: <b>'+fmt(fast.executions)+'</b> · ';
    // query_id is CLIENT-settable: a customer can name a query
    // '<img onerror=...>' and the slowest-execution pick lands it here.
    h+='slowest: <b>'+fmt(fast.slow_duration_ms)+' ms</b> (<code>'+esc(fast.slow_query_id)+'</code>) · ';
    h+='fastest: <b>'+fmt(fast.fast_duration_ms)+' ms</b> (<code>'+esc(fast.fast_query_id)+'</code>)';
    if(Number(fast.fast_duration_ms)>0){
      const ratio=(Number(fast.slow_duration_ms)/Number(fast.fast_duration_ms)).toFixed(1);
      h+=' → <b>'+ratio+'×</b> slower';
    }
    h+='</div>';
  }
  focus.innerHTML=h;

  // ── Per-execution scatters ────────────────────────────────────────────────
  // Five charts (duration, memory, user CPU, read rows, read bytes)
  // share the same shape: one dot per execution, x = event_time,
  // y = the metric for that execution, succ = green dot, fail = red
  // cross. Built off DATA.qa_executions so we never need a separate
  // SQL aggregation for any of these — the executions_timeline query
  // already carries every column we need.
  const execs=DATA.qa_executions||[];
  if(execs.length){
    renderQaScatter('chart-qa-scatter', execs, {
      ySelect: r => Number(r.query_duration_ms),
      yUnit:   max => pickDurationUnit(max),
      yLabel:  u => 'duration (' + u.label + ')',
      yFmt:    (v,u) => fmtDuration(v,u),
    });
    renderQaScatter('chart-qa-mem', execs, {
      ySelect: r => Number(r.memory_usage),
      yUnit:   max => pickByteUnit(max),
      yLabel:  u => 'memory (' + u.label + ')',
      yFmt:    (v,u) => fmtBytes(v,u),
    });
    renderQaScatter('chart-qa-cpu', execs, {
      ySelect: r => Number(r.user_cpu_us||0) / 1e6,    // µs → sec
      yUnit:   () => ({factor:1, label:'sec', precision:3}),
      yLabel:  () => 'user CPU (sec)',
      yFmt:    v => v.toFixed(3) + ' sec',
    });
    renderQaScatter('chart-qa-rrows', execs, {
      ySelect: r => Number(r.read_rows),
      yUnit:   () => ({factor:1, label:'rows', precision:0}),
      yLabel:  () => 'read rows',
      yFmt:    v => fmt(v) + ' rows',
    });
    renderQaScatter('chart-qa-rbytes', execs, {
      ySelect: r => Number(r.read_bytes),
      yUnit:   max => pickByteUnit(max),
      yLabel:  u => 'read (' + u.label + ')',
      yFmt:    (v,u) => fmtBytes(v,u),
    });
  }

  // ── Minute-bucketed count charts ──────────────────────────────────────────
  const sum=DATA.qa_summary||[];
  if(sum.length){
    const labels=sum.map(r=>r.time_bucket);
    mkChart(document.getElementById('chart-qa-execs'),{
      type:'bar',
      data:{labels,datasets:[
        {label:'succeeded',data:sum.map(r=>Number(r.succeeded)),
          backgroundColor:STATUS.good,borderColor:STATUS.good,borderWidth:1,borderRadius:4,stack:'s'},
        {label:'failed',data:sum.map(r=>Number(r.failed)),
          backgroundColor:STATUS.critical,borderColor:STATUS.critical,borderWidth:1,borderRadius:4,stack:'s'}
      ]},
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true},y:{stacked:true,beginAtZero:true}}}
    });
  }

  const fot=DATA.qa_failed_over_time||[];
  if(fot.length){
    const piv=pivot(fot,'time_bucket','error_type','errors');
    piv.datasets.forEach(ds=>{ds.stack='e';});
    mkChart(document.getElementById('chart-qa-failed'),{
      type:'bar',data:piv,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true},y:{stacked:true,beginAtZero:true}}}
    });
  }

  // Top ProfileEvents for the focus (slowest) execution.
  const pe=(DATA.qa_profile||[]).slice(0,30);
  if(pe.length){
    mkChart(document.getElementById('chart-qa-profile'),{
      type:'bar',
      data:{labels:pe.map(r=>r.metric),datasets:[{label:'value',data:pe.map(r=>Number(r.value)),
        backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:c=>' '+fmt(c.raw)}}},
        scales:{x:{beginAtZero:true,ticks:{callback:v=>fmt(v)}}}}
    });
  }

  // Fast vs Slow comparison — top 30 by |delta|.
  const cmp=(DATA.qa_pe_compare||[]).slice(0,30);
  if(cmp.length){
    mkChart(document.getElementById('chart-qa-compare'),{
      type:'bar',
      data:{labels:cmp.map(r=>r.metric),datasets:[
        // Which execution, not good-vs-bad: identity, so categorical slots.
        {label:'slow',data:cmp.map(r=>Number(r.slow_value)),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
        {label:'fast',data:cmp.map(r=>Number(r.fast_value)),
          backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4}
      ]},
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{position:'top'},tooltip:{callbacks:{label:c=>c.dataset.label+': '+fmt(c.raw)}}},
        scales:{x:{beginAtZero:true,ticks:{callback:v=>fmt(v)}}}}
    });
  }

  // Detail tables
  renderTable('tbl-qa-host',DATA.qa_by_host||[],
    ['hostname','executions','avg_duration_ms','p95_duration_ms','max_duration_ms','min_duration_ms','avg_memory','max_memory','errors'],
    r=>Number(r.errors||0)>0?'error-row':'');
  renderTable('tbl-qa-failed',DATA.qa_failed||[],
    ['error_type','tables_touched','user','errors','first_seen','last_seen','max_duration_ms','sample_exception']);
  renderTable('tbl-qa-tables',DATA.qa_tables||[],
    ['database','table_name','engine','total_rows','size','size_uncompressed','partition_key','sorting_key','storage_policy']);
  renderTable('tbl-qa-parts',DATA.qa_text_parts||[],
    ['ts','level','logger_name','message']);
  renderTable('tbl-qa-textlog',DATA.qa_text_full||[],
    ['ts','level','logger_name','message']);
}

document.addEventListener('DOMContentLoaded',function(){
  // Theme first: Chart.defaults must be right before the first chart is built.
  applyChartTheme();
  const _tbtn=document.getElementById('theme-toggle');
  if(_tbtn){
    _tbtn.textContent = currentTheme()==='dark' ? '\u2600 Light' : '\u263e Dark';
    _tbtn.addEventListener('click',()=>setTheme(currentTheme()==='dark'?'light':'dark'));
  }
  // Follow the OS while the viewer has not stamped an explicit preference.
  if(window.matchMedia){
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change',()=>{
      let stamped=null; try{ stamped=localStorage.getItem('chdiag-theme'); }catch(e){}
      if(!stamped){ repaintSurfaceGaps(); }
    });
  }

  if(!DATA){document.body.innerHTML='<p style="padding:40px;color:red">No embedded data.</p>';return;}

  renderAlerts();
  renderQueryAnalysis();

  // Publish the sticky band's real height so anchor offsets and the
  // scroll-spy threshold follow it instead of hardcoding a guess. Re-measured
  // on resize because the header's meta column wraps at narrow widths.
  // Observe rather than measure once: the band's height changes AFTER this
  // runs (hdr-meta's two lines land below, nav links for optional sections
  // un-hide, the webfont swaps) and again whenever the viewport resizes and
  // the header's meta column rewraps. Measuring a single time read 104px for
  // a band that settles at ~123px, which left anchor jumps tucking each
  // heading under the nav.
  const topbar=document.querySelector('.topbar');
  let topbarH=124;
  function measureTopbar(){
    if(!topbar) return;
    const h=Math.round(topbar.getBoundingClientRect().height);
    if(h && h!==topbarH){
      topbarH=h;
      document.documentElement.style.setProperty('--topbar-h', h+'px');
    }
  }
  measureTopbar();
  if(window.ResizeObserver){
    new ResizeObserver(measureTopbar).observe(topbar);
  } else {
    window.addEventListener('resize', measureTopbar, {passive:true});
  }

  // nav active highlight on scroll
  const secs=[...document.querySelectorAll('section[id]')];
  const navLinks=[...document.querySelectorAll('nav a')];
  window.addEventListener('scroll',function(){
    let cur='';
    // A section counts as current once its top reaches the underside of the
    // band, so the highlight matches what the reader can actually see.
    const line=topbarH+8;
    secs.forEach(s=>{if(s.getBoundingClientRect().top<=line)cur=s.id;});
    navLinks.forEach(a=>{
      a.classList.toggle('active',a.getAttribute('href')==='#'+cur);
    });
  },{passive:true});

  // header
  document.getElementById('hdr-badge').innerHTML=
    '<span class="badge badge-'+esc(DATA.mode)+'">'+esc(DATA.mode)+'</span>';
  document.getElementById('hdr-meta').innerHTML=
    'Generated: '+esc(DATA.generated_at)+'<br>Version: '+esc(DATA.version||'N/A');

  // stats
  // esc() on both arguments: every caller currently passes a number, a
  // version string or a joined load average, so nothing renders differently
  // today — but a stat tile is a general helper and the escape-by-default
  // contract has to hold at the helper, not at each call site.
  const tile=(v,l)=>'<div class="stat-card"><div class="val'
    +(String(v).length>14?' val-sm':String(v).length>8?' val-md':'')
    +'">'+esc(v)+'</div><div class="lbl">'+esc(l)+'</div></div>';
  const sg=document.getElementById('stats-grid');
  sg.innerHTML=[
    // A stat tile is a hero figure: long strings (uptime, long versions) step
    // down a size instead of wrapping the headline onto three lines.
    tile(DATA.version||'N/A','Server Version'),
    tile(DATA.uptime||'N/A','Uptime'),
    tile(fmt(DATA.total_databases),'Databases'),
    tile(fmt(DATA.total_tables),'Tables'),
    tile(fmt(DATA.active_parts),'Active Parts'),
    tile(DATA.total_size||'N/A','Total Data Size'),
  ].join('');

  // ── Host facts (from host_info.json; absent when host-info was skipped) ──
  (function(){
    const hi=DATA.host_info;
    if(!hi)return;
    document.getElementById('host-block').style.display='';

    const os=hi.os||{}, cpu=hi.cpu||{}, mem=hi.memory||{};
    const gib=b=>b?(Number(b)/1073741824).toFixed(2)+' GiB':'—';
    const dur=sec=>{
      sec=Number(sec||0); if(!sec)return '—';
      const d=Math.floor(sec/86400),h=Math.floor(sec%86400/3600),m=Math.floor(sec%3600/60);
      return (d?d+'d ':'')+(h?h+'h ':'')+m+'m';
    };

    // The host's own headline numbers, appended to the existing KPI row so the
    // reader sees server and machine together.
    if(cpu.logical_cpus) sg.innerHTML+=tile(String(cpu.logical_cpus),'Logical CPUs');
    if(mem.total_bytes)  sg.innerHTML+=tile(gib(mem.total_bytes),'Host RAM');
    if(cpu.load_avg_1_5_15 && cpu.load_avg_1_5_15.length)
      sg.innerHTML+=tile(cpu.load_avg_1_5_15.join(' / '),'Load 1/5/15m');

    // Machine facts. A definition-style table rather than more tiles: these are
    // strings to read once, not magnitudes to compare.
    const osRows=[
      ['hostname',os.hostname],
      ['os',[os.distro,os.distro_version].filter(Boolean).join(' ')],
      ['kernel',os.kernel_version],
      ['architecture',os.arch],
      ['host uptime',dur(os.uptime_seconds)],
      ['cpu model',cpu.model_name],
      ['vector flags',(cpu.notable_flags||[]).join(', ')],
      ['memory total',gib(mem.total_bytes)],
      ['memory available',gib(mem.available_bytes)],
      ['page cache',gib(mem.cached_bytes)],
      ['swap total',gib(mem.swap_total_bytes)],
      ['collected at',hi.collected_at]
    ].filter(r=>r[1]!==undefined && r[1]!=='' && r[1]!==null)
     .map(r=>({setting:r[0],value:r[1]}));
    renderTable('tbl-host-os',osRows,['setting','value']);

    // Tunables carry a status, so they get an icon + word as well as a colour.
    const ICON={ok:'\u2713 ok',warning:'\u26a0 warning',info:'\u2013',unknown:'? unknown'};
    const checks=(DATA.host_checks||[]).map(c=>({
      setting:c.setting,
      value:c.value,
      status:ICON[c.status]||c.status,
      note:c.note||''
    }));
    renderTable('tbl-host-tunables',checks,['setting','value','status','note'],
      r=>String(r.status).indexOf('warning')>=0?'alert-row':'');


    const procs=(hi.top_processes_by_rss||[]).slice(0,10).map(pr=>({
      pid:pr.pid, rss:gib(pr.rss_bytes), threads:pr.threads, state:pr.state, command:pr.command
    }));
    renderTable('tbl-host-procs',procs,['pid','rss','threads','state','command']);

    // A section that could not be read is never left looking healthy.
    const notes=hi.notes||[];
    if(notes.length){
      // Notes are free text built from OS errors and paths — the one source
      // of the three that is not a fixed enum, so escape each before joining.
      document.getElementById('host-notes').innerHTML=
        '<div class="alert-skipped">\u2139 host facts partially unavailable: '+notes.map(esc).join('; ')+'</div>';
    }
  })();

  // ── Storage: size by database (horizontal bar) ───────────────────────────
  (function(){
    const rows=DATA.storage_by_db||[];
    if(!rows.length)return;
    mkChart(document.getElementById('chart-storage-db'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.database),
        datasets:[{
          label:'Compressed (bytes)',
          data:rows.map(r=>r.bytes_total),
          // Nominal categories: one series, one colour. A hue per bar would
          // re-encode bar length in colour and spend the identity channel on
          // nothing.
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4
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
    // A bar, not a doughnut: the reader's job here is comparing counts, and a
    // ring makes close values indistinguishable. It also sidesteps the
    // all-pairs colour cap a doughnut would impose.
    mkChart(document.getElementById('chart-engines'),{
      type:'bar',
      data:{labels:rows.map(r=>r.engine),
        datasets:[{label:'Tables',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false}},
        scales:{x:{beginAtZero:true,ticks:{precision:0}}}}
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
    mkChart(document.getElementById('chart-query-time'),{
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
    mkChart(document.getElementById('chart-query-kind'),{
      type:'bar',
      data:{labels:rows.map(r=>r.query_kind||'unknown'),
        datasets:[{label:'Count',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{y:{beginAtZero:true}}}
    });
  })();

  // ── Query: avg duration and avg memory by kind — TWO single-axis charts ──
  //
  // These were one chart with a second y-axis (ms on the left, MB on the
  // right). Two scales on one plot align arbitrarily, so the picture invents a
  // correlation the data does not contain. Same measures, one axis each.
  (function(){
    const rows=DATA.query_by_kind||[];
    if(!rows.length)return;
    const labels=rows.map(r=>r.query_kind||'unknown');
    const one=(canvas,label,vals,axis)=>mkChart(document.getElementById(canvas),{
      type:'bar',
      data:{labels:labels,datasets:[{label:label,data:vals,
        backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false}},
        scales:{y:{beginAtZero:true,title:{display:true,text:axis}}}}
    });
    one('chart-query-duration','Avg duration',rows.map(r=>r.avg_duration_ms),'ms');
    one('chart-query-memory','Avg memory',rows.map(r=>r.avg_memory_mb),'MB');
  })();

  // ── Deep dive: slowest queries (horizontal bar) ───────────────────────────
  (function(){
    const rows=DATA.query_slow||[];
    if(!rows.length)return;
    const labels=rows.map(r=>queryLabel(r,34));
    const ch=mkChart(document.getElementById('chart-slow-queries'),{
      type:'bar',
      data:{
        labels:labels,
        datasets:[
          {label:'Avg Duration (ms)',data:rows.map(r=>r.avg_duration_ms),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
          {label:'Max Duration (ms)',data:rows.map(r=>r.max_duration_ms),backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4}
        ]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'},tooltip:{callbacks:{
          title:ctx=>{
            const r=rows[ctx[0].dataIndex];
            return r.query_kind+' · '+r.user;
          },
          afterLabel:ctx=>{
            const r=rows[ctx.dataIndex];
            return['Executions: '+r.executions,'Errors: '+r.errors,
                   'Avg Read: '+r.avg_read_mb+' MB','Avg Mem: '+r.avg_memory_mb+' MB',
                   'hash: '+fullHash(r.hash)];
          }
        }}},
        scales:{x:{beginAtZero:true,title:{display:true,text:'milliseconds'}}}
      }
    });
    bindQueryPeek(ch,rows,'peek-slow-queries');
  })();

  // ── Deep dive: heaviest reads (horizontal bar) ────────────────────────────
  (function(){
    const rows=DATA.query_heavy||[];
    if(!rows.length)return;
    const labels=rows.map(r=>queryLabel(r,34));
    const ch=mkChart(document.getElementById('chart-heavy-reads'),{
      type:'bar',
      data:{
        labels:labels,
        datasets:[{
          label:'Avg Read (MB)',
          data:rows.map(r=>r.avg_read_mb),
          // Nominal categories: one series, one colour. A hue per bar would
          // re-encode bar length in colour and spend the identity channel on
          // nothing.
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{
          title:ctx=>{
            const r=rows[ctx[0].dataIndex];
            return r.query_kind+' · '+r.user;
          },
          label:ctx=>' Avg Read: '+ctx.raw+' MB',
          afterLabel:ctx=>{
            const r=rows[ctx.dataIndex];
            return['Executions: '+r.executions,'Total Read: '+r.total_read,
                   'Avg Duration: '+r.avg_duration_ms+' ms',
                   'hash: '+fullHash(r.hash)];
          }
        }}},
        scales:{x:{beginAtZero:true,title:{display:true,text:'MB per query (avg)'}}}
      }
    });
    bindQueryPeek(ch,rows,'peek-heavy-reads');
  })();

  // ── Deep dive: user activity (grouped bar) ────────────────────────────────
  (function(){
    const rows=DATA.query_by_user||[];
    if(!rows.length)return;
    mkChart(document.getElementById('chart-user-activity'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.user),
        datasets:[
          {label:'Executions',data:rows.map(r=>r.executions),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
          // Errors is a state, so it keeps the reserved critical token.
          {label:'Errors',data:rows.map(r=>r.error_count),backgroundColor:STATUS.critical,borderColor:STATUS.critical,borderWidth:1,borderRadius:4}
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
    ['query_kind','user','executions','avg_duration_ms','max_duration_ms','avg_read_mb','avg_memory_mb','errors','hash','sample_query'],
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
    mkChart(document.getElementById('chart-exceptions'),{
      type:'bar',
      data:{
        labels:rows.map(r=>'Code '+r.exception_code),
        datasets:[{label:'Count',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]
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
    stackWithGaps(d);
    mkChart(document.getElementById('chart-partlog-time'),{
      type:'bar',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true,ticks:{maxTicksLimit:14,maxRotation:45}},y:{stacked:true,beginAtZero:true}}}
    });
  })();

  // ── Part log: by type (horizontal bar) ───────────────────────────────────
  //
  // Was a doughnut over up to ~8 event types. Part events span orders of
  // magnitude (NewPart dwarfs everything), which a ring renders as one slice
  // and a sliver; bars keep the comparison readable and lift the three-slot
  // all-pairs colour cap that a ring imposes.
  (function(){
    const rows=[...(DATA.part_log_by_type||[])].sort((a,b)=>Number(b.count||0)-Number(a.count||0));
    if(!rows.length)return;
    mkChart(document.getElementById('chart-partlog-type'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.event_type),
        datasets:[{label:'Events',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]
      },
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>
          [' Events: '+fmt(Number(rows[ctx.dataIndex].count||0)),
           ' Size: '+(rows[ctx.dataIndex].total_size||'—')]}}},
        scales:{x:{beginAtZero:true}}}
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

    // Rows are now per-(dict, pod) — cloud mode uses clusterAllReplicas
    // so a dict loaded on 3 replicas shows 3 rows. Aggregate to per-dict
    // for the charts where rendering N copies would be misleading, but
    // keep the raw per-pod rows for the table.
    const byDict={};
    rows.forEach(r=>{
      const k=r.database+'.'+r.name;
      const entry=byDict[k]||(byDict[k]={
        key:k, database:r.database, name:r.name, type:r.type,
        bytes_max:0, bytes_max_human:'',
        lifetime_min:Number(r.lifetime_min||0), lifetime_max:Number(r.lifetime_max||0),
        statuses:{}, loaded_pods:0, total_pods:0
      });
      entry.total_pods++;
      entry.statuses[r.status]=(entry.statuses[r.status]||0)+1;
      if(r.status==='LOADED') entry.loaded_pods++;
      const b=Number(r.bytes_allocated||0);
      if(b>entry.bytes_max){ entry.bytes_max=b; entry.bytes_max_human=r.bytes_allocated_human||''; }
    });
    const dicts=Object.values(byDict);

    // Status pie — count of (dict × pod) slots per status. A dict
    // LOADED on 3 pods and NOT_LOADED on 1 contributes 3+1 slots, so
    // operators can see the cluster-wide loading coverage.
    const statusMap={};
    rows.forEach(r=>{statusMap[r.status]=(statusMap[r.status]||0)+1;});
    const statLabels=Object.keys(statusMap);
    // A bar, not a pie: with two or three states a ring is decoration, and the
    // count is what the operator reads. Fill stays on the reserved status ramp
    // and the state is spelled out on the axis, so nothing rides on hue alone.
    statLabels.sort((a,b)=>statusMap[b]-statusMap[a]);
    mkChart(document.getElementById('chart-dict-status'),{
      type:'bar',
      data:{
        labels:statLabels,
        datasets:[{label:'pod-slots',data:statLabels.map(s=>statusMap[s]),
          backgroundColor:statLabels.map(s=>DICT_STATUS_COLOR[s]||STATUS.neutral),
          borderColor:statLabels.map(s=>DICT_STATUS_COLOR[s]||STATUS.neutral),
          borderWidth:1,borderRadius:4}]
      },
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{
          label:ctx=>' '+ctx.parsed.x+' pod-slots'}}},
        scales:{x:{beginAtZero:true,ticks:{precision:0}}}}
    });

    // Bytes allocated — max across pods per dict (same dict has very
    // similar bytes on every pod that loaded it; using max avoids
    // visually shrinking a dict that's NOT_LOADED on one replica).
    const topDicts=[...dicts].sort((a,b)=>b.bytes_max-a.bytes_max).slice(0,15);
    mkChart(document.getElementById('chart-dict-bytes'),{
      type:'bar',
      data:{
        labels:topDicts.map(d=>d.key),
        datasets:[{
          label:'max bytes_allocated across pods',
          data:topDicts.map(d=>d.bytes_max),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>{
          const d=topDicts[ctx.dataIndex];
          return [' '+d.bytes_max_human, ' loaded on '+d.loaded_pods+' / '+d.total_pods+' pods'];
        }}}},
        scales:{x:{ticks:{callback:v=>v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
      }
    });

    // Lifetime is per-definition (same value on every pod), so use the
    // deduped per-dict view.
    const lifeRows=dicts.filter(d=>d.lifetime_max>0||d.lifetime_min>0).slice(0,20);
    if(lifeRows.length){
      mkChart(document.getElementById('chart-dict-lifetime'),{
        type:'bar',
        data:{
          labels:lifeRows.map(d=>d.name),
          datasets:[
            {label:'Lifetime Min (s)',data:lifeRows.map(d=>d.lifetime_min),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
            {label:'Lifetime Max (s)',data:lifeRows.map(d=>d.lifetime_max),backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4}
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

    // Dictionary detail table — per-(dict, pod) rows so the operator
    // can see which pod each runtime stat came from. Columns ordered:
    // identity → topology → sizing → activity → schema → lifecycle →
    // errors → metadata. Long-text columns last; the table wrapper
    // already overflow-x's, so horizontal scroll is expected.
    const el=document.getElementById('tbl-dicts');
    const cols=['hostname','database','name','status','type','source',
                'bytes_allocated_human','element_count','load_factor',
                'query_count','hit_rate_pct','found_rate_pct','error_count',
                'key_names','key_types','attribute_names','attribute_types',
                'lifetime_min','lifetime_max',
                'loading_start_time','last_update','loading_duration_s',
                'last_exception','origin','comment','uuid'];
    let h='<table class="dt"><thead><tr>'+cols.map(k=>'<th>'+esc(k)+'</th>').join('')+'</tr></thead><tbody>';
    rows.forEach(r=>{
      const isFailed=r.status==='FAILED'||String(r.last_exception||'').length>1;
      h+='<tr'+(isFailed?' class="error-row"':r.status==='LOADING'?' class="alert-row"':'')+'>';
      cols.forEach(k=>{
        // name / last_exception / origin / comment / source are customer data.
        if(k==='status') h+='<td>'+dictStatusBadge(r[k])+'</td>';
        else h+='<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>';
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
    // Delay bands are ORDERED, so they take a one-hue ordinal ramp. The old
    // green→yellow→red rainbow spent five hues re-encoding an order the axis
    // already spells out, and read as a status scale it is not.
    const bColors=(ORDINAL[currentTheme()]||ORDINAL.light).slice(0,bLabels.length);
    mkChart(document.getElementById('chart-replica-delay'),{
      type:'bar',
      data:{labels:bLabels,datasets:[{label:'Replicas',data:bData,
        backgroundColor:bColors,borderColor:bColors,borderWidth:1,borderRadius:4,_ordinal:true}]},
      options:{responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false}},scales:{y:{beginAtZero:true,ticks:{stepSize:1}}}}
    });

    // queue size horizontal bar (top 15)
    const topQ=[...rows].sort((a,b)=>Number(b.queue_size||0)-Number(a.queue_size||0)).slice(0,15);
    if(topQ.some(r=>Number(r.queue_size||0)>0)){
      mkChart(document.getElementById('chart-replica-queue'),{
        type:'bar',
        data:{
          labels:topQ.map(r=>r.database+'.'+r.table),
          datasets:[
            {label:'Queue Size',data:topQ.map(r=>r.queue_size||0),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
            {label:'Inserts In Queue',data:topQ.map(r=>r.inserts_in_queue||0),backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4},
            {label:'Merges In Queue',data:topQ.map(r=>r.merges_in_queue||0),backgroundColor:C[2],borderColor:C[2],borderWidth:1,borderRadius:4}
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
      mkChart(document.getElementById('chart-disk-usage'),{
        type:'bar',
        data:{
          // In cloud mode system.disks fans out, so the same disk appears
          // once per replica (measured 11 disks → 33 rows on 3 replicas).
          // Qualify the label with the short hostname, matching the
          // hostname column the table below now shows — otherwise the
          // chart is 33 bars under 11 duplicate labels.
          labels:rows.map(r=>r.hostname?r.name+' @ '+String(r.hostname).split('.')[0]:r.name),
          datasets:[
            {label:'Used',data:rows.map(r=>Number(r.total_space||0)-Number(r.free_space||0)),
              backgroundColor:rows.map(r=>Number(r.free_pct||100)<CRIT_PCT?STATUS.critical:Number(r.free_pct||100)<WARN_PCT?STATUS.warning:C[0]),
              borderColor:rows.map(r=>Number(r.free_pct||100)<CRIT_PCT?STATUS.critical:Number(r.free_pct||100)<WARN_PCT?STATUS.warning:C[0]),
              borderWidth:1,borderRadius:4},
            {label:'Free',data:rows.map(r=>Number(r.free_space||0)),
              // Free space is the remainder, not a "good" state — recessive gray.
              backgroundColor:OTHER,borderColor:OTHER,borderWidth:1,borderRadius:4}
          ]
        },
        options:{
          responsive:true,maintainAspectRatio:false,
          interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'},tooltip:{callbacks:{
            label:ctx=>{
              const r=rows[ctx.dataIndex];
              const host=r.hostname?' ('+String(r.hostname).split('.')[0]+')':'';
              if(ctx.datasetIndex===0)return' Used: '+r.total_space_human+' total, '+r.free_pct+'% free'+host;
              return' Free: '+r.free_space_human+host;
            }
          }}},
          scales:{x:{stacked:true},y:{stacked:true,beginAtZero:true,
            ticks:{callback:v=>v>=1e12?(v/1e12).toFixed(1)+'T':v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
        }
      });
    }
    renderTable('tbl-disks',rows,
      ['hostname','name','type','free_space_human','total_space_human','free_pct','path'],
      r=>Number(r.free_pct||100)<CRIT_PCT?'error-row':Number(r.free_pct||100)<WARN_PCT?'alert-row':'');
  })();

  // ── Server errors ─────────────────────────────────────────────────────────
  (function(){
    const rows=(DATA.server_errors||[]).slice(0,20);
    if(rows.length){
      mkChart(document.getElementById('chart-server-errors'),{
        type:'bar',
        data:{
          labels:rows.map(r=>r.name||'code '+r.code),
          datasets:[{label:'Total count (since restart)',
            data:rows.map(r=>r.value),
            backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]
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

  // ── Logs ──────────────────────────────────────────────────────────────────
  //
  // Bounded structured rows searchable in-page; the complete raw files are
  // linked under Collected Files. Paginated because the row cap bounds the
  // payload, not the DOM — 1000 rows of <tr> is what makes a page crawl.
  (function(){
    const rows=DATA.text_log||[];
    if(!rows.length) return;
    document.getElementById('sec-logs').style.display='';
    document.getElementById('nav-logs').style.display='';

    const cap=DATA.text_log_row_cap||rows.length;
    document.getElementById('logs-scope').textContent=
      'Warning and worse (Warning, Error, Critical, Fatal) from system.text_log, last 24 h, newest first'
      +(rows.length>=cap?' — capped at the most recent '+cap+' rows; the complete raw files are under Collected Files.':'.');

    const sel=document.getElementById('log-level-filter');
    [...new Set(rows.map(r=>r.level))].sort().forEach(l=>{
      sel.innerHTML+='<option value="'+esc(l)+'">'+esc(l)+'</option>';
    });

    const PAGE=100;
    let filtered=rows.slice(), page=0;
    const BADGE={Fatal:'err-badge',Critical:'err-badge',Error:'err-badge',Warning:'warn-badge'};
    // Level is a state: status ramp plus the word itself, never colour alone.
    const cell=r=>({
      event_time:r.event_time,
      level:'<span class="'+(BADGE[r.level]||'warn-badge')+'">'+esc(r.level)+'</span>',
      logger_name:r.logger_name,
      message:r.message
    });

    function render(){
      const cnt=document.getElementById('log-count');
      const pg=document.getElementById('log-pagination');
      cnt.textContent=filtered.length+' / '+rows.length+' entries';
      if(!filtered.length){
        document.getElementById('tbl-logs').innerHTML='<p class="no-data">No entries match</p>';
        pg.innerHTML=''; return;
      }
      const start=page*PAGE;
      renderTable('tbl-logs',filtered.slice(start,start+PAGE).map(cell),
        ['event_time','level','logger_name','message'],null,['level']);
      const total=Math.ceil(filtered.length/PAGE);
      if(total<=1){pg.innerHTML='';return;}
      let h='';
      if(page>0) h+='<button onclick="window._logPg('+(page-1)+')">&#9664;</button>';
      h+='<span class="cur">'+(page+1)+' / '+total+'</span>';
      if(page<total-1) h+='<button onclick="window._logPg('+(page+1)+')">&#9654;</button>';
      pg.innerHTML=h;
    }
    window._logPg=function(pp){page=pp;render();};
    window.logsFilter=function(){
      const q=(document.getElementById('log-search').value||'').toLowerCase();
      const lvl=document.getElementById('log-level-filter').value;
      filtered=rows.filter(r=>{
        if(lvl && r.level!==lvl) return false;
        if(q) return String(r.message||'').toLowerCase().includes(q)
                 || String(r.logger_name||'').toLowerCase().includes(q);
        return true;
      });
      page=0; render();
    };
    render();
  })();

  // ── Collected files ───────────────────────────────────────────────────────
  //
  // An index of the rest of the bundle, not a copy of it. Every artifact the
  // run wrote gets a row and a relative link; the browser opens the file.
  (function(){
    const files=DATA.bundle_files||[];
    if(!files.length) return;
    document.getElementById('sec-files').style.display='';
    document.getElementById('nav-files').style.display='';

    const total=files.reduce((a,f)=>a+Number(f.bytes||0),0);
    const human=b=>{
      const u=['B','KiB','MiB','GiB','TiB']; let i=0; b=Number(b)||0;
      while(b>=1024 && i<u.length-1){b/=1024;i++;}
      return (i?b.toFixed(1):b)+' '+u[i];
    };
    document.getElementById('files-scope').textContent=
      files.length+' files collected, '+human(total)+' in total.';

    const sel=document.getElementById('file-group-filter');
    // '' is the 'All folders' sentinel, so the root group gets '/' as its
    // option value or selecting '(bundle root)' would filter nothing.
    [...new Set(files.map(f=>f.group))].sort().forEach(g=>{
      sel.innerHTML+='<option value="'+esc(g||'/')+'">'+esc(g||'(bundle root)')+'</option>';
    });

    let filtered=files.slice();
    // The link is markup this file builds; the filename inside it is escaped,
    // and each path segment is percent-encoded so a space, '#' or '?' in a
    // name cannot break the href (encodeURI leaves '#'/'?' alone — they would
    // become a bogus fragment/query and 404 against the extracted bundle).
    const row=f=>({
      file:'<a href="'+f.href.split('/').map(encodeURIComponent).join('/')+'" target="_blank" rel="noopener">'+esc(f.file)+'</a>',
      kind:f.kind||'\u2014',
      size:f.size
    });

    function render(){
      document.getElementById('file-count').textContent=
        filtered.length+' / '+files.length+' files';
      renderTable('tbl-files',filtered.map(row),['file','kind','size'],null,['file']);
    }
    window.filesFilter=function(){
      const q=(document.getElementById('file-search').value||'').toLowerCase();
      const g=document.getElementById('file-group-filter').value;
      filtered=files.filter(f=>{
        if(g!=='' && f.group!==(g==='/'?'':g)) return false;
        return !q || String(f.file).toLowerCase().includes(q);
      });
      render();
    };
    render();
  })();

  // ── Async inserts (non-gov only) ──────────────────────────────────────────
  (function(){
    const rows=DATA.async_inserts||[];
    if(!rows.length)return;
    document.getElementById('sec-async-inserts').style.display='';
    document.getElementById('nav-async-inserts').style.display='';
    const d=pivot(rows,'hour','status','flushes');
    stackWithGaps(d);
    mkChart(document.getElementById('chart-async-inserts'),{
      type:'bar',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true,ticks:{maxRotation:45}},y:{stacked:true,beginAtZero:true}}}
    });
    renderTable('tbl-async-inserts',rows,
      ['hour','status','flushes','total_rows','total_bytes','avg_flush_ms']);
  })();
});
</script>
</body>
</html>`
