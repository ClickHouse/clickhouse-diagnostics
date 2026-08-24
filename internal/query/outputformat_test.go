package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realRepoSQLFiles walks the shipped query directories, matching the
// ../../ convention used by gov_leak_test.go.
func realRepoSQLFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{
		"../../queries.onprem", "../../queries.cloud",
		"../../queries.gov", "../../queries.query_analysis",
	} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.EqualFold(filepath.Ext(p), ".sql") {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return out
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestParseOutputFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    OutputFormat
		wantErr bool
	}{
		{"", DefaultOutputFormat, false},
		{"jsonl", FormatJSONL, false},
		{"JSONL", FormatJSONL, false},
		{"  native  ", FormatNative, false},
		{"tsv", FormatTSV, false},
		{"json", OutputFormat{}, true},        // near-miss must not silently resolve
		{"JSONEachRow", OutputFormat{}, true}, // the clause is not the flag value
		{"parquet", OutputFormat{}, true},
	}
	for _, c := range cases {
		got, err := ParseOutputFormat(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseOutputFormat(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("ParseOutputFormat(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// TestDefaultIsReadable guards the reason this layer exists: the default
// must be a text format an operator (or a tool) can read without loading
// the bundle into a ClickHouse first.
func TestDefaultIsReadable(t *testing.T) {
	if DefaultOutputFormat.Clause == "Native" {
		t.Fatal("default output format is Native (binary); the point of the flag " +
			"is that bundles are readable without a ClickHouse to load them into")
	}
	if !DefaultOutputFormat.IsJSON() {
		t.Errorf("default = %q; expected a JSON format so every row carries its "+
			"column names", DefaultOutputFormat.Clause)
	}
}

func TestApplyReplacesExistingFormat(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"no format", "SELECT 1 FROM system.parts"},
		{"trailing native", "SELECT 1 FROM system.parts\nFORMAT Native"},
		{"lowercase", "SELECT 1 FROM system.parts\nformat native"},
		{"trailing whitespace", "SELECT 1 FROM system.parts\nFORMAT Native\n\n  "},
		{"other format", "SELECT 1 FROM system.parts FORMAT JSONCompact"},
	}
	for _, c := range cases {
		got := FormatJSONL.Apply(c.sql)
		if n := strings.Count(strings.ToUpper(got), "FORMAT "); n != 1 {
			t.Errorf("%s: got %d FORMAT clauses, want exactly 1:\n%s", c.name, n, got)
		}
		if !strings.HasSuffix(got, "\nFORMAT JSONEachRow") {
			t.Errorf("%s: does not end with the applied format:\n%q", c.name, got)
		}
		// Whatever we produce must still pass the security validator,
		// which is applied to this exact text before execution.
		if err := ValidateQueryContent(got); err != nil {
			t.Errorf("%s: applied SQL fails validation: %v", c.name, err)
		}
	}
}

// TestApplyPreservesFormatInsideQuery makes sure the strip is anchored to
// the end. `message_format_string` and columns like it contain "format",
// and truncating a query at the first match would silently drop columns.
func TestApplyPreservesFormatInsideQuery(t *testing.T) {
	sql := "SELECT message_format_string, formatReadableSize(bytes) FROM system.text_log\nFORMAT Native"
	got := FormatJSONL.Apply(sql)
	for _, want := range []string{"message_format_string", "formatReadableSize(bytes)"} {
		if !strings.Contains(got, want) {
			t.Errorf("Apply dropped %q from the query body:\n%s", want, got)
		}
	}
}

// TestNoQueryFileCarriesFormat pins the invariant that Go owns the output
// format. A stray FORMAT in a query file would be silently replaced by
// Apply, so this is about keeping the files honest rather than correctness.
func TestNoQueryFileCarriesFormat(t *testing.T) {
	files := realRepoSQLFiles(t)
	if len(files) == 0 {
		t.Skip("no query files found (running outside the repo)")
	}
	for _, f := range files {
		body := readFileForTest(t, f)
		if reFormatClause.MatchString(strings.TrimRight(body, " \t\r\n")) {
			t.Errorf("%s ends with a FORMAT clause; the output format is chosen "+
				"in Go via --output-format, so query files must not pin one", f)
		}
	}
}
