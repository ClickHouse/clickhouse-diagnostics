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

// TestFindVersionedFiles_TieredLadder mirrors the real
// queries.query_analysis/query_details.sql ladder (root → 23.8.1.0 →
// 23.9.1.0 → 23.11.1.0) plus the asynchronous_insert_log pattern of a
// file that exists ONLY in a version dir and must vanish for older
// servers.
func TestFindVersionedFiles_TieredLadder(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"query_details.sql":           "SELECT 'baseline'",
		"23.8.1.0/query_details.sql":  "SELECT 'plus query_cache_usage'",
		"23.9.1.0/query_details.sql":  "SELECT 'plus peak_threads_usage'",
		"23.11.1.0/query_details.sql": "SELECT 'plus hostname column'",
		// No root counterpart — like system.asynchronous_insert_log
		// (the table itself only exists from 22.10).
		"22.10.1.0/async_log.sql": "SELECT 'table exists from 22.10'",
	})

	cases := []struct {
		server        internal.Version
		wantDetails   string // expected DirName for query_details.sql
		wantAsyncSeen bool
	}{
		{v(22, 8, 1, 0), "", false},
		{v(22, 10, 1, 0), "", true},
		{v(23, 8, 5, 0), "23.8.1.0", true},
		{v(23, 10, 1, 0), "23.9.1.0", true},
		{v(25, 4, 1, 0), "23.11.1.0", true},
	}
	for _, tc := range cases {
		files, err := FindVersionedFiles(dir, tc.server, ".sql")
		if err != nil {
			t.Fatal(err)
		}
		m := asMap(files)
		if got := m["query_details.sql"].DirName; got != tc.wantDetails {
			t.Errorf("server %d.%d: query_details from %q, want %q",
				tc.server.Major, tc.server.Minor, got, tc.wantDetails)
		}
		if _, seen := m["async_log.sql"]; seen != tc.wantAsyncSeen {
			t.Errorf("server %d.%d: async_log seen=%v, want %v",
				tc.server.Major, tc.server.Minor, seen, tc.wantAsyncSeen)
		}
	}
}

// TestFindVersionedFiles_RealRepoDirs guards the SHIPPED trees, not
// synthetic ones: every query/alert directory must resolve cleanly, and
// every version-directory override must have a root twin — except the
// documented version-only files (tables that don't exist below their
// gate). This is the CI check that a rung added in one mode but renamed,
// or a version dir with a typo, can't land silently.
func TestFindVersionedFiles_RealRepoDirs(t *testing.T) {
	// Files that intentionally exist ONLY in version directories.
	versionOnly := map[string]bool{
		"system.asynchronous_insert_log_7_days.sql": true, // table added 22.10
	}
	dirs := map[string]string{
		"../../queries.onprem":         ".sql",
		"../../queries.gov":            ".sql",
		"../../queries.cloud":          ".sql",
		"../../queries.query_analysis": ".sql",
		"../../alerts":                 ".yaml",
	}
	high := internal.Version{Major: 99, Minor: 1, Patch: 1, Build: 1}
	for dir, ext := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				t.Skipf("%s not present", dir)
			}
			files, err := FindVersionedFiles(dir, high, ext)
			if err != nil {
				t.Fatalf("resolution failed: %v", err)
			}
			if len(files) == 0 {
				t.Fatal("resolved zero files")
			}
			// Every override must shadow a root twin (same base name)
			// unless it's a documented version-only file.
			for _, f := range files {
				if f.DirName == "" || versionOnly[f.Name] {
					continue
				}
				if _, err := os.Stat(filepath.Join(dir, f.Name)); err != nil {
					t.Errorf("override %s/%s has no root twin — renamed override becomes a separate file", f.DirName, f.Name)
				}
			}
		})
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
