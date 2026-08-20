package query

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWindowDefaultSQL(t *testing.T) {
	cases := []struct {
		spec string
		want string
		ok   bool
	}{
		{"now", "now()", true},
		{"1d", "now() - INTERVAL 1 DAY", true},
		{"15d", "now() - INTERVAL 15 DAY", true},
		{"12h", "now() - INTERVAL 12 HOUR", true},
		{"30m", "now() - INTERVAL 30 MINUTE", true},
		// Rejected: these must fail closed rather than widen a window.
		{"7dd", "", false},
		{"d", "", false},
		{"7", "", false},
		{"0d", "", false},
		{"-1d", "", false},
		{"7y", "", false},
		{"", "", false},
		{"NOW", "", false},
	}
	for _, c := range cases {
		got, ok := windowDefaultSQL(c.spec)
		if ok != c.ok {
			t.Errorf("windowDefaultSQL(%q) ok = %v, want %v", c.spec, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("windowDefaultSQL(%q) = %q, want %q", c.spec, got, c.want)
		}
	}
}

// TestWindowDefaultExpansion checks the template -> SQL mapping for the
// shapes the shipped queries actually use.
func TestWindowDefaultExpansion(t *testing.T) {
	cases := []struct {
		templated string
		original  string
	}{
		{
			"WHERE (event_time > {from:7d} AND event_time <= {to:now})",
			"WHERE (event_time > now() - INTERVAL 7 DAY AND event_time <= now())",
		},
		{
			"WHERE event_time > {from:7d} AND event_time <= {to:now}",
			"WHERE event_time > now() - INTERVAL 7 DAY AND event_time <= now()",
		},
		{
			"WHERE event_date >= toDate({from:1d}) AND event_date <= toDate({to:now})",
			"WHERE event_date >= toDate(now() - INTERVAL 1 DAY) AND event_date <= toDate(now())",
		},
	}
	for _, c := range cases {
		got := Apply(c.templated, Vars{}) // no flags -> defaults
		if got != c.original {
			t.Errorf("default expansion changed the window:\n got: %s\nwant: %s", got, c.original)
		}
	}
}

func TestWindowFlagsOverrideDefaults(t *testing.T) {
	from := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	to := time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC)

	got := Apply("WHERE event_time > {from:15d} AND event_time <= {to:now}", Vars{From: from, To: to})
	want := "WHERE event_time > '2026-03-01 10:30:00' AND event_time <= '2026-03-02 11:00:00'"
	if got != want {
		t.Errorf("both flags:\n got: %s\nwant: %s", got, want)
	}

	// One end supplied: the other must still fall back to its default,
	// not to a zero timestamp (which would select nothing).
	got = Apply("WHERE event_time > {from:7d} AND event_time <= {to:now}", Vars{From: from})
	want = "WHERE event_time > '2026-03-01 10:30:00' AND event_time <= now()"
	if got != want {
		t.Errorf("only --from:\n got: %s\nwant: %s", got, want)
	}

	got = Apply("WHERE event_time > {from:7d} AND event_time <= {to:now}", Vars{To: to})
	want = "WHERE event_time > now() - INTERVAL 7 DAY AND event_time <= '2026-03-02 11:00:00'"
	if got != want {
		t.Errorf("only --to:\n got: %s\nwant: %s", got, want)
	}
}

// TestMalformedWindowIsRefused: a typo must not silently become an
// unbounded scan. It stays in the SQL and is reported as unbound, which
// makes the executor refuse the query.
func TestMalformedWindowIsRefused(t *testing.T) {
	sql := Apply("WHERE event_time > {from:7dd}", Vars{})
	if !strings.Contains(sql, "{from:7dd}") {
		t.Fatalf("malformed spec was silently replaced: %s", sql)
	}
	unbound := UnboundPlaceholders(sql)
	if len(unbound) == 0 {
		t.Fatal("malformed window placeholder was not reported as unbound; " +
			"the executor would send a literal brace to the server")
	}
}

// TestLogFormatBracesUntouched guards the reason rePlaceholder is narrow:
// some queries match ClickHouse log messages containing {} braces.
func TestLogFormatBracesUntouched(t *testing.T) {
	sql := "SELECT 1 WHERE message LIKE 'Selected {}/{} parts by partition key'"
	if got := Apply(sql, Vars{}); got != sql {
		t.Errorf("log-format braces were rewritten:\n got: %s\nwant: %s", got, sql)
	}
	if u := UnboundPlaceholders(sql); len(u) != 0 {
		t.Errorf("log-format braces reported as unbound: %v", u)
	}
}

