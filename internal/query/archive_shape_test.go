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

// part_name is not collected by the part_log aggregates at all, and this
// guards both of the ways it could come back.
//
// As a GROUP BY key it is fatal: part_name is unique per part, so keying on it
// makes the "aggregate" a row-for-row copy of the source table — 868 rows for
// 868 events on a toy dataset, millions over 7 days of production traffic.
//
// Sampled with any(part_name) it is merely useless: one arbitrary part out of
// however many the group covers, with nothing to say why that one was picked.
// It is not a handle anyone can troubleshoot from, so it was dropped outright
// rather than demoted to a sample.
func TestPartLog_DoesNotCollectPartName(t *testing.T) {
	var seen int
	for _, path := range realRepoSQLFiles(t) {
		if !strings.HasPrefix(filepath.Base(path), "system.part_log") {
			continue
		}
		seen++

		// Strip comment text before matching — whole-line AND trailing. Those
		// files explain at length WHY part_name is absent, and that prose must
		// not read as a projection. Same approach as gov_leak_test.go.
		var code []string
		for _, line := range strings.Split(readFileForTest(t, path), "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				code = append(code, trimmed)
			}
		}

		if strings.Contains(strings.Join(code, "\n"), "part_name") {
			t.Errorf("%s: part_name must not be collected — as a group key it copies "+
				"the source table row-for-row, and any(part_name) is an arbitrary "+
				"sample nobody can act on", path)
		}
	}
	if seen == 0 {
		t.Fatal("no system.part_log*.sql files found — test is not guarding anything")
	}
}
