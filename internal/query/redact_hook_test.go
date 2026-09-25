package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/collection"
)

// The redaction map is keyed by collector file name. A rename of
// system.tables.sql would silently switch the redaction off, so every key must
// still be a real file in every mode directory that has it.
func TestSensitiveCollectorFieldsMatchRealFiles(t *testing.T) {
	for name := range collection.SensitiveCollectorFields {
		found := false
		for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				found = true
			}
		}
		if !found {
			t.Errorf("SensitiveCollectorFields names %q but no mode directory ships that file — redaction is silently off", name)
		}
	}
}

func TestRedactResult(t *testing.T) {
	line := `{"database":"d","name":"t","create_table_query":"CREATE TABLE d.t (x UInt8) ENGINE = MySQL('h:3306', 'db', 'tbl', 'u', 'pw123')","engine_full":"MySQL('h:3306', 'db', 'tbl', 'u', 'pw123')","as_select":""}` + "\n"

	out, note := redactResult("system.tables.sql", ".jsonl", line)
	if strings.Contains(out, "pw123") {
		t.Errorf("password survived: %s", out)
	}
	if !strings.Contains(note, "2 credential(s)") {
		t.Errorf("note = %q, want a 2-credential count", note)
	}

	// A collector that carries no DDL is untouched, byte for byte.
	other := `{"database":"d","name":"pw123 is a table name here"}` + "\n"
	if out, note := redactResult("system.databases.sql", ".jsonl", other); out != other || note != "" {
		t.Errorf("non-sensitive collector changed: %q / %q", out, note)
	}

	// Non-JSONL cannot be redacted field by field: written as-is, but flagged.
	if out, note := redactResult("system.tables.sql", ".native", "opaque"); out != "opaque" || !strings.Contains(note, "NOT redacted") {
		t.Errorf("native: %q / %q", out, note)
	}
}
