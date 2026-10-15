package query

import (
	"testing"
	"time"
)

// Bundles are named clickhouse_diagnostic_<ts>: the run folder, and the
// archive built from it. Releases before the rename wrote
// clickhouse_backup_<ts>, which the pre-pass still reads.
func TestRunFolderName(t *testing.T) {
	at := time.Date(2026, 10, 15, 13, 45, 7, 0, time.Local)
	if got, want := RunFolderName(at), "clickhouse_diagnostic_20261015_134507"; got != want {
		t.Errorf("RunFolderName = %q, want %q", got, want)
	}
}
