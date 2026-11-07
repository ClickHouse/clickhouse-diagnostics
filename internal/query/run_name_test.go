package query

import (
	"testing"
	"time"
)

// bundle 命名为 clickhouse_diagnostic_<ts>：包括运行目录以及由它构建的归档。
// 重命名之前的发行版本写的是 clickhouse_backup_<ts>，预检脚本仍会读取。
func TestRunFolderName(t *testing.T) {
	at := time.Date(2026, 10, 15, 13, 45, 7, 0, time.Local)
	if got, want := RunFolderName(at), "clickhouse_diagnostic_20261015_134507"; got != want {
		t.Errorf("RunFolderName = %q，期望 %q", got, want)
	}
}
