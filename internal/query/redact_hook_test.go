package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/collection"
)

// 脱敏映射以 collector 文件名作为键。重命名 system.tables.sql 会悄悄关闭
// 脱敏，因此每个键都必须仍是每个拥有该文件的模式目录中的真实文件。
func TestSensitiveCollectorFieldsMatchRealFiles(t *testing.T) {
	for name := range collection.SensitiveCollectorFields {
		found := false
		for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				found = true
			}
		}
		if !found {
			t.Errorf("SensitiveCollectorFields 命名了 %q，但没有任何模式目录随包提供该文件 —— 脱敏已悄悄关闭", name)
		}
	}
}

func TestRedactResult(t *testing.T) {
	line := `{"database":"d","name":"t","create_table_query":"CREATE TABLE d.t (x UInt8) ENGINE = MySQL('h:3306', 'db', 'tbl', 'u', 'pw123')","engine_full":"MySQL('h:3306', 'db', 'tbl', 'u', 'pw123')","as_select":""}` + "\n"

	out, note := redactResult("system.tables.sql", ".jsonl", line)
	if strings.Contains(out, "pw123") {
		t.Errorf("密码仍然存在：%s", out)
	}
	if !strings.Contains(note, "2 个凭据") {
		t.Errorf("note = %q，期望 2 个凭据的计数", note)
	}

	// 不携带 DDL 的 collector 保持原样，逐字节不变。
	other := `{"database":"d","name":"pw123 is a table name here"}` + "\n"
	if out, note := redactResult("system.databases.sql", ".jsonl", other); out != other || note != "" {
		t.Errorf("非敏感 collector 被改动了：%q / %q", out, note)
	}

	// 非 JSONL 无法逐字段脱敏：原样写出，但会标记出来。
	if out, note := redactResult("system.tables.sql", ".native", "opaque"); out != "opaque" || !strings.Contains(note, "未脱敏") {
		t.Errorf("native：%q / %q", out, note)
	}
}
