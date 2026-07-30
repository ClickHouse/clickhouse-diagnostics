package query

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGovQueries_NoRawIdentifiersOrDDL guards the gov mode promise at CI
// time: every .sql under queries.gov/ (root AND version subdirectories)
// must hash identifier columns and must not ship schema/DDL text.
//
// This exists because that promise was silently broken by a file that was
// a verbatim copy of the onprem variant — queries.gov/25.4.1.0/
// system.tables.sql shipped raw database/table names plus full DDL until
// it was found by reading. Copy-paste is the natural failure mode when
// gov mirrors onprem's version ladder, so it needs a test, not vigilance.
func TestGovQueries_NoRawIdentifiersOrDDL(t *testing.T) {
	govDir := "../../queries.gov"
	if _, err := os.Stat(govDir); os.IsNotExist(err) {
		t.Skip("queries.gov/ not present")
	}

	// Columns that expose schema or infrastructure text wholesale. These
	// must never appear in a gov query, hashed or not — hashing a DDL blob
	// is pointless and it has no diagnostic value in gov mode.
	forbidden := []string{
		"create_table_query",
		"engine_full",
		"as_select",
		"metadata_path",
		"data_paths",
		"dependencies_database",
		"dependencies_table",
		"loading_dependencies_database",
		"loading_dependencies_table",
		"loading_dependent_database",
		"loading_dependent_table",
		"parameterized_view_parameters",
	}

	// Identifier columns that must be hashed if the file selects them.
	//
	// Checked per FILE rather than per line: gov queries legitimately
	// select a bare column inside a subquery that the outer SELECT then
	// hashes (system.disks does exactly that). So the rule is "if this
	// file projects the identifier at all, it must hash it somewhere" —
	// which is precisely the copy-paste regression this guards against (an
	// onprem file dropped into queries.gov/ hashes nothing). It does not
	// catch a file that hashes a column AND also emits it raw; that needs
	// real result-column analysis, so it stays a review concern.
	// Covers identifier columns AND the free-text columns that carry
	// identifiers inside them — every one of these was found raw by review
	// and hashed by hand during this branch, so the guard has to include
	// them or it protects only the leaks nobody made.
	mustBeHashed := []string{
		"database", "table", "name", "user", "path", "host_name", "host_address",
		"last_exception", "postpone_reason", "last_error_message",
		"partition", "origin", "replica_name", "comment", "source",
	}

	// Documented exemptions: the column name collides with an identifier
	// but the value is not customer data. Keyed by BASE name so a future
	// version-directory copy (queries.gov/25.x/system.errors.sql) keeps the
	// exemption instead of failing spuriously.
	exempt := map[string]map[string]bool{
		// system.errors.name is a ClickHouse error constant (UNKNOWN_TABLE,
		// TOO_MANY_PARTS, …), not a schema identifier.
		"system.errors.sql": {"name": true},
	}

	var files []string
	err := filepath.Walk(govDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(p, ".sql") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no gov .sql files found")
	}

	for _, path := range files {
		rel, _ := filepath.Rel(govDir, path)
		t.Run(rel, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Strip comment lines: they legitimately name columns.
			var code []string
			for _, line := range strings.Split(string(raw), "\n") {
				s := strings.TrimSpace(line)
				if s != "" && !strings.HasPrefix(s, "--") {
					code = append(code, s)
				}
			}
			body := strings.Join(code, "\n")

			for _, f := range forbidden {
				if regexp.MustCompile(`\b` + f + `\b`).MatchString(body) {
					t.Errorf("ships schema/DDL column %q — gov must not expose it", f)
				}
			}
			for _, id := range mustBeHashed {
				if exempt[filepath.Base(rel)][id] {
					continue
				}
				// Matches the identifier as its own projection. Both ends need
				// alternatives beyond "own line":
				//   leading  — line start, a comma, or SELECT (the first item
				//              of an inline list is preceded by neither).
				//   trailing — a comma, end of line, or FROM (the last item of
				//              a single-line query is followed by neither).
				// Without both, "SELECT database FROM system.databases" slips
				// through entirely — which it did until this was widened.
				projected := regexp.MustCompile(
					`(?mi)(?:^|,|\bSELECT\b)\s*` + id + `\s*(?:,|$|\s+FROM\b)`).MatchString(body)
				// "Hashed" means some SHA256 expression is aliased to this
				// name — covers both the direct form
				// (SHA256(concat(database, …)) AS database) and hashing a
				// derived expression (… concat(splitByChar('.', tables)[1],
				// …) as database), which query_log_details uses.
				hashed := regexp.MustCompile(`(?i)SHA256\(.*\)\s*AS\s+` + id + `\b`).MatchString(body)
				if projected && !hashed {
					t.Errorf("projects identifier %q but never hashes it — looks like an unhashed copy of the onprem/cloud variant", id)
				}
			}
		})
	}
}
