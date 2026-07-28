package alert

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal"
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

func TestExpandQuery_Cloud_PerReplicaTable(t *testing.T) {
	ev := &Evaluator{mode: "cloud"}
	got := ev.expandQuery("SELECT * FROM {sys.query_log}")
	want := "SELECT * FROM clusterAllReplicas(default, system.query_log)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandQuery_Cloud_SharedTable(t *testing.T) {
	// Shared tables (parts, tables, replicas, …) are queried plain in cloud
	// because every replica sees the same rows.
	ev := &Evaluator{mode: "cloud"}
	for _, table := range []string{"parts", "tables", "replicas", "replication_queue", "mutations", "detached_parts"} {
		got := ev.expandQuery("SELECT * FROM {sys." + table + "}")
		want := "SELECT * FROM system." + table
		if got != want {
			t.Errorf("table %q: got %q, want %q", table, got, want)
		}
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
	results := ev.RunAll("/nonexistent/dir/that/does/not/exist", internal.Version{Major: 25, Minor: 4, Patch: 1, Build: 0})
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
	results := ev.RunAll(dir, internal.Version{Major: 25, Minor: 4, Patch: 1, Build: 0})
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

// TestAlertYAML_OverridesMatchRoot guards against root/override drift: a
// version-directory rule (e.g. alerts/24.1.1.0/foo.yaml) is a near-copy of
// its root twin differing only in the query, so a future edit could easily
// land in one and not the other. Assert every override has a root twin with
// the same identity metadata (name/title/severity/tags), and that its name
// matches the filename (a rename silently creates a second rule instead of
// an override — see the README note).
func TestAlertYAML_OverridesMatchRoot(t *testing.T) {
	alertsDir := "../../alerts"
	if _, err := os.Stat(alertsDir); os.IsNotExist(err) {
		t.Skip("alerts/ directory not present")
	}
	load := func(path string) (Definition, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Definition{}, err
		}
		var d Definition
		return d, yaml.Unmarshal(raw, &d)
	}
	entries, err := os.ReadDir(alertsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		verDir := filepath.Join(alertsDir, e.Name())
		files, err := os.ReadDir(verDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".yaml") {
				continue
			}
			name := f.Name()
			t.Run(e.Name()+"/"+name, func(t *testing.T) {
				rootPath := filepath.Join(alertsDir, name)
				if _, err := os.Stat(rootPath); err != nil {
					t.Fatalf("override has no root twin %s — a renamed override becomes a separate rule, not an override", rootPath)
				}
				ov, err := load(filepath.Join(verDir, name))
				if err != nil {
					t.Fatal(err)
				}
				rt, err := load(rootPath)
				if err != nil {
					t.Fatal(err)
				}
				if base := strings.TrimSuffix(name, ".yaml"); ov.Name != base {
					t.Errorf("name %q does not match filename %q", ov.Name, base)
				}
				if ov.Name != rt.Name {
					t.Errorf("name drift: override %q vs root %q", ov.Name, rt.Name)
				}
				if ov.Title != rt.Title {
					t.Errorf("title drift: override %q vs root %q", ov.Title, rt.Title)
				}
				if ov.Severity != rt.Severity {
					t.Errorf("severity drift: override %q vs root %q", ov.Severity, rt.Severity)
				}
				if !reflect.DeepEqual(ov.Tags, rt.Tags) {
					t.Errorf("tags drift: override %v vs root %v", ov.Tags, rt.Tags)
				}
			})
		}
	}
}
