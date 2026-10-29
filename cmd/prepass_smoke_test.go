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

// Bundles are being renamed from clickhouse_backup_<ts> to
// clickhouse_diagnostic_<ts>. The pre-pass must read both: bundles under the
// old name sit in open cases. With both in one results folder it must pick
// the newest by its timestamp — sorted by name, every clickhouse_diagnostic_*
// would win over every clickhouse_backup_*, whatever their dates.
func TestInspectBundle_ReadsBothBundleNames(t *testing.T) {
	writeRun := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range minimalBundle {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	collected := func(out string) (path, at string) {
		t.Helper()
		var r struct {
			Path        string `json:"path"`
			CollectedAt string `json:"collected_at"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("pre-pass did not emit JSON: %v\n%s", err, out)
		}
		return r.Path, r.CollectedAt
	}

	// A results folder holding one of each, the OLD name being the newer run.
	parent := t.TempDir()
	writeRun(filepath.Join(parent, "clickhouse_diagnostic_20260102_000000"))
	writeRun(filepath.Join(parent, "clickhouse_backup_20260105_000000"))
	out, errOut, code := runPrepass(t, "--json", parent)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if path, at := collected(out); filepath.Base(path) != "clickhouse_backup_20260105_000000" || at != "2026-01-05 00:00:00" {
		t.Errorf("picked %s collected %q; want the newest run, clickhouse_backup_20260105_000000", filepath.Base(path), at)
	}

	// An archive under the new name.
	dir := t.TempDir()
	var members [][2]string
	for name, body := range minimalBundle {
		members = append(members, [2]string{"clickhouse_diagnostic_20260103_101500/" + name, body})
	}
	archive := filepath.Join(dir, "clickhouse_diagnostic_20260103_101500.tar.gz")
	writeTarGz(t, archive, members)
	out, errOut, code = runPrepass(t, "--json", "--extract-to", filepath.Join(dir, "x"), archive)
	if code != 0 {
		t.Fatalf("new-name archive: exit %d: %s", code, errOut)
	}
	if _, at := collected(out); at != "2026-01-03 10:15:00" {
		t.Errorf("new-name archive: collected_at = %q, want 2026-01-03 10:15:00", at)
	}
}

// A file is found by its collector name plus the run timestamp, never by
// prefix alone: system.replicas_*.jsonl also matches
// system.replicas_problem_tables_*.jsonl, so when the replicas collector
// failed (no system.replicas file) the pre-pass read the problem-tables file
// as system.replicas — the same trap as system.parts_* against
// system.parts_max_block_recent_*.
func TestInspectBundle_FileLookupIsExact(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(b, name+"_20261029_120000.jsonl"), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("system.version", `{"version":"26.7.1.1"}`)
	// No system.replicas file (the collector failed); only the problem-tables
	// one, with a read-only row that must NOT be reported as HC-1.4 / HC-3.x
	// from "system.replicas".
	put("system.replicas_problem_tables", `{"database":"db","table":"t","is_readonly":1,"is_session_expired":1,"queue_size":"0","absolute_delay":"9999","log_max_index":"1","log_pointer":"1","total_replicas":3,"active_replicas":1,"zookeeper_exception":""}`)
	out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "9999") {
		t.Fatalf("system.replicas checks read system.replicas_problem_tables:\n%s", out)
	}
}

// A replica that was down is absent from every per-replica file (requests
// run with skip_unavailable_shards = 1); the pre-pass must say which.
func TestInspectBundle_ReplicasThatDidNotAnswer(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		if err := os.WriteFile(filepath.Join(b, name+"_20261029_120000.jsonl"), []byte(body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("system.version", `{"version":"26.7.1.1"}`)
	run := func(rows ...string) string {
		put("system.replicas_answered", strings.Join(rows, "\n"))
		out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
		if err != nil {
			t.Fatalf("pre-pass failed: %v\n%s", err, out)
		}
		return string(out)
	}
	// One missing, its name unmatched: named.
	out := run(`{"host_name":"r1.svc","replica":"r1","name_matched":1,"configured":"2","answered":"1"}`,
		`{"host_name":"r2.svc","replica":"r2","name_matched":0,"configured":"2","answered":"1"}`)
	for _, want := range []string{"1 of 2 replica(s) did not answer", "r2.svc", "HC-0"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	// Two hosts sharing a short name, one of them down: the short name
	// matched both, but the counts still say one is missing.
	out = run(`{"host_name":"ch-01.site-a","replica":"ch-01","name_matched":1,"configured":"2","answered":"1"}`,
		`{"host_name":"ch-01.site-b","replica":"ch-01","name_matched":1,"configured":"2","answered":"1"}`)
	if !strings.Contains(out, "1 of 2 replica(s) did not answer") || !strings.Contains(out, "could not be matched") {
		t.Errorf("duplicate short names hid a missing replica:\n%s", out)
	}
	// Alias / IP endpoints: no name matches, but every replica answered.
	out = run(`{"host_name":"10.0.0.1","replica":"10","name_matched":0,"configured":"2","answered":"2"}`,
		`{"host_name":"10.0.0.2","replica":"10","name_matched":0,"configured":"2","answered":"2"}`)
	if strings.Contains(out, "did not answer") {
		t.Errorf("unmatched names but every replica answered, yet warned:\n%s", out)
	}
}

// An essential bundle says so first: its absent per-table files and 1-day
// windows are by design, and the reason it was essential is a finding.
func TestInspectBundle_EssentialCollectionIsReported(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "system.version_20261029_120000.jsonl"), []byte(`{"version":"26.7.1.1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	xl := "clickhouse-diagnostic execution log\n====\nstarted:  2026-10-29 12:00:00 +0000\nmode:     onprem\n" +
		"collect:  essential collection, chosen automatically: memory at 94 % of the server limit — query timeout 30s\n\nSummary\n-------\n"
	if err := os.WriteFile(filepath.Join(b, "execution_log.txt"), []byte(xl), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass failed: %v\n%s", err, out)
	}
	for _, want := range []string{"essential collection", "memory at 94 %", "-collect full"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
}
