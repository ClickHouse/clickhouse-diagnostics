package alert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/query"
	"gopkg.in/yaml.v3"
)

// ── expandQuery ──────────────────────────────────────────────────────────────

func TestExpandQuery_OnPrem(t *testing.T) {
	ev := &Evaluator{mode: "onprem"}
	got := ev.expandQuery("SELECT * FROM {sys.mutations} WHERE parts_to_do > 0")
	want := "SELECT * FROM system.mutations WHERE parts_to_do > 0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandQuery_Cloud(t *testing.T) {
	ev := &Evaluator{mode: "cloud"}
	got := ev.expandQuery("SELECT * FROM {sys.mutations}")
	want := "SELECT * FROM clusterAllReplicas(default, system.mutations)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandQuery_MultiPlaceholder(t *testing.T) {
	ev := &Evaluator{mode: "onprem"}
	got := ev.expandQuery("SELECT * FROM {sys.parts} JOIN {sys.tables} USING (database, table)")
	if !strings.Contains(got, "system.parts") || !strings.Contains(got, "system.tables") {
		t.Errorf("multi-placeholder expansion failed: %q", got)
	}
}

// ── FormattedMessages ────────────────────────────────────────────────────────

func TestFormattedMessages(t *testing.T) {
	r := Result{
		Message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h",
		Rows: []map[string]interface{}{
			{"mutation_id": "mutation_42", "database": "prod", "table": "events", "hours_running": "5"},
		},
	}
	msgs := r.FormattedMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	want := "Mutation mutation_42 on prod.events running 5h"
	if msgs[0] != want {
		t.Errorf("got %q, want %q", msgs[0], want)
	}
}

func TestFormattedMessages_MultiRow(t *testing.T) {
	r := Result{
		Message: "{database}.{table} has {detached_count} detached part(s)",
		Rows: []map[string]interface{}{
			{"database": "db1", "table": "t1", "detached_count": "3"},
			{"database": "db2", "table": "t2", "detached_count": "7"},
		},
	}
	msgs := r.FormattedMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

// ── Fired ────────────────────────────────────────────────────────────────────

func TestFired_NoRows(t *testing.T) {
	r := Result{Rows: []map[string]interface{}{}}
	if r.Fired() {
		t.Error("empty rows should not be fired")
	}
}

func TestFired_WithRows(t *testing.T) {
	r := Result{Rows: []map[string]interface{}{{"x": "1"}}}
	if !r.Fired() {
		t.Error("rows present should be fired")
	}
}

func TestFired_WithError(t *testing.T) {
	r := Result{Error: "connection refused"}
	if !r.Fired() {
		t.Error("error result should be fired")
	}
}

// ── parseJSONCompact ─────────────────────────────────────────────────────────

func TestParseJSONCompact(t *testing.T) {
	raw := `{"meta":[{"name":"exception_code"},{"name":"error_count"}],"data":[["241","15"],["60","8"]],"rows":2}`
	rows, err := parseJSONCompact(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0]["exception_code"] != "241" {
		t.Errorf("expected '241', got %v", rows[0]["exception_code"])
	}
}

func TestParseJSONCompact_Empty(t *testing.T) {
	raw := `{"meta":[{"name":"id"}],"data":[],"rows":0}`
	rows, err := parseJSONCompact(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

// ── RunAll (filesystem, no ClickHouse) ───────────────────────────────────────

func TestRunAll_MissingDir(t *testing.T) {
	ev := &Evaluator{mode: "onprem"}
	results := ev.RunAll("/nonexistent/dir/that/does/not/exist")
	if results != nil {
		t.Errorf("expected nil for missing dir, got %v", results)
	}
}

func TestYAML_LoadsCorrectly(t *testing.T) {
	raw := []byte(`
name: test_alert
title: "Test Alert"
severity: warning
query: |
  SELECT database, table, count() AS parts
  FROM {sys.parts}
  WHERE active = 1
  GROUP BY database, table
  HAVING parts > 1000
message: "{database}.{table} has {parts} active parts"
tags:
  - test
`)
	var def Definition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	if def.Name != "test_alert" {
		t.Errorf("name: got %q, want %q", def.Name, "test_alert")
	}
	if def.Severity != "warning" {
		t.Errorf("severity: got %q, want %q", def.Severity, "warning")
	}
	if !strings.Contains(def.Query, "{sys.parts}") {
		t.Error("query should contain {sys.parts} placeholder")
	}
}

func TestSecurity_ForbiddenQueryRejected(t *testing.T) {
	dir := t.TempDir()
	badYAML := []byte(`
name: bad_alert
title: "Forbidden"
severity: critical
query: |
  DROP TABLE system.parts
message: "should never fire"
`)
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(badYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// evalFile directly — no ClickHouse client needed; security check fires before query execution
	ev := &Evaluator{mode: "onprem"}
	r := ev.evalFile(filepath.Join(dir, "bad.yaml"))
	if !strings.Contains(r.Error, "security") {
		t.Errorf("expected security error, got: %q", r.Error)
	}
}

func TestRunAll_IgnoresNonYAML(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hello"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("some notes"), 0644)

	// Empty client is OK here because no .yaml files exist so RunAll returns early.
	ev := &Evaluator{mode: "onprem"}
	results := ev.RunAll(dir)
	if len(results) != 0 {
		t.Errorf("expected 0 results (no yaml files), got %d", len(results))
	}
}

// ── Real alert YAML files from the alerts/ directory ─────────────────────────

func TestAlertYAML_FilesAreValid(t *testing.T) {
	alertsDir := "../../alerts"
	if _, err := os.Stat(alertsDir); os.IsNotExist(err) {
		t.Skip("alerts/ directory not present")
	}

	entries, err := os.ReadDir(alertsDir)
	if err != nil {
		t.Fatal(err)
	}

	ev := &Evaluator{mode: "onprem"}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(alertsDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var def Definition
			if err := yaml.Unmarshal(raw, &def); err != nil {
				t.Fatalf("YAML parse error: %v", err)
			}
			if def.Name == "" {
				t.Error("alert has no name")
			}
			if def.Title == "" {
				t.Error("alert has no title")
			}
			if def.Query == "" {
				t.Error("alert has no query")
			}
			// Security: expanded query must pass validation
			expanded := ev.expandQuery(def.Query)
			if err := query.ValidateQueryContent(expanded); err != nil {
				t.Errorf("security validation failed: %v", err)
			}
		})
	}
}
