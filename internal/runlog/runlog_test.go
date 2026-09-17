package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRender_SectionsAndOrdering(t *testing.T) {
	r := New()
	r.SetMeta("mode", "onprem")
	r.SetMeta("server", "26.7.5.10")
	r.Record(Entry{Stage: "collector", Name: "system.parts.sql", Status: "ok", Duration: 1200 * time.Millisecond, Bytes: 66_528_876, Rows: 50000})
	r.Record(Entry{Stage: "collector", Name: "system.part_log_3_days.sql", Source: "23.11.1.0", Status: "ok", Duration: 41 * time.Second, Bytes: 192_796_599, Rows: 556430})
	r.Record(Entry{Stage: "collector", Name: "system.crash_log.sql", Status: "failed", Duration: 20 * time.Millisecond, Rows: -1,
		Error: "error executing query: non-OK status: 404, body: Code: 60. DB::Exception: Unknown table expression identifier 'system.crash_log'\nsecond line | with pipe " + strings.Repeat("x", 400)})
	r.Record(Entry{Stage: "alert", Name: "too_many_parts.yaml", Status: "fired", Duration: 300 * time.Millisecond, Rows: -1, Extra: "1 instance(s)"})
	r.Phase("collectors", 43*time.Second, "")
	r.Phase("dashboard", 4*time.Second, "")
	out := r.Render(time.Now().Add(50 * time.Second))

	for _, want := range []string{
		"clickhouse-diagnostic execution log",
		"mode:     onprem",
		"collectors: 2 ok, 1 failed, 0 empty",
		"alerts:     1 rules — 1 fired, 0 clean, 0 failed, 0 skipped",
		"Most expensive collectors",
		"Failed collectors (1)",
		"| # | stage | name | source | status | duration_ms | bytes | rows | note | error |",
		"| 2 | collector | system.part_log_3_days.sql | 23.11.1.0 | ok | 41000 | 192796599 | 556430 |  |  |",
		"phases:     collectors 43.00 s | dashboard 4.00 s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n---\n%s", want, out)
		}
	}
	// Most expensive list is sorted by duration: part_log first.
	top := out[strings.Index(out, "Most expensive"):]
	if strings.Index(top, "system.part_log_3_days.sql") > strings.Index(top, "system.parts.sql") {
		t.Error("most-expensive list is not sorted by duration")
	}
	// Error text: capped, single line, pipes replaced so the table stays parseable.
	if strings.Contains(out, strings.Repeat("x", 301)) {
		t.Error("error text was not capped")
	}
	failed := out[strings.Index(out, "| 3 | collector"):]
	failed = failed[:strings.Index(failed, "\n")]
	if strings.Count(failed, "|") != 11 {
		t.Errorf("failed row has a stray pipe or newline: %q", failed)
	}
}

func TestWrite_CreatesFileAndNilIsSafe(t *testing.T) {
	dir := t.TempDir()
	r := New()
	r.Record(Entry{Stage: "collector", Name: "system.version.sql", Status: "ok", Duration: time.Millisecond, Bytes: 25, Rows: 1})
	p, err := r.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != FileName {
		t.Errorf("unexpected file name %s", p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	var nilRec *Recorder
	nilRec.Record(Entry{})
	nilRec.Phase("x", 0, "")
	if p, err := nilRec.Write(dir); err != nil || p != "" {
		t.Errorf("nil recorder must be a no-op, got %q %v", p, err)
	}
}

func TestGovRedaction(t *testing.T) {
	r := New().WithGov(true)
	r.Record(Entry{Stage: "collector", Name: "system.parts.sql", Status: "failed", Rows: -1,
		Error: "error executing query: non-OK status: 404, body: Code: 60. DB::Exception: Unknown table expression identifier 'customer_db.orders' in scope SELECT … (UNKNOWN_TABLE)"})
	r.Record(Entry{Stage: "collector", Name: "system.tables.sql", Status: "failed", Rows: -1, Error: "dial tcp 10.1.2.3:8123: connection refused"})
	r.Phase("dashboard", time.Second, "failed: could not read customer_db.orders")
	out := r.Render(time.Now())
	for _, leak := range []string{"customer_db", "orders", "10.1.2.3"} {
		if strings.Contains(out, leak) {
			t.Errorf("gov log leaks %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"Code: 60 (UNKNOWN_TABLE) — message redacted in gov mode", "error text redacted in gov mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("gov log missing %q", want)
		}
	}
	// Non-gov keeps the text.
	plain := New()
	plain.Record(Entry{Stage: "collector", Name: "x.sql", Status: "failed", Rows: -1, Error: "Code: 60. Unknown table customer_db.orders (UNKNOWN_TABLE)"})
	if !strings.Contains(plain.Render(time.Now()), "customer_db.orders") {
		t.Error("non-gov recorder must keep the error text")
	}
}
