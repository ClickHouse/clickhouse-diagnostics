package query

import (
	"strings"
	"testing"
	"time"

	"clickhouse-diagnostic/internal"
)

func win(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	from, _ := time.Parse(time.RFC3339, "2026-08-01T10:00:00Z")
	return from, from.Add(2 * time.Hour)
}

// TestTextLogOpts_RequiresWindow 固定住该采集器需要显式启用的理由：
// text_log 数据量很大，因此无界导出会被拒绝，而不是悄悄产出数 GB 的归档。
func TestTextLogOpts_RequiresWindow(t *testing.T) {
	from, to := win(t)
	cases := []struct {
		name    string
		opts    TextLogOpts
		wantErr string
	}{
		{"no window at all", TextLogOpts{}, "需要同时提供 --from 和 --to"},
		{"only from", TextLogOpts{From: from}, "需要同时提供 --from 和 --to"},
		{"only to", TextLogOpts{To: to}, "需要同时提供 --from 和 --to"},
		{"inverted", TextLogOpts{From: to, To: from}, "必须晚于"},
		{"bad level", TextLogOpts{From: from, To: to, Level: "verbose"}, "不是 ClickHouse 日志级别"},
		{"valid", TextLogOpts{From: from, To: to}, ""},
		{"valid with level", TextLogOpts{From: from, To: to, Level: "warning"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.opts.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("意外错误：%v", err)
				}
				if c.opts.RowLimit != DefaultTextLogRowLimit {
					t.Errorf("行数上限应默认为 %d，实际为 %d",
						DefaultTextLogRowLimit, c.opts.RowLimit)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("得到 %v，期望包含 %q 的错误", err, c.wantErr)
			}
		})
	}
}

// TestTextLogSQL_VersionGate：message_format_string 在 23.1 中加入，
// 因此低于该版本时不得选择它 —— 这与 .sql 各版本层级所用的门控相同。
func TestTextLogSQL_VersionGate(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	c := NewTextLogCollector(nil, "onprem")

	old := c.buildSQL(opts, internal.Version{Major: 22, Minor: 8})
	if strings.Contains(old, "message_format_string") {
		t.Error("22.8 不得选择 message_format_string（23.1 才加入）")
	}
	newer := c.buildSQL(opts, internal.Version{Major: 23, Minor: 1})
	if !strings.Contains(newer, "message_format_string") {
		t.Error("23.1+ 应选择 message_format_string")
	}
	if !strings.Contains(c.buildSQL(opts, internal.Version{Major: 25, Minor: 4}), "message_format_string") {
		t.Error("25.4 应选择 message_format_string")
	}
}

// TestTextLogSQL_ModeAwareTable：cloud 必须扇出，单节点不得扇出。
func TestTextLogSQL_ModeAwareTable(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to}
	_ = opts.Validate()
	v := internal.Version{Major: 25, Minor: 4}

	cloud := NewTextLogCollector(nil, "cloud").buildSQL(opts, v)
	if !strings.Contains(cloud, "clusterAllReplicas(default, system.text_log)") {
		t.Errorf("cloud 应扇出，实际为：\n%s", cloud)
	}
	for _, mode := range []string{"onprem", "gov"} {
		sql := NewTextLogCollector(nil, mode).buildSQL(opts, v)
		if strings.Contains(sql, "clusterAllReplicas") {
			t.Errorf("%s 必须查询本地表，实际为：\n%s", mode, sql)
		}
	}
}

func TestTextLogSQL_WindowLevelAndLimit(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to, Level: "warning", RowLimit: 42}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	sql := NewTextLogCollector(nil, "onprem").buildSQL(opts, internal.Version{Major: 25, Minor: 4})

	for _, want := range []string{
		"2026-08-01 10:00:00", // from，UTC
		"2026-08-01 12:00:00", // to，UTC
		"LIMIT 42",
		"level <= 'Warning'", // 枚举按严重程度排序，因此“至少该严重级别”用 <=
		"ORDER BY event_time_microseconds ASC",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL 缺少 %q：\n%s", want, sql)
		}
	}
	// 生成的 SQL 必须能通过其他每个查询都要经过的同一项只读校验。
	if err := ValidateQueryContent(sql); err != nil {
		t.Errorf("生成的 text_log SQL 未通过安全校验：%v", err)
	}
}

func TestTextLogSQL_NoLevelFilterWhenUnset(t *testing.T) {
	from, to := win(t)
	opts := TextLogOpts{From: from, To: to}
	_ = opts.Validate()
	sql := NewTextLogCollector(nil, "onprem").buildSQL(opts, internal.Version{Major: 25, Minor: 4})
	if strings.Contains(sql, "level <=") {
		t.Error("没有 --text-log-level 时应表示不过滤级别")
	}
}

// TestLevelCanonicalisation 固定住取代已废弃的 strings.Title 往返的
// 不区分大小写级别处理：真实级别的任何大小写都会解析为确切的枚举拼写，
// 并且只有这个已被证明的拼写才会被拼接进 SQL。
func TestLevelCanonicalisation(t *testing.T) {
	for in, want := range map[string]string{
		"error": "Error", "ERROR": "Error", "eRrOr": "Error",
		"information": "Information", "FATAL": "Fatal",
	} {
		got, ok := canonicalLevel(in)
		if !ok || got != want {
			t.Errorf("canonicalLevel(%q) = %q,%v；期望 %q,true", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "err", "warn", "Errors", "critical1"} {
		if got, ok := canonicalLevel(bad); ok {
			t.Errorf("canonicalLevel(%q) 被接受为 %q；必须拒绝非成员", bad, got)
		}
	}

	// 端到端：小写的 flag 值必须以枚举拼写到达 SQL。
	opts := TextLogOpts{
		From:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		To:    time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		Level: "warning",
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("小写级别被拒绝：%v", err)
	}
	c := NewTextLogCollector(nil, "onprem")
	sql := c.buildSQL(opts, internal.Version{Major: 24, Minor: 8})
	if !strings.Contains(sql, "level <= 'Warning'") {
		t.Errorf("SQL 未使用规范拼写：\n%s", sql)
	}
}
