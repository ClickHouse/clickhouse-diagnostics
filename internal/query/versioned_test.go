package query

import (
	"os"
	"path/filepath"
	"testing"

	"clickhouse-diagnostic/internal"
)

// writeFiles creates the given relative-path → content files under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func v(major, minor, patch, build int) internal.Version {
	return internal.Version{Major: major, Minor: minor, Patch: patch, Build: build}
}

// asMap keys results by file name for easy assertions.
func asMap(files []internal.QueryFile) map[string]internal.QueryFile {
	m := make(map[string]internal.QueryFile, len(files))
	for _, f := range files {
		m[f.Name] = f
	}
	return m
}

func TestFindVersionedFiles_RootOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql": "SELECT 1",
		"b.sql": "SELECT 2",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	// Sorted by name for deterministic execution order.
	if files[0].Name != "a.sql" || files[1].Name != "b.sql" {
		t.Errorf("expected sorted [a.sql b.sql], got [%s %s]", files[0].Name, files[1].Name)
	}
}

func TestFindVersionedFiles_OverrideWhenServerIsNewEnough(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql":          "SELECT 'root'",
		"24.1.1.0/a.sql": "SELECT 'v24'",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file after override, got %d", len(files))
	}
	if files[0].DirName != "24.1.1.0" {
		t.Errorf("expected override from 24.1.1.0, got DirName=%q", files[0].DirName)
	}
}

func TestFindVersionedFiles_IncompatibleVersionDirSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql":          "SELECT 'root'",
		"26.1.1.0/a.sql": "SELECT 'future'",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].DirName != "" {
		t.Errorf("expected root file (server too old for 26.1.1.0), got DirName=%q", files[0].DirName)
	}
}

func TestFindVersionedFiles_HighestCompatibleVersionWins(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.sql":             "SELECT 'root'",
		"23.8.1.0/a.sql":    "SELECT 'v23'",
		"25.4.1.0/a.sql":    "SELECT 'v25'",
		"26.1.1.0/a.sql":    "SELECT 'future'",
		"23.8.1.0/b.sql":    "SELECT 'b-v23'",
		"invalid-dir/c.sql": "SELECT 'not-a-version'",
		"c.sql":             "SELECT 'c-root'",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".sql")
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(files)
	if len(m) != 3 {
		t.Fatalf("expected 3 unique files, got %d", len(m))
	}
	if m["a.sql"].DirName != "25.4.1.0" {
		t.Errorf("a.sql: expected 25.4.1.0 (highest compatible), got %q", m["a.sql"].DirName)
	}
	if m["b.sql"].DirName != "23.8.1.0" {
		t.Errorf("b.sql: expected 23.8.1.0 (only in version dir), got %q", m["b.sql"].DirName)
	}
	// invalid-dir is not a version directory → its file must be ignored,
	// leaving the root c.sql.
	if m["c.sql"].DirName != "" {
		t.Errorf("c.sql: expected root (invalid version dir ignored), got %q", m["c.sql"].DirName)
	}
}

func TestFindVersionedFiles_ExtensionFilter(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"rule.yaml":          "name: x",
		"RULE2.YAML":         "name: y",
		"readme.md":          "# docs",
		"query.sql":          "SELECT 1",
		"25.4.1.0/rule.yaml": "name: x-new",
	})

	files, err := FindVersionedFiles(dir, v(25, 4, 1, 0), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := asMap(files)
	if len(m) != 2 {
		t.Fatalf("expected 2 yaml files, got %d: %v", len(m), m)
	}
	if m["rule.yaml"].DirName != "25.4.1.0" {
		t.Errorf("rule.yaml: expected 25.4.1.0 override, got %q", m["rule.yaml"].DirName)
	}
	if _, ok := m["RULE2.YAML"]; !ok {
		t.Error("extension match should be case-insensitive")
	}
}
