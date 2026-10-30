package bundle

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRows_ExactCollectorNameAndQuotedIntegers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "system.replicas_20261029_120000.jsonl", `{"database":"db","table":"t","is_readonly":1,"absolute_delay":"18446744073709551615","queue_size":"3"}`+"\n"+`{"database":"db","table":"u","is_readonly":0}`+"\n")
	write(t, dir, "system.replicas_problem_tables_20261029_120000.jsonl", `{"database":"x","table":"y"}`+"\n")
	b := Open(dir, nil)
	rows, err := b.Rows("system.replicas")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d; the prefix-sharing collector must not be mixed in", len(rows))
	}
	r := rows[0]
	if r.Str("database") != "db" || r.Int("is_readonly") != 1 || r.Int("queue_size") != 3 || r.Uint("absolute_delay") != 18446744073709551615 {
		t.Fatalf("row accessors: %v", r)
	}
	if rows[1].Int("queue_size") != 0 || rows[1].Str("missing") != "" {
		t.Fatal("absent columns must read as zero values")
	}
}

func TestRows_StatusOfMissingSources(t *testing.T) {
	dir := t.TempDir()
	b := Open(dir, map[string]Status{"system.parts": {State: "skipped", Reason: "essential collection"}, "system.tables": {State: "failed", Reason: "Code: 159"}})
	for name, want := range map[string]string{"system.parts": "skipped", "system.tables": "failed", "system.columns": "not collected"} {
		_, err := b.Rows(name)
		ne, ok := err.(*NotCollected)
		if !ok || ne.State != want {
			t.Errorf("%s: err %v, want state %q", name, err, want)
		}
	}
}

func TestRows_ParsesEachFileOnce(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "system.metrics_20261029_120000.jsonl", `{"metric":"Query","value":"5"}`+"\n")
	b := Open(dir, nil)
	a, _ := b.Rows("system.metrics")
	if err := os.Remove(filepath.Join(dir, "system.metrics_20261029_120000.jsonl")); err != nil {
		t.Fatal(err)
	}
	c, err := b.Rows("system.metrics")
	if err != nil || len(a) != 1 || len(c) != 1 {
		t.Fatalf("second read must come from the cache: %v %v %v", a, c, err)
	}
}

func TestRow_MapAndFloat(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x_20261029_120000.jsonl", `{"errors":{"ZNONODE":"7","ZOK":2},"lat":"1.5","f":2.25}`+"\n")
	rows, err := Open(dir, nil).Rows("x")
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	if m := r.Map("errors"); m["ZNONODE"] != 7 || m["ZOK"] != 2 {
		t.Fatalf("map: %v", m)
	}
	if r.Float("lat") != 1.5 || r.Float("f") != 2.25 {
		t.Fatalf("float: %v %v", r.Float("lat"), r.Float("f"))
	}
}