// TestShippedQueriesHaveNoHardcodedWindow pins the change against the real
// repo: a new query that hard-codes its own interval would silently ignore
// --from/--to.
func TestShippedQueriesHaveNoHardcodedWindow(t *testing.T) {
	// toIntervalHour(...) is bucket granularity in a GROUP BY, not a
	// window filter, so it is allowed.
	bad := regexp.MustCompile(`(?i)INTERVAL\s+\d+\s+(DAY|HOUR|MINUTE)|toIntervalDay\(\s*\d|today\(\)\s*-\s*\d`)

	var files []string
	for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.EqualFold(filepath.Ext(p), ".sql") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("query directories not present")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if m := bad.FindString(string(b)); m != "" {
			t.Errorf("%s hard-codes a time window (%q); use {from:<default>} / "+
				"{to:now} so --from/--to can override it", f, m)
		}
	}
}

// TestShippedQueryWindowsAllExpand makes sure every default spec actually
// present in the repo is one windowDefaultSQL accepts — a typo would
// otherwise only surface at collection time, against a live server.
func TestShippedQueryWindowsAllExpand(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.EqualFold(filepath.Ext(p), ".sql") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("query directories not present")
	}
	seen := 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, m := range reWindowPlaceholder.FindAllStringSubmatch(string(b), -1) {
			seen++
			if _, ok := windowDefaultSQL(m[2]); !ok {
				t.Errorf("%s: %q has an unusable default spec %q", f, m[0], m[2])
			}
		}
		if u := UnboundPlaceholders(Apply(string(b), Vars{})); len(u) > 0 {
			// '%salt%' is expected in gov files; named braces are not.
			t.Errorf("%s: unbound after default expansion: %v", f, u)
		}
	}
	if seen == 0 {
		t.Error("no window placeholders found in the shipped queries; " +
			"this guard would pass vacuously")
	}
	t.Logf("checked %d window placeholders across %d files", seen, len(files))
}

// TestPlaceholderInCommentIgnored: query files explain ClickHouse macros
// in prose (queries.gov/system.replication_queue.sql mentions the
// {replica} macro). Treating that as an unbound placeholder would refuse
// a valid query over a comment.
func TestPlaceholderInCommentIgnored(t *testing.T) {
	sql := "SELECT replica_name\n-- conventionally the {replica} macro\nFROM system.replication_queue"
	if u := UnboundPlaceholders(sql); len(u) != 0 {
		t.Errorf("placeholder inside a comment reported as unbound: %v", u)
	}
	block := "SELECT 1 /* uses the {shard} macro */ FROM system.parts"
	if u := UnboundPlaceholders(block); len(u) != 0 {
		t.Errorf("placeholder inside a block comment reported as unbound: %v", u)
	}
	// But a real one outside a comment must still be caught.
	if u := UnboundPlaceholders("SELECT 1 WHERE id = {query_id}"); len(u) != 1 {
		t.Errorf("real unbound placeholder missed: %v", u)
	}
}

// TestShippedQueryDefaultWindows pins the default look-back of every
// shipped query by name. Without this the windows drift silently: they
// live in 20 files across three mode directories plus version overrides,
// and nothing else would notice one being edited to a different value.
//
// The map is the specification — update it deliberately when a window
// changes, in the same commit that changes the SQL.
func TestShippedQueryDefaultWindows(t *testing.T) {
	want := map[string]string{
		"system.query_log_details_7_days.sql":       "7d",
		"system.part_log_7_days.sql":                "7d",
		"system.metric_log_7_days.sql":              "7d",
		"system.asynchronous_insert_log_7_days.sql": "7d",
		"system.text_log.sql":                       "1d",
	}

	checked := map[string]int{}
	for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.EqualFold(filepath.Ext(p), ".sql") {
				return err
			}
			spec, ok := want[filepath.Base(p)]
			if !ok {
				return nil // query has no window; TestShippedQueriesHaveNoHardcodedWindow covers it
			}
			b, readErr := os.ReadFile(p)
			if readErr != nil {
				t.Fatalf("read %s: %v", p, readErr)
			}
			froms := regexp.MustCompile(`\{from:([A-Za-z0-9]+)\}`).FindAllStringSubmatch(string(b), -1)
			if len(froms) == 0 {
				t.Errorf("%s: expected a {from:%s} window, found none", p, spec)
				return nil
			}
			for _, m := range froms {
				if m[1] != spec {
					t.Errorf("%s: default window is {from:%s}, expected {from:%s}", p, m[1], spec)
				}
			}
			checked[filepath.Base(p)]++
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(checked) == 0 {
		t.Skip("query directories not present")
	}
	for name := range want {
		if checked[name] == 0 {
			t.Errorf("%s was never found in any mode directory; the guard is not "+
				"covering it (renamed or deleted?)", name)
		}
	}
	t.Logf("pinned windows across %d query files", len(checked))
}
