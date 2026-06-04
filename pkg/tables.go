package pkg

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// reSystemTable matches direct `system.<table>` references anywhere in
// a SQL string. The word-boundary at the end keeps it from matching
// `system.parts_something` partially — `[a-z_]+` is greedy.
var reSystemTable = regexp.MustCompile(`(?i)\bsystem\.[a-z_][a-z0-9_]*\b`)

// reMergeTable matches the `merge(<db>, '<pattern>')` table function
// used by a couple of queries that aggregate across versioned tables
// (e.g. clusterAllReplicas wrapping `merge(system, '^asynchronous_insert_log')`).
// The table name itself is a regex pattern, so we just report it
// verbatim — the operator reading the dry-run knows what to read.
var reMergeTable = regexp.MustCompile(`(?i)merge\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*,\s*'([^']+)'\s*\)`)

// ExtractTables returns a sorted, de-duplicated list of system table
// references found inside sql. It covers:
//
//   - Direct references:           system.parts, system.query_log, …
//   - clusterAllReplicas wrappers: extracted via the inner system.X
//     (this regex doesn't care about the outer call; it matches the
//     inner reference directly).
//   - merge() patterns:            "<db>.* matching '<pattern>'"
//
// Used by --dry-run to label each printed query with the system tables
// it would touch. Best-effort — a hand-rolled SQL parser is overkill
// for the queries this tool generates.
func ExtractTables(sql string) []string {
	seen := map[string]struct{}{}
	for _, m := range reSystemTable.FindAllString(sql, -1) {
		seen[strings.ToLower(m)] = struct{}{}
	}
	for _, m := range reMergeTable.FindAllStringSubmatch(sql, -1) {
		seen[fmt.Sprintf("%s.* matching '%s'", strings.ToLower(m[1]), m[2])] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
