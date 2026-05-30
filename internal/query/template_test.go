package query

import (
	"strings"
	"testing"
	"time"
)

func TestSysTable_CloudWrapping(t *testing.T) {
	// Per-replica tables get clusterAllReplicas in cloud, plain elsewhere.
	cases := []struct {
		mode, table, want string
	}{
		{"cloud", "query_log", "clusterAllReplicas(default, system.query_log)"},
		{"cloud", "text_log", "clusterAllReplicas(default, system.text_log)"},
		{"cloud", "errors", "clusterAllReplicas(default, system.errors)"},
		{"cloud", "parts", "system.parts"},     // shared — no wrapping
		{"cloud", "tables", "system.tables"},   // shared
		{"cloud", "replicas", "system.replicas"},
		{"onprem", "query_log", "system.query_log"},
		{"gov", "text_log", "system.text_log"},
		{"CLOUD", "query_log", "clusterAllReplicas(default, system.query_log)"}, // case insens
	}
	for _, c := range cases {
		got := SysTable(c.mode, c.table)
		if got != c.want {
			t.Errorf("SysTable(%q, %q) = %q, want %q", c.mode, c.table, got, c.want)
		}
	}
}

func TestApply_AllPlaceholders(t *testing.T) {
	sql := `SELECT * FROM {sys.query_log}
WHERE query_id = {query_id}
  AND normalized_query_hash = {normalized_query_hash}
  AND event_time BETWEEN {from} AND {to}`
	vars := Vars{
		Mode:                "cloud",
		QueryID:             "abc-123",
		NormalizedQueryHash: "9876543210",
		From:                time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		To:                  time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC),
	}
	out := Apply(sql, vars)
	want := []string{
		"clusterAllReplicas(default, system.query_log)",
		"query_id = 'abc-123'",
		"normalized_query_hash = 9876543210", // unquoted numeric
		"'2026-05-01 10:00:00'",
		"'2026-05-08 10:00:00'",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing substring %q in:\n%s", w, out)
		}
	}
}

func TestApply_EmptyValuesLeavePlaceholdersIntact(t *testing.T) {
	// Apply with no QueryID set must leave {query_id} untouched so
	// the executor's unbound-placeholder check can catch it.
	sql := "SELECT 1 WHERE query_id = {query_id}"
	out := Apply(sql, Vars{Mode: "onprem"})
	if !strings.Contains(out, "{query_id}") {
		t.Errorf("expected {query_id} to remain unsubstituted, got: %s", out)
	}
}

func TestApply_GovSalt(t *testing.T) {
	sql := "SELECT hex(SHA256(concat(database, '%salt%')))"
	out := Apply(sql, Vars{Mode: "gov", GovSalt: "customerABC2026"})
	if strings.Contains(out, "%salt%") {
		t.Errorf("salt placeholder not replaced: %s", out)
	}
	if !strings.Contains(out, "'customerABC2026'") {
		t.Errorf("substituted salt missing: %s", out)
	}
}

func TestUnboundPlaceholders(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"SELECT 1", nil},
		{"WHERE x = {query_id}", []string{"{query_id}"}},
		{"WHERE x = {query_id} AND y = {from}", []string{"{query_id}", "{from}"}},
		// '%salt%' is NOT flagged
		{"WHERE x = '%salt%'", nil},
		// sys placeholders ARE flagged if Apply was skipped — caller should always Apply first
		{"FROM {sys.parts}", []string{"{sys.parts}"}},
		// Empty `{}` braces (ClickHouse log-format markers in
		// message_format_string literals) are NOT flagged.
		{"WHERE x = 'Selected {}/{} parts by primary key'", nil},
		// De-dup repeated placeholders.
		{"q = {query_id} AND r = {query_id}", []string{"{query_id}"}},
	}
	for _, c := range cases {
		got := UnboundPlaceholders(c.in)
		if len(got) != len(c.want) {
			t.Errorf("UnboundPlaceholders(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i, w := range c.want {
			if got[i] != w {
				t.Errorf("UnboundPlaceholders(%q)[%d] = %q, want %q", c.in, i, got[i], w)
			}
		}
	}
}
