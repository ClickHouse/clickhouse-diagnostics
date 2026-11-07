package query

import (
	"strings"
	"testing"
	"time"
)

func TestSysTable_CloudWrapping(t *testing.T) {
	// per-replica 表在 cloud 下会加上 clusterAllReplicas，其他模式则用普通引用。
	cases := []struct {
		mode, table, want string
	}{
		{"cloud", "query_log", "clusterAllReplicas(default, system.query_log)"},
		{"cloud", "text_log", "clusterAllReplicas(default, system.text_log)"},
		{"cloud", "errors", "clusterAllReplicas(default, system.errors)"},
		{"cloud", "parts", "system.parts"},   // shared —— 不包裹
		{"cloud", "tables", "system.tables"}, // shared
		{"cloud", "replicas", "system.replicas"},
		// system.dictionaries 是 per-replica 的（即使定义共享，
		// 运行时状态也不同），因此 cloud 必须包裹。
		{"cloud", "dictionaries", "clusterAllReplicas(default, system.dictionaries)"},
		{"onprem", "query_log", "system.query_log"},
		{"gov", "text_log", "system.text_log"},
		{"CLOUD", "query_log", "clusterAllReplicas(default, system.query_log)"}, // 不区分大小写
	}
	for _, c := range cases {
		got := SysTable(c.mode, c.table)
		if got != c.want {
			t.Errorf("SysTable(%q, %q) = %q，期望 %q", c.mode, c.table, got, c.want)
		}
	}
}

func TestApply_AllPlaceholders(t *testing.T) {
	sql := `SELECT * FROM {sys.query_log}
WHERE query_id = {query_id}
  AND normalized_query_hash = {normalized_query_hash}
  AND event_time BETWEEN {from} AND {to}`
	vars := Vars{
		Mode:                "cloud",
		QueryID:             "abc-123",
		NormalizedQueryHash: "9876543210",
		From:                time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		To:                  time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC),
	}
	out := Apply(sql, vars)
	want := []string{
		"clusterAllReplicas(default, system.query_log)",
		"query_id = 'abc-123'",
		"normalized_query_hash = 9876543210", // 不带引号的数值
		"'2026-05-01 10:00:00'",
		"'2026-05-08 10:00:00'",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("缺少子串 %q：\n%s", w, out)
		}
	}
}

func TestApply_EmptyValuesLeavePlaceholdersIntact(t *testing.T) {
	// 未设置 QueryID 时调用 Apply 必须让 {query_id} 保持原样，
	// 以便 executor 的未绑定占位符检查能捕获它。
	sql := "SELECT 1 WHERE query_id = {query_id}"
	out := Apply(sql, Vars{Mode: "onprem"})
	if !strings.Contains(out, "{query_id}") {
		t.Errorf("期望 {query_id} 保持未替换，实际为：%s", out)
	}
}

func TestApply_GovSalt(t *testing.T) {
	sql := "SELECT hex(SHA256(concat(database, '%salt%')))"
	out := Apply(sql, Vars{Mode: "gov", GovSalt: "customerABC2026"})
	if strings.Contains(out, "%salt%") {
		t.Errorf("salt 占位符未被替换：%s", out)
	}
	if !strings.Contains(out, "'customerABC2026'") {
		t.Errorf("缺少替换后的 salt：%s", out)
	}
}

func TestUnboundPlaceholders(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"SELECT 1", nil},
		{"WHERE x = {query_id}", []string{"{query_id}"}},
		{"WHERE x = {query_id} AND y = {from}", []string{"{query_id}", "{from}"}},
		// '%salt%' 不会被标记
		{"WHERE x = '%salt%'", nil},
		// 如果跳过了 Apply，sys 占位符会被标记 —— 调用方应始终先调用 Apply
		{"FROM {sys.parts}", []string{"{sys.parts}"}},
		// 空的 `{}` 花括号（message_format_string 字面量中的 ClickHouse
		// 日志格式标记）不会被标记。
		{"WHERE x = 'Selected {}/{} parts by primary key'", nil},
		// 对重复占位符去重。
		{"q = {query_id} AND r = {query_id}", []string{"{query_id}"}},
	}
	for _, c := range cases {
		got := UnboundPlaceholders(c.in)
		if len(got) != len(c.want) {
			t.Errorf("UnboundPlaceholders(%q) = %v，期望 %v", c.in, got, c.want)
			continue
		}
		for i, w := range c.want {
			if got[i] != w {
				t.Errorf("UnboundPlaceholders(%q)[%d] = %q，期望 %q", c.in, i, got[i], w)
			}
		}
	}
}
