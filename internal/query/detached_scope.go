package query

import (
	"fmt"
	"strings"
)

// Detached parts on a large SharedMergeTree server.
//
// system.detached_parts lists the detached/ directory of every table — one
// listing per table, a remote call on object-storage disks whose metadata
// lives in Keeper (seen at 20–300 s per run, 0 rows found, on servers with
// tens of thousands of tables). On SharedMergeTree, broken parts showed
// first in logs and errors in every escalation reviewed, so on such a server
// an auto or full run lists every non-shared table plus the shared tables
// that broken-part log lines name; -collect full lists everything.

// SignalTablesQuery finds tables named in recent broken-part log lines
// (logger_name starts with "<database>.<table>").
func SignalTablesQuery(mode string) string {
	src := "system.text_log"
	if mode == "cloud" {
		src = "clusterAllReplicas(default, system.text_log)"
	}
	return fmt.Sprintf(`SELECT DISTINCT
    extract(logger_name, '^([^.\\s]+)\\.')       AS db,
    extract(logger_name, '^[^.\\s]+\\.([^\\s]+)') AS tbl
FROM %s
WHERE event_date >= toDate(now() - INTERVAL 1 DAY) AND event_time > now() - INTERVAL 1 DAY
  AND level IN ('Fatal', 'Critical', 'Error', 'Warning')
  AND (message ILIKE '%%broken%%part%%' OR message ILIKE '%%unexpected%%part%%' OR message ILIKE '%%detach%%part%%')
  AND db != '' AND tbl != ''
LIMIT 1000
FORMAT TSV`, src)
}

// ParseSignalTables reads SignalTablesQuery's TSV, dropping malformed rows
// and duplicates.
func ParseSignalTables(tsv string) [][2]string {
	var out [][2]string
	seen := map[[2]string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(tsv), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 || f[0] == "" || f[1] == "" {
			continue
		}
		k := [2]string{f[0], f[1]}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// DetachedScope is the {detached_scope} predicate: "1" (every table) unless
// limit is set, in which case every non-shared table plus the named ones.
// qualifier prefixes the columns ("p." in gov files, where database is the
// hashed alias).
func DetachedScope(limit bool, tables [][2]string, qualifier string) string {
	if !limit {
		return "1"
	}
	cols := fmt.Sprintf("(%sdatabase, %stable)", qualifier, qualifier)
	s := cols + " IN (SELECT database, name FROM system.tables WHERE engine NOT LIKE 'Shared%')"
	if len(tables) > 0 {
		lits := make([]string, 0, len(tables))
		for _, t := range tables {
			lits = append(lits, fmt.Sprintf("(%s, %s)", sqlString(t[0]), sqlString(t[1])))
		}
		s += " OR " + cols + " IN (" + strings.Join(lits, ", ") + ")"
	}
	return s
}

func sqlString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
