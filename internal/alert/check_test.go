package alert

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/bundle"
)

func TestEvalFile_GoCheckRunsOverTheBundleNotTheServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "system.replicas_20261029_120000.jsonl"),
		[]byte(`{"database":"db","table":"t","is_readonly":1,"absolute_delay":"5"}`+"\n"+`{"database":"db","table":"u","is_readonly":0}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RegisterCheck("test_readonly", func(b *bundle.Bundle, mode string) ([]map[string]interface{}, error) {
		rows, err := b.Rows("system.replicas")
		if err != nil {
			return nil, err
		}
		var out []map[string]interface{}
		for _, r := range rows {
			if r.Int("is_readonly") == 1 {
				out = append(out, map[string]interface{}{"database": r.Str("database"), "table": r.Str("table")})
			}
		}
		return out, nil
	})
	rule := "name: ro\ntitle: Ro\nseverity: critical\nsource: [system.replicas]\ncheck: go:test_readonly\nquery: |\n  SELECT database, table FROM {sys.replicas} WHERE is_readonly = 1\nmessage: \"{database}.{table}\"\n"
	sent := 0
	ev := (&Evaluator{mode: "cloud", queryFn: func(string) (string, error) { sent++; return noRows, nil }}).WithBundle(bundle.Open(dir, nil))
	r := ev.evalFile(writeRule(t, rule))
	if sent != 0 {
		t.Fatalf("a Go-check rule sent %d query(ies) to the server", sent)
	}
	if r.Error != "" || len(r.Rows) != 1 || r.Rows[0]["table"] != "t" {
		t.Fatalf("result: %+v", r)
	}
	// The customer still gets the SQL to run on their cluster, expanded.
	if !strings.Contains(r.Query, "system.replicas") || strings.Contains(r.Query, "{sys.") {
		t.Errorf("Query shown: %q", r.Query)
	}
}

func TestEvalFile_GoCheckSourceNotCollectedIsNotApplicable(t *testing.T) {
	RegisterCheck("test_needs_parts", func(b *bundle.Bundle, mode string) ([]map[string]interface{}, error) {
		_, err := b.Rows("system.parts")
		return nil, err
	})
	rule := "name: p\ntitle: P\nseverity: warning\nsource: [system.parts]\ncheck: go:test_needs_parts\nquery: |\n  SELECT 1 FROM {sys.parts}\nmessage: \"x\"\n"
	ev := (&Evaluator{mode: "onprem"}).WithBundle(bundle.Open(t.TempDir(), map[string]bundle.Status{"system.parts": {State: "skipped", Reason: "essential collection"}}))
	r := ev.evalFile(writeRule(t, rule))
	if !r.Skipped || r.Error != "" || !strings.Contains(r.Reason, "system.parts") || !strings.Contains(r.Reason, "skipped") {
		t.Fatalf("missing source: %+v", r)
	}
}

func TestEvalFile_GoCheckErrorsAndUnknownChecks(t *testing.T) {
	RegisterCheck("test_broken", func(*bundle.Bundle, string) ([]map[string]interface{}, error) { return nil, errors.New("boom") })
	ev := (&Evaluator{mode: "onprem"}).WithBundle(bundle.Open(t.TempDir(), nil))
	for check, want := range map[string]string{"go:test_broken": "boom", "go:no_such_check": "unknown check", "sql:x.sql": "unknown check"} {
		rule := "name: b\ntitle: B\nseverity: warning\nsource: [system.one]\ncheck: " + check + "\nquery: |\n  SELECT 1\nmessage: \"x\"\n"
		if r := ev.evalFile(writeRule(t, rule)); !strings.Contains(r.Error, want) {
			t.Errorf("%s: %+v", check, r)
		}
	}
}
