package query

import (
	"fmt"
	"strings"
)

// OutputFormat is the ClickHouse format used to serialise query results
// into the support bundle.
//
// The historical format was Native: compact, exactly typed, and reloadable
// with no schema. It is also column-oriented binary, so reading a bundle
// means loading it into a ClickHouse first — no grep, no jq, and nothing
// an LLM-based tool can read directly. Since the archive is already
// tar.gz'd (internal/archive.go), the compressed cost of a text format is
// within roughly 10% of Native, which makes readability nearly free.
type OutputFormat struct {
	// Name is the value accepted on the command line.
	Name string
	// Clause is the ClickHouse FORMAT appended to each query.
	Clause string
	// Ext is the output file extension, including the leading dot.
	Ext string
}

var (
	// FormatJSONL is one self-describing JSON object per line. Preferred
	// because every row carries its column names: system.query_log has
	// ~60 columns, and positional formats make a row unreadable without
	// counting fields back to a header.
	FormatJSONL = OutputFormat{Name: "jsonl", Clause: "JSONEachRow", Ext: ".jsonl"}

	// FormatNative is the original binary format. Kept for bundles that
	// will be bulk-reloaded into ClickHouse, where exact type fidelity
	// without a target schema matters more than being able to read them.
	FormatNative = OutputFormat{Name: "native", Clause: "Native", Ext: ".native"}

	// FormatTSV carries names AND types in its first two lines, so it is
	// the most faithful text format for reload, and the smallest. It is
	// positional, so it reads poorly for wide tables.
	FormatTSV = OutputFormat{Name: "tsv", Clause: "TSVWithNamesAndTypes", Ext: ".tsv"}
)

var outputFormats = []OutputFormat{FormatJSONL, FormatNative, FormatTSV}

// DefaultOutputFormat is what runs when --output-format is not given.
var DefaultOutputFormat = FormatJSONL

// ParseOutputFormat resolves a command-line value to a format.
func ParseOutputFormat(s string) (OutputFormat, error) {
	want := strings.ToLower(strings.TrimSpace(s))
	if want == "" {
		return DefaultOutputFormat, nil
	}
	for _, f := range outputFormats {
		if f.Name == want {
			return f, nil
		}
	}
	return OutputFormat{}, fmt.Errorf("--output-format must be one of %s (got %q)",
		OutputFormatNames(), s)
}

// OutputFormatNames lists the accepted values, for flag help and errors.
func OutputFormatNames() string {
	names := make([]string, 0, len(outputFormats))
	for _, f := range outputFormats {
		names = append(names, f.Name)
	}
	return strings.Join(names, "|")
}

// IsJSON reports whether this format emits JSON, and therefore needs the
// 64-bit integer quoting guarantee (see pkg.ClickHouseClient).
func (f OutputFormat) IsJSON() bool {
	return strings.HasPrefix(f.Clause, "JSON")
}

// Apply returns sql with any trailing FORMAT clause replaced by this
// format's. Query files carry no FORMAT of their own, but overrides,
// templates and hand-edited files might, and appending a second FORMAT
// would be a syntax error.
func (f OutputFormat) Apply(sql string) string {
	return strings.TrimRight(StripTrailingFormat(sql), " \t\r\n") + "\nFORMAT " + f.Clause
}

// StripTrailingFormat removes a trailing `FORMAT <name>` clause.
func StripTrailingFormat(sql string) string {
	return reFormatClause.ReplaceAllString(strings.TrimRight(sql, " \t\r\n"), "")
}
