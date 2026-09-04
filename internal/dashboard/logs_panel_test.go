package dashboard

import (
	"strings"
	"testing"
)

// The Logs panel is only safe because it is bounded. Raw server logs are
// tail-copied into the bundle at up to 50 MiB per file; embedding those would
// produce a document the browser cannot open — they are linked under
// Collected Files instead. These caps are the guardrail.
func TestTextLogSQL_IsBounded(t *testing.T) {
	sql := (&Generator{mode: "onprem"}).textLogSQL()
	for _, want := range []string{
		"LIMIT 1000",                              // textLogRowCap
		"leftUTF8(message, 300)",                  // textLogMessageCap — leftUTF8, so the cap is genuinely characters and a cut cannot split one
		"INTERVAL 24 HOUR",                        // recent only
		"'Warning', 'Error', 'Critical', 'Fatal'", // triaged only — and Critical sits between Fatal and Error, so it must be here
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("text_log query lost its bound %q:\n%s", want, sql)
		}
	}
	if textLogRowCap > 2000 || textLogMessageCap > 500 {
		t.Errorf("caps raised beyond what the payload can carry: %d rows x %d chars",
			textLogRowCap, textLogMessageCap)
	}
}

// The analyzer resolves WHERE identifiers against SELECT aliases, so
// `toString(event_time) AS event_time` + `WHERE event_time > now() - ...`
// compares String to DateTime → NO_COMMON_TYPE (code 386 on 26.4.5.143).
// The predicate must sit in a subquery where event_time is still a DateTime.
func TestTextLogSQL_FiltersOnTypedTimestamp(t *testing.T) {
	sql := (&Generator{mode: "onprem"}).textLogSQL()
	where := strings.Index(sql, "WHERE event_time >")
	alias := strings.Index(sql, "toString(event_time) AS event_time")
	if where < 0 || alias < 0 {
		t.Fatalf("query shape changed unexpectedly:\n%s", sql)
	}
	if where < alias {
		t.Error("the event_time predicate must not resolve against the stringified alias")
	}
	if !strings.Contains(sql, "FROM (") {
		t.Errorf("expected the filter to be wrapped in a subquery:\n%s", sql)
	}
}

func TestTemplate_LogsPanelIsConditional(t *testing.T) {
	for _, want := range []string{
		`id="sec-logs"`, `id="nav-logs"`, "const rows=DATA.text_log||[];",
		"if(!rows.length) return;", "window.logsFilter", "log-pagination",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("logs panel missing %q", want)
		}
	}
	// Paginated: the row cap bounds the payload, pagination bounds the DOM.
	if !strings.Contains(htmlTemplate, "const PAGE=100;") {
		t.Error("logs table must paginate; 1000 rows of <tr> is what makes a page crawl")
	}
}
