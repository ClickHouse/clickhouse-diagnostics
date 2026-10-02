package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-pass (skills/clickhouse-diagnostic/scripts/inspect_bundle.py) is the
// mandatory first step of the analysis skill, and it is plain Python with no
// test of its own. This smoke test builds the smallest bundle that exercises
// the paths where one file's rows are read after another file's rows have been
// iterated, runs the script, and requires a clean exit.
//
// It exists because of a real crash: the mutations block looped over
// `for (db, tb), c in per_table...`, Python leaks loop variables into the
// enclosing scope, and `tb` was system.tables for the rest of the function.
// One unfinished mutation — an everyday state — therefore turned every later
// row access into AttributeError and the pre-pass printed a traceback and
// nothing else. A bundle with a pending mutation AND a materialized view is
// the two-line fixture that reproduces it.
func TestInspectBundle_SmokeOnMinimalBundle(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("pre-pass script not present: %v", err)
	}

	dir := t.TempDir()
	b := filepath.Join(dir, "clickhouse_backup_20260101_000000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, rows []map[string]any) {
		var sb strings.Builder
		for _, r := range rows {
			line, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			sb.Write(line)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(b, name+"_20260101_000000.jsonl"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("system.version", []map[string]any{{"version": "26.7.1.1"}})
	write("system.settings", []map[string]any{{"name": "max_threads", "value": "8", "changed": 1}})
	// A materialized view: analyse() reads system.tables rows AFTER the
	// mutations block, which is where the leaked loop variable struck.
	write("system.tables", []map[string]any{
		{"database": "db", "name": "t", "engine": "MergeTree", "total_rows": "10"},
		{"database": "db", "name": "mv", "engine": "MaterializedView",
			"create_table_query": "CREATE MATERIALIZED VIEW db.mv TO db.t AS SELECT * FROM db.src"},
	})
	// One unfinished mutation is enough: the loop runs for every pending
	// table, not only above the >100 threshold.
	write("system.mutations", []map[string]any{
		{"database": "db", "table": "t", "mutation_id": "0000000000", "parts_to_do": "3", "is_done": "0",
			"create_time": "2026-01-01 00:00:00", "command": "UPDATE x = 1 WHERE y = 2"},
	})
	write("system.parts", []map[string]any{
		{"database": "db", "table": "t", "partition_id": "all", "active": 1, "rows": "10",
			"bytes_on_disk": "1024", "level": 0, "max_block_number": "1", "modification_time": "2026-01-01 00:00:00"},
	})

	out, err := exec.Command(py, script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass failed on a minimal bundle (exit %v).\n"+
			"A traceback here means one file's rows were clobbered before they were read.\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, "Traceback") {
		t.Fatalf("pre-pass printed a traceback:\n%s", s)
	}
	for _, want := range []string{"Bundle inspection", "26.7.1.1"} {
		if !strings.Contains(s, want) {
			t.Errorf("pre-pass output missing %q:\n%s", want, s)
		}
	}
}
