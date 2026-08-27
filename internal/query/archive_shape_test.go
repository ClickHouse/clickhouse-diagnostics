package query

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A plain `ARRAY JOIN tables` drops every query_log row whose `tables` array is
// empty — which is precisely the failure class the archive exists for: a query
// that threw before it resolved a table (UNKNOWN_TABLE 60, parse errors 6/27)
// has no tables and vanishes. Observed against a real bundle: the archived
// JSONL held exception codes 395 and 241 only, while the live dashboard query
// (no array join) reported 395, 241, 6 and 60.
func TestQueryLogDetails_UsesLeftArrayJoin(t *testing.T) {
	plainArrayJoin := regexp.MustCompile(`(?i)(^|[^T]\s)ARRAY\s+JOIN\s+tables`)

	var seen int
	for _, path := range realRepoSQLFiles(t) {
		if !strings.HasPrefix(filepath.Base(path), "system.query_log_details") {
			continue
		}
		seen++
		body := readFileForTest(t, path)
		if !strings.Contains(strings.ToUpper(body), "LEFT ARRAY JOIN TABLES") {
			t.Errorf("%s: must use LEFT ARRAY JOIN tables, or zero-table failures are dropped", path)
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			if plainArrayJoin.MatchString(line) {
				t.Errorf("%s: bare ARRAY JOIN tables on line %q", path, strings.TrimSpace(line))
			}
		}
	}
	if seen == 0 {
		t.Fatal("no system.query_log_details*.sql files found — test is not guarding anything")
	}
}

// part_name is unique per part, so grouping on it makes the "aggregated"
// part_log a row-for-row copy of the source table: 868 rows for 868 events on a
// toy dataset, millions over 7 days of production traffic. It may be sampled
// with any(part_name) but must never be a group key.
func TestPartLog_DoesNotGroupByPartName(t *testing.T) {
	var seen int
	for _, path := range realRepoSQLFiles(t) {
		if !strings.HasPrefix(filepath.Base(path), "system.part_log") {
			continue
		}
		seen++
		body := readFileForTest(t, path)

		// Bare `part_name,` in the projection = an implicit group key under
		// GROUP BY ALL and an explicit one everywhere else.
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			if trimmed == "part_name," || trimmed == "part_name" {
				t.Errorf("%s: part_name is projected bare (group key); use any(part_name) AS part_name", path)
			}
		}

		// And never in an explicit GROUP BY key list.
		if idx := strings.LastIndex(strings.ToUpper(body), "GROUP BY"); idx >= 0 {
			keys := body[idx:]
			if strings.Contains(keys, "part_name") {
				t.Errorf("%s: part_name appears in the GROUP BY key list", path)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no system.part_log*.sql files found — test is not guarding anything")
	}
}
