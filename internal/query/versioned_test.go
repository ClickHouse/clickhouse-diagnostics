package query

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	// Files that intentionally exist ONLY in version directories, keyed by
	// "<dir>/<file>" so the exemption stays as narrow as its reason: the
	// asynchronous_insert_log table was added in 22.10, so onprem/gov have
	// no root file — but cloud DOES (it never runs anything that old), and
	// there the exemption must not apply.
	versionOnly := map[string]bool{
		"queries.onprem/system.asynchronous_insert_log_7_days.sql": true,
		"queries.gov/system.asynchronous_insert_log_7_days.sql":    true,
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
				if f.DirName == "" || versionOnly[filepath.Base(dir)+"/"+f.Name] {
					continue
				}
				if _, err := os.Stat(filepath.Join(dir, f.Name)); err != nil {
					t.Errorf("override %s/%s has no root twin — renamed override becomes a separate file", f.DirName, f.Name)
				}
			}
		})
	}
}

// TestRealRepoLadders_Monotonic automates the check the SQL review has been
// doing by hand every round: as the server version rises, a file's selected
// column set must never SHRINK. A rung that forgets a column its lower rung
// had silently drops data on newer servers — and only on newer servers,
// which is exactly the case nobody runs locally.
//
// Column sets are approximated by the aliases/bare columns each file
// projects; that's coarse but sufficient to catch a dropped column, which is
// the failure mode.
func TestRealRepoLadders_Monotonic(t *testing.T) {
	ladders := []internal.Version{
		{Major: 22, Minor: 8, Patch: 1}, {Major: 22, Minor: 11, Patch: 1},
		{Major: 23, Minor: 4, Patch: 1}, {Major: 23, Minor: 11, Patch: 1},
		{Major: 24, Minor: 2, Patch: 1}, {Major: 25, Minor: 4, Patch: 1},
	}
	// No placeholder exemption: a lower rung that stands in a literal
	// ('n/a' AS size_uncompressed) still aliases it to the real output name,
	// so the higher rung keeps the same name and the set never shrinks. That
	// convention is what makes this check simple — enforce it rather than
	// carve an exception. (An earlier version had a regex here that could
	// never match, because projectedColumns returns output NAMES, not
	// expressions.)
	type seen struct {
		cols map[string]bool
		ver  internal.Version
		dir  string
	}
	for _, dir := range []string{"../../queries.onprem", "../../queries.gov", "../../queries.cloud", "../../queries.query_analysis"} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				t.Skipf("%s not present", dir)
			}
			prev := map[string]seen{} // file → what the previously-selected rung projected
			for _, v := range ladders {
				files, err := FindVersionedFiles(dir, v, ".sql")
				if err != nil {
					t.Fatalf("%d.%d: %v", v.Major, v.Minor, err)
				}
				for _, f := range files {
					raw, err := os.ReadFile(f.FullPath)
					if err != nil {
						t.Fatal(err)
					}
					cur := projectedColumns(string(raw))
					p, had := prev[f.Name]
					if had {
						for col := range p.cols {
							if !cur[col] {
								t.Errorf("%s: column %q was projected by the rung selected at %d.%d (%s) "+
									"but is missing from the rung selected at %d.%d (%s) — ladder must not shrink",
									f.Name, col,
									p.ver.Major, p.ver.Minor, rungLabel(p.dir),
									v.Major, v.Minor, rungLabel(f.DirName))
							}
						}
					}
					prev[f.Name] = seen{cols: cur, ver: v, dir: f.DirName}
				}
			}
		})
	}
}

// rungLabel names a rung for failure messages ("root" when the file came
// from the directory root rather than a version subdirectory).
func rungLabel(dirName string) string {
	if dirName == "" {
		return "root"
	}
	return dirName + "/"
}

// projectedColumns extracts the output column names a query projects: the
// alias when one is present, otherwise the bare column. Comments, the FROM
// clause onward, and aggregate internals are ignored.
func projectedColumns(sql string) map[string]bool {
	out := map[string]bool{}
	reAlias := regexp.MustCompile(`(?i)\sAS\s+` + "`?" + `([A-Za-z_][A-Za-z0-9_.]*)` + "`?" + `\s*,?$`)
	reBare := regexp.MustCompile("^`?([A-Za-z_][A-Za-z0-9_.]*)`?,$")
	for _, line := range strings.Split(sql, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "--") {
			continue
		}
		up := strings.ToUpper(s)
		if strings.HasPrefix(up, "FROM ") || strings.HasPrefix(up, "WHERE ") ||
			strings.HasPrefix(up, "GROUP BY") || strings.HasPrefix(up, "ORDER BY") ||
			strings.HasPrefix(up, "FORMAT ") || strings.HasPrefix(up, "LIMIT ") {
			continue
		}
		if m := reAlias.FindStringSubmatch(s); m != nil {
			out[m[1]] = true
			continue
		}
		if m := reBare.FindStringSubmatch(s); m != nil {
			out[m[1]] = true
		}
	}
	return out
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
