package query

import (
	"strings"
	"testing"
)

func TestDetachedScope(t *testing.T) {
	if got := DetachedScope(false, nil, ""); got != "1" {
		t.Fatalf("没有范围：%q", got)
	}
	got := DetachedScope(true, [][2]string{{"db", "t1"}, {"o'b", `x\y`}}, "")
	for _, want := range []string{
		"(database, table) IN (SELECT database, name FROM system.tables WHERE engine NOT LIKE 'Shared%')",
		"OR (database, table) IN (('db', 't1'), ('o\\'b', 'x\\\\y'))",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("范围缺少 %q：\n%s", want, got)
		}
	}
	if q := DetachedScope(true, nil, "p."); !strings.Contains(q, "(p.database, p.table) IN") || strings.Contains(q, " OR ") {
		t.Errorf("没有 signal 表时的限定范围：%q", q)
	}
}

func TestParseSignalTables(t *testing.T) {
	got := ParseSignalTables("db\tt1\n\tbad\ndb\tt1\nx\ty\n")
	if len(got) != 2 || got[0] != [2]string{"db", "t1"} || got[1] != [2]string{"x", "y"} {
		t.Fatalf("解析结果 %v", got)
	}
}

func TestApply_DetachedScopeDefaultsToEverything(t *testing.T) {
	sql := "SELECT * FROM system.detached_parts WHERE database != 'system' AND {detached_scope}"
	if got := Apply(sql, Vars{}); !strings.HasSuffix(got, "AND 1") {
		t.Errorf("默认范围：%q", got)
	}
	if got := Apply(sql, Vars{DetachedScope: "x = 1"}); !strings.HasSuffix(got, "AND (x = 1)") {
		t.Errorf("给定范围：%q", got)
	}
	g := "FROM system.detached_parts AS p WHERE {detached_scope_p}"
	if got := Apply(g, Vars{DetachedScopeP: "p.x = 1"}); !strings.HasSuffix(got, "WHERE (p.x = 1)") {
		t.Errorf("限定范围：%q", got)
	}
}
