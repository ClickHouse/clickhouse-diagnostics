package query

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SharedSystemTables lists system.* tables whose contents are the same on
// every replica — both in ClickHouse Cloud (SharedMergeTree, shared storage)
// and in self-managed ReplicatedMergeTree clusters. Wrapping these with
// clusterAllReplicas duplicates every row once per replica and produces
// inflated counts / repeated alert rows.
//
// This is the single source of truth used by the alert evaluator, the
// dashboard generator, and the new query-analysis collector. If you
// add a system table to one of those code paths, update this map.
var SharedSystemTables = map[string]bool{
	"columns":           true,
	"databases":         true,
	"detached_parts":    true,
	"dictionaries":      true,
	"mutations":         true,
	"parts":             true,
	"replicas":          true,
	"replication_queue": true,
	"tables":            true,
}

// SysTable returns the mode-appropriate system table reference.
// In cloud mode, per-replica tables (query_log, part_log, text_log,
// errors, …) are wrapped with clusterAllReplicas so the result spans
// every replica. Tables in SharedSystemTables are queried directly.
func SysTable(mode, table string) string {
	if strings.ToLower(mode) == "cloud" && !SharedSystemTables[table] {
		return fmt.Sprintf("clusterAllReplicas(default, system.%s)", table)
	}
	return "system." + table
}

// Vars holds the substitution values for a query template. All fields
// are optional; the Apply function only replaces placeholders whose
// matching field is non-zero.
//
// Validation is the caller's responsibility — Vars values are spliced
// directly into the SQL string. Use the package-level Validate*
// helpers (or the equivalent in cmd/main.go) before populating Vars.
type Vars struct {
	// Mode selects the ClickHouse topology — "cloud" / "onprem" / "gov".
	// Required for {sys.<table>} substitution to pick the right reference.
	Mode string

	// QueryID, when non-empty, replaces every {query_id} placeholder
	// with the quoted UUID 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'.
	QueryID string

	// NormalizedQueryHash, when non-empty, replaces every
	// {normalized_query_hash} placeholder with the bare numeric value.
	NormalizedQueryHash string

	// From / To, when non-zero, replace {from} / {to} placeholders with
	// quoted ClickHouse DateTime literals in UTC.
	From time.Time
	To   time.Time

	// GovSalt, when non-empty, replaces every '%salt%' literal in the
	// SQL with the customer-supplied salt for gov-mode hashing.
	GovSalt string
}

// Apply substitutes every placeholder in sql whose value is present in
// vars. Placeholders supported (case-sensitive):
//
//	{sys.<table>}             — mode-appropriate system table reference
//	{query_id}                — 'uuid' (quoted)
//	{normalized_query_hash}   — 12345 (unquoted numeric)
//	{from}, {to}              — 'YYYY-MM-DD HH:MM:SS' UTC (quoted)
//	'%salt%'                  — '<gov-salt>'
//
// Placeholders whose corresponding Vars field is empty are left intact
// — the executor's empty-placeholder check then refuses to send the
// query, preventing leakage of unbound templates to the server.
func Apply(sql string, vars Vars) string {
	sql = expandSysPlaceholders(sql, vars.Mode)

	if vars.QueryID != "" {
		sql = strings.ReplaceAll(sql, "{query_id}", "'"+vars.QueryID+"'")
	}
	if vars.NormalizedQueryHash != "" {
		sql = strings.ReplaceAll(sql, "{normalized_query_hash}", vars.NormalizedQueryHash)
	}
	if !vars.From.IsZero() {
		sql = strings.ReplaceAll(sql, "{from}",
			"'"+vars.From.UTC().Format("2006-01-02 15:04:05")+"'")
	}
	if !vars.To.IsZero() {
		sql = strings.ReplaceAll(sql, "{to}",
			"'"+vars.To.UTC().Format("2006-01-02 15:04:05")+"'")
	}
	if vars.GovSalt != "" {
		sql = strings.ReplaceAll(sql, "'%salt%'", "'"+vars.GovSalt+"'")
	}
	return sql
}

// expandSysPlaceholders walks the SQL and replaces every {sys.<table>}
// occurrence with SysTable(mode, table). Hand-rolled (no regex) because
// the placeholder shape is fixed and trivial — keeps the dependency
// surface in this package small.
func expandSysPlaceholders(sql, mode string) string {
	out := sql
	for {
		start := strings.Index(out, "{sys.")
		if start == -1 {
			return out
		}
		end := strings.Index(out[start:], "}")
		if end == -1 {
			return out
		}
		table := out[start+5 : start+end]
		out = strings.ReplaceAll(out, out[start:start+end+1], SysTable(mode, table))
	}
}

// reBoundPlaceholder matches our known placeholder shape — a name
// composed of ASCII letters, digits, dots, and underscores, between
// braces. This deliberately does NOT match the empty `{}` form, so
// ClickHouse log-format strings like
//   'Selected {}/{} parts by partition key, …'
// — which appear as literal values in some queries — are left alone.
var rePlaceholder = regexp.MustCompile(`\{[A-Za-z0-9_.]+\}`)

// UnboundPlaceholders returns any named {…} placeholders left in sql
// after Apply. Used by the executor as a safety check: if a required
// value (e.g. {query_id}) was never bound, the SQL would reach the
// server with a literal '{query_id}' and either parse-error or — worse
// — return zero rows silently. Better to refuse the query.
//
// Empty `{}` braces (the ClickHouse log-format placeholder) are
// ignored. '%salt%' is intentionally NOT flagged here: any .sql file
// under queries.gov/ contains it as a marker and the executor only
// substitutes when GovSalt is set (gov mode), so its presence after
// Apply is expected and not an error in non-gov modes.
func UnboundPlaceholders(sql string) []string {
	matches := rePlaceholder.FindAllString(sql, -1)
	if len(matches) == 0 {
		return nil
	}
	// Stable de-dup so identical unbound placeholders don't repeat
	// in the operator-facing warning.
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}
