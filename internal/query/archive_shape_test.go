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

// Free-text columns must be truncated with leftUTF8, never with left.
//
// left() counts BYTES. A cut that lands inside a multi-byte character emits a
// lone continuation byte, and the collector writes ClickHouse's response to
// disk verbatim (executor.go), so nothing downstream repairs it: the .jsonl
// line stops being valid UTF-8. Strict readers reject it outright — Python's
// json raises "invalid continuation byte" on the whole line — while jq is
// lenient and silently substitutes U+FFFD, corrupting the text just as badly
// but without saying so. Reachable with any non-ASCII literal in customer SQL
// ('München', CJK identifiers), which is ordinary rather than exotic.
//
// leftUTF8 counts code points and is registered next to left in 22.8, so it
// works at the support floor and needs no version gate.
func TestArchiveQueries_TruncateFreeTextWithLeftUTF8(t *testing.T) {
	// Documented exemptions, keyed by repo-relative path. Both reasons are
	// about the VALUE never reaching the output as raw truncated text, not
	// about the truncation being safe in general.
	exempt := map[string]string{
		// last_error_trace is Array(UInt64) stringified to bracketed ASCII
		// digits, so there is no multi-byte character to split.
		"queries.onprem/system.errors.sql": "toString(Array(UInt64)) is ASCII",
		"queries.cloud/system.errors.sql":  "toString(Array(UInt64)) is ASCII",
		// gov feeds the truncated message into SHA256 and emits hex, so an
		// invalid byte cannot survive into the archive.
		"queries.gov/system.text_log.sql": "truncated value is hashed to hex",
	}

	// Matches left( but not leftUTF8( — \b would not help, since "left" is a
	// prefix of "leftUTF8".
	bareLeft := regexp.MustCompile(`(?i)\bleft\s*\(`)

	var seen int
	for _, path := range realRepoSQLFiles(t) {
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
		if _, ok := exempt[rel]; ok {
			continue
		}
		seen++
		for i, line := range strings.Split(readFileForTest(t, path), "\n") {
			// Strip comments — whole-line AND trailing. These files discuss
			// left() vs leftUTF8() at length and that prose must not match.
			if j := strings.Index(line, "--"); j >= 0 {
				line = line[:j]
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			// Remove every leftUTF8( first, so the remainder can only contain
			// a byte-wise left(.
			stripped := strings.ReplaceAll(strings.ReplaceAll(line, "leftUTF8(", ""), "leftUTF8 (", "")
			if bareLeft.MatchString(stripped) {
				t.Errorf("%s:%d: truncates with byte-wise left() — use leftUTF8() or the "+
					"archived .jsonl can hold invalid UTF-8: %q",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
	if seen == 0 {
		t.Fatal("no non-exempt .sql files found — test is not guarding anything")
	}
}
