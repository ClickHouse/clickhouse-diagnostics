package query

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every shipped query file must pass the same check the executor runs before
// sending it. The executor refuses a file that fails — at collection time, on
// the customer's server, where the only trace is one "Error executing query"
// line and a missing file in the bundle. The inline spot checks above cannot
// catch that: a Keeper operation named in a string literal ('Create') once
// tripped the CREATE rule in a new collector and only a lab run noticed.
func TestShippedQueryFiles_PassTheSafetyCheck(t *testing.T) {
	roots := []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov", "../../queries.query_analysis"}
	checked := 0
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			t.Fatalf("%s not present", root)
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checked++
			if verr := ValidateQueryContent(string(b)); verr != nil {
				t.Errorf("%s: %v", strings.TrimPrefix(path, "../../"), verr)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 100 {
		t.Fatalf("checked only %d query files — the walk is not reaching the shipped trees", checked)
	}
}
