package dashboard

import (
	"strings"
	"testing"
)

// An alert message is the rule's template with one row's values substituted,
// and those values routinely carry a full ClickHouse exception: one measured
// in a real bundle was 1764 characters over 15 lines, of which the first 216
// were the error itself. replication_queue_errors returns up to 50 such rows
// and keeper_health up to 168, so rendering every message in full turned the
// panel into hundreds of lines of stack frames. These are the bounds that
// keep the panel readable; a refactor must not drop them.
func TestTemplate_AlertMessagesCollapseVerboseParts(t *testing.T) {
	for _, want := range []string{
		"const ALERT_HEAD_CHARS=260;",                 // inline length of one instance
		"const ALERT_ROWS_SHOWN=5;",                   // instances before the rest collapse
		"const ALERT_STACK_RE=",                       // the stack trace is the cut point
		`Stack trace \(when copying this message`,     // ClickHouse's own marker, matched literally
		"function alertMessageParts(msg){",            // head / full split
		"function alertDisclosure(parts){",            // the <details> toggle
		"function alertRowLine(a,row){",               // one instance line
		`'<details class="alert-more">`,               // native disclosure, no JS needed
		`'<pre class="alert-full">'+esc(parts.full)+`, // full text is kept, and escaped
		"more instance",                               // the extra-rows toggle
		"more about this rule",                        // the description's second half

		// Two off-by-one traps found in review. The cap is INCLUSIVE of the
		// ellipsis, so the slice has to be one short — otherwise the no-space
		// fallback emits 261 characters. And the toggle compares the inline
		// head against the whole flat message, not a version-stripped copy:
		// with the stripped copy, a message that is NOTHING BUT a version
		// suffix strips to empty, the fallback restores it, and the mismatch
		// renders a disclosure whose body repeats the line above it.
		"head.slice(0,ALERT_HEAD_CHARS-1)",
		"truncated:head!==flat,",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("alert panel lost %q", want)
		}
	}

	// The head must never be emitted raw: every branch that prints customer
	// text goes through esc().
	for _, banned := range []string{
		"+parts.head+", "+p.head+", "+ep.head+", "+msg+'</li>", "+a.error+'",
		// The pre-fix forms of the two traps above.
		"head.slice(0,ALERT_HEAD_CHARS)+", "truncated:head!==flatTrimmed",
	} {
		if strings.Contains(htmlTemplate, banned) {
			t.Errorf("alert panel interpolates customer text unescaped: %q", banned)
		}
	}

	// A disclosure that is always open helps nobody; the toggle must be
	// closed by default (no `open` attribute on the element we emit).
	if strings.Contains(htmlTemplate, `<details class="alert-more" open`) {
		t.Error("alert disclosures must start closed")
	}
}

// The collapse only pays off if the full text is still in the page — support
// needs the stack trace, just not on screen by default.
func TestBuildHTML_AlertStackTraceIsKeptButCollapsed(t *testing.T) {
	trace := "Code: 221. DB::Exception: No interserver IO endpoint named SharedMergeTreePartsUpdate:/clickhouse/tables/<uuid>/default/virtual_parts/node-07. " +
		"(NO_SUCH_INTERSERVER_IO_ENDPOINT) (version 26.2.1.390 (official build)), Stack trace (when copying this message, always include the lines below):\n\n" +
		"0. ./ci/tmp/build/./src/Common/Exception.cpp:141:1: DB::Exception::Exception(DB::Exception::MessageMasked&&, int, bool) @ 0x000000001373469f\n" +
		"1. DB::Exception::Exception(String&&, int, String, bool) @ 0x000000000cbeeb0e\n"
	data := map[string]interface{}{
		"generated_at": "2026-09-25 10:00:00 UTC", "mode": "onprem", "version": "26.2.1.390",
		"alerts": []map[string]interface{}{{
			"name": "replication_queue_errors", "title": "Replication queue entries have exceptions",
			"severity": "critical", "file": "replication_queue_errors.yaml",
			"message": "{database}.{table} (replica {replica_name}): {type} failed after {num_tries} tries — {last_exception}",
			"rows": []map[string]interface{}{
				{"database": "demo", "table": "events", "replica_name": "r-07", "type": "GET_PART", "num_tries": 128, "last_exception": trace},
			},
		}},
	}
	html := buildHTML(data)
	if !strings.Contains(html, "NO_SUCH_INTERSERVER_IO_ENDPOINT") {
		t.Error("the full exception must still be embedded — support reads the trace after expanding")
	}
	if !strings.Contains(html, "Exception.cpp:141") {
		t.Error("stack frames must survive into the payload")
	}
}
