package alert

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/query"
	"gopkg.in/yaml.v3"
)

// ── expandQuery ──────────────────────────────────────────────────────────────

func TestExpandQuery_OnPrem(t *testing.T) {
	ev := &Evaluator{mode: "onprem"}
	got := ev.expandQuery("SELECT * FROM {sys.mutations} WHERE parts_to_do > 0")
	want := "SELECT * FROM system.mutations WHERE parts_to_do > 0"
	if got != want {
		t.Errorf("得到 %q，期望 %q", got, want)
	}
}

func TestExpandQuery_Cloud_PerReplicaTable(t *testing.T) {
	ev := &Evaluator{mode: "cloud"}
	got := ev.expandQuery("SELECT * FROM {sys.query_log}")
	want := "SELECT * FROM clusterAllReplicas(default, system.query_log)"
	if got != want {
		t.Errorf("得到 %q，期望 %q", got, want)
	}
}

func TestExpandQuery_Cloud_SharedTable(t *testing.T) {
	// 共享表（parts、tables、replicas 等）在 cloud 中直接查询，
	// 因为每个 replica 看到相同的行。
	ev := &Evaluator{mode: "cloud"}
	for _, table := range []string{"parts", "tables", "replicas", "replication_queue", "mutations", "detached_parts"} {
		got := ev.expandQuery("SELECT * FROM {sys." + table + "}")
		want := "SELECT * FROM system." + table
		if got != want {
			t.Errorf("表 %q：得到 %q，期望 %q", table, got, want)
		}
	}
}

func TestExpandQuery_MultiPlaceholder(t *testing.T) {
	ev := &Evaluator{mode: "onprem"}
	got := ev.expandQuery("SELECT * FROM {sys.parts} JOIN {sys.tables} USING (database, table)")
	if !strings.Contains(got, "system.parts") || !strings.Contains(got, "system.tables") {
		t.Errorf("多占位符展开失败：%q", got)
	}
}

// ── FormattedMessages ────────────────────────────────────────────────────────

func TestFormattedMessages(t *testing.T) {
	r := Result{
		Message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h",
		Rows: []map[string]interface{}{
			{"mutation_id": "mutation_42", "database": "prod", "table": "events", "hours_running": "5"},
		},
	}
	msgs := r.FormattedMessages()
	if len(msgs) != 1 {
		t.Fatalf("期望 1 条消息，实际为 %d 条", len(msgs))
	}
	want := "Mutation mutation_42 on prod.events running 5h"
	if msgs[0] != want {
		t.Errorf("得到 %q，期望 %q", msgs[0], want)
	}
}

func TestFormattedMessages_MultiRow(t *testing.T) {
	r := Result{
		Message: "{database}.{table} has {detached_count} detached part(s)",
		Rows: []map[string]interface{}{
			{"database": "db1", "table": "t1", "detached_count": "3"},
			{"database": "db2", "table": "t2", "detached_count": "7"},
		},
	}
	msgs := r.FormattedMessages()
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息，实际为 %d 条", len(msgs))
	}
}

// ── Notable ────────────────────────────────────────────────────────────────────

func TestNotable_NoRows(t *testing.T) {
	r := Result{Rows: []map[string]interface{}{}}
	if r.Notable() {
		t.Error("空行不得是 Notable")
	}
}

func TestNotable_WithRows(t *testing.T) {
	r := Result{Rows: []map[string]interface{}{{"x": "1"}}}
	if !r.Notable() {
		t.Error("存在行时必须是 Notable")
	}
}

func TestNotable_WithError(t *testing.T) {
	r := Result{Error: "connection refused"}
	if !r.Notable() {
		t.Error("出错的结果必须是 Notable（它需要展示）—— 但 Summarize 不得把它计为 fired")
	}
}

// ── parseJSONCompact ─────────────────────────────────────────────────────────

func TestParseJSONCompact(t *testing.T) {
	raw := `{"meta":[{"name":"exception_code"},{"name":"error_count"}],"data":[["241","15"],["60","8"]],"rows":2}`
	rows, err := parseJSONCompact(raw)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("期望 2 行，实际为 %d 行", len(rows))
	}
	if rows[0]["exception_code"] != "241" {
		t.Errorf("期望 '241'，实际为 %v", rows[0]["exception_code"])
	}
}

func TestParseJSONCompact_Empty(t *testing.T) {
	raw := `{"meta":[{"name":"id"}],"data":[],"rows":0}`
	rows, err := parseJSONCompact(raw)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(rows) != 0 {
		t.Errorf("期望 0 行，实际为 %d 行", len(rows))
	}
}

// ── RunAll（文件系统，无 ClickHouse）─────────────────────────────────────────

func TestRunAll_MissingDir(t *testing.T) {
	ev := &Evaluator{mode: "onprem"}
	results := ev.RunAll("/nonexistent/dir/that/does/not/exist", internal.Version{Major: 25, Minor: 4, Patch: 1, Build: 0})
	if results != nil {
		t.Errorf("目录缺失时期望 nil，实际为 %v", results)
	}
}

func TestYAML_LoadsCorrectly(t *testing.T) {
	raw := []byte(`
name: test_alert
title: "Test Alert"
severity: warning
query: |
  SELECT database, table, count() AS parts
  FROM {sys.parts}
  WHERE active = 1
  GROUP BY database, table
  HAVING parts > 1000
message: "{database}.{table} has {parts} active parts"
tags:
  - test
`)
	var def Definition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		t.Fatalf("yaml 反序列化失败：%v", err)
	}
	if def.Name != "test_alert" {
		t.Errorf("name：得到 %q，期望 %q", def.Name, "test_alert")
	}
	if def.Severity != "warning" {
		t.Errorf("severity：得到 %q，期望 %q", def.Severity, "warning")
	}
	if !strings.Contains(def.Query, "{sys.parts}") {
		t.Error("query 应包含 {sys.parts} 占位符")
	}
}

func TestSecurity_ForbiddenQueryRejected(t *testing.T) {
	dir := t.TempDir()
	badYAML := []byte(`
name: bad_alert
title: "Forbidden"
severity: critical
query: |
  DROP TABLE system.parts
message: "should never fire"
`)
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(badYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// 直接调用 evalFile —— 无需 ClickHouse client；安全检查在查询执行前触发
	ev := &Evaluator{mode: "onprem"}
	r := ev.evalFile(filepath.Join(dir, "bad.yaml"))
	if !strings.Contains(r.Error, "security") {
		t.Errorf("期望安全错误，实际为：%q", r.Error)
	}
}

func TestRunAll_IgnoresNonYAML(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hello"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("some notes"), 0644)

	// 这里 client 为空也没问题，因为不存在 .yaml 文件，RunAll 会提前返回。
	ev := &Evaluator{mode: "onprem"}
	results := ev.RunAll(dir, internal.Version{Major: 25, Minor: 4, Patch: 1, Build: 0})
	if len(results) != 0 {
		t.Errorf("期望 0 个结果（没有 yaml 文件），实际为 %d 个", len(results))
	}
}

// ── 来自 alerts/ 目录的真实告警 YAML 文件 ───────────────────────────────────

// TestSummarize 断言关键的报送不变量：skipped 的规则（表不存在）
// 被排除在 "evaluated" 之外 —— 从未运行的检查不得被读作通过的检查。
func TestSummarize(t *testing.T) {
	results := []Result{
		{Name: "fired", Rows: []map[string]interface{}{{"a": 1}}}, // 已触发
		{Name: "clean"},                         // 已评估，未触发
		{Name: "errored", Error: "query: boom"}, // 已出错，未触发
		{Name: "skipped", Skipped: true, Reason: "table not present"}, // 未评估
	}
	evaluated, fired, errored, skipped := Summarize(results)
	// evaluated = 给出了答案。skipped 的规则（从未运行）和
	// errored 的规则（运行了，但没得到结果）都被排除 —— 把任一者报告为
	// "checked" 都是在声称并未发生的验证。
	if evaluated != 2 {
		t.Errorf("evaluated = %d，期望 2（skipped 和 errored 的规则都必须排除）", evaluated)
	}
	// fired 只统计真正的 findings。把错误折叠进来会把一个权限问题
	// 报告成 N 个生产问题 —— 这是针对受限的 Cloud 用户实际观察到的，
	// 当时全部 11 条规则都出错了。
	if fired != 1 {
		t.Errorf("fired = %d，期望 1（只统计匹配的行，不统计错误）", fired)
	}
	if errored != 1 {
		t.Errorf("errored = %d，期望 1（与 fired 分开计数）", errored)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d，期望 1", skipped)
	}
	// 被跳过的结果既不是 fired 也不是 errored。
	if results[3].Notable() {
		t.Error("Skipped 的结果不得报告 Notable()")
	}
}

// TestWriteSummaryJSON 覆盖没有 dashboard 时支持工程师会阅读的唯一产物：
// 四种状态必须正确映射，且匹配的行绝不能出现 —— 它们的列由规则定义，
// 因此在 gov 模式下会携带 gov 在其他所有地方都会哈希的标识符。
func TestWriteSummaryJSON(t *testing.T) {
	results := []Result{
		{Name: "fired_rule", Title: "Fired", Severity: SeverityCritical,
			Rows: []map[string]interface{}{
				{"database": "secretdb", "table": "customer_pii"},
				{"database": "secretdb", "table": "other"},
			}},
		{Name: "clean_rule", Title: "Clean", Severity: SeverityWarning},
		{Name: "errored_rule", Title: "Errored", Severity: SeverityWarning, Error: "query: boom"},
		{Name: "skipped_rule", Title: "Skipped", Skipped: true, Reason: "table not present"},
	}

	for _, mode := range []string{"onprem", "gov"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteSummaryJSON(dir, results, mode); err != nil {
				t.Fatal(err)
			}
			blob, err := os.ReadFile(filepath.Join(dir, "alerts_summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			// 行内容在任何模式下都不得泄漏。
			for _, leak := range []string{"secretdb", "customer_pii"} {
				if strings.Contains(string(blob), leak) {
					t.Errorf("行内容 %q 泄漏进了 alerts_summary.json", leak)
				}
			}
			var got struct {
				Evaluated int `json:"evaluated"`
				Fired     int `json:"fired"`
				Errored   int `json:"errored"`
				// 需要该 tag：Go 不会把 not_applicable 映射到 NotApplicable。
				NotApplicable int `json:"not_applicable"`
				Note          string
				Rules         []struct {
					Name, State string
					// 同样需要 tag —— 键是 skip_reason，因此没有 tag 的
					// Reason 会悄悄保持为空。
					Reason string `json:"skip_reason"`
					Count  int    `json:"instance_count"`
				}
			}
			if err := json.Unmarshal(blob, &got); err != nil {
				t.Fatal(err)
			}
			if got.Evaluated != 2 || got.Fired != 1 || got.Errored != 1 || got.NotApplicable != 1 {
				t.Errorf("计数 = evaluated %d / fired %d / errored %d / n-a %d；期望 2/1/1/1",
					got.Evaluated, got.Fired, got.Errored, got.NotApplicable)
			}
			states := map[string]string{}
			counts := map[string]int{}
			reasons := map[string]string{}
			for _, r := range got.Rules {
				states[r.Name] = r.State
				counts[r.Name] = r.Count
				reasons[r.Name] = r.Reason
			}
			// dashboard 的 “not applicable” 说明正是根据 skip reason 渲染的，
			// 因此它必须能在序列化后保留。
			if reasons["skipped_rule"] != "table not present" {
				t.Errorf("skipped_rule skip_reason = %q，期望 %q", reasons["skipped_rule"], "table not present")
			}
			for name, want := range map[string]string{
				"fired_rule": "fired", "clean_rule": "clean",
				"errored_rule": "error", "skipped_rule": "skipped",
			} {
				if states[name] != want {
					t.Errorf("%s：state %q，期望 %q", name, states[name], want)
				}
			}
			// 计数是被省略行的替代品。
			if counts["fired_rule"] != 2 {
				t.Errorf("fired_rule instance_count = %d，期望 2", counts["fired_rule"])
			}
			// 说明不得把读者引向一个并不存在的产物。
			if strings.Contains(got.Note, "see the dashboard") {
				t.Errorf("说明指向了本归档中不存在的 dashboard：%q", got.Note)
			}
			if mode == "gov" && !strings.Contains(got.Note, "gov") {
				t.Errorf("gov 的说明应陈述隐私原因：%q", got.Note)
			}
		})
	}
}

// TestRunAll_OverrideFilePrefix 断言来自版本目录的规则会在 File 中报告
// 目录前缀（与 analysis.go 一致），以便支持工程师分辨运行的是哪个变体。
// 使用被安全校验拦截的规则：evalFile 在接触 client 之前就返回，
// 因此无需 ClickHouse 连接。
func TestRunAll_OverrideFilePrefix(t *testing.T) {
	dir := t.TempDir()
	blocked := []byte("name: r\ntitle: t\nquery: |\n  DROP TABLE x\nmessage: m\n")
	if err := os.WriteFile(filepath.Join(dir, "r.yaml"), blocked, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "22.11.1.0"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "22.11.1.0", "r.yaml"), blocked, 0644); err != nil {
		t.Fatal(err)
	}

	ev := &Evaluator{mode: "onprem"}
	// 较新的服务器 → 22.11.1.0 的覆盖胜出。
	results := ev.RunAll(dir, internal.Version{Major: 25, Minor: 4, Patch: 1, Build: 0})
	if len(results) != 1 {
		t.Fatalf("期望 1 个结果（覆盖遮蔽根规则），实际为 %d 个", len(results))
	}
	if results[0].File != "22.11.1.0/r.yaml" {
		t.Errorf("File = %q，期望 %q", results[0].File, "22.11.1.0/r.yaml")
	}
	// 较旧的服务器 → 根规则，无前缀。
	results = ev.RunAll(dir, internal.Version{Major: 22, Minor: 8, Patch: 1, Build: 0})
	if len(results) != 1 || results[0].File != "r.yaml" {
		t.Errorf("根规则运行：得到 %+v，期望单个 File=r.yaml 的结果", results)
	}
}

// TestNotApplicable_ModeAware 固定住规则中单节点的部分：本地 UNKNOWN_TABLE
// 是定论（“不适用”），而缺列则永远不是。在 cloud 中答案并非由模式推导 ——
// 它来自统计拥有该表的 replica 数；这些分支见
// TestNotApplicable_CloudClusterProbe。
func TestNotApplicable_ModeAware(t *testing.T) {
	missing := errString("Code: 60. DB::Exception: Table system.crash_log doesn't exist. (UNKNOWN_TABLE) (version 24.8.1.1)")
	badColumn := errString("Code: 47. DB::Exception: Missing columns: 'is_killed' (UNKNOWN_IDENTIFIER)")

	for _, mode := range []string{"onprem", "gov"} {
		ev := &Evaluator{mode: mode}
		if !ev.notApplicable(missing) {
			t.Errorf("%s：本地缺失的表应判为不适用", mode)
		}
		if ev.notApplicable(badColumn) {
			t.Errorf("%s：缺列必须保持为真正的错误", mode)
		}
	}

	// Cloud 通过统计拥有该表的 replica 来消解歧义
	//（对 system.tables 使用 clusterAllReplicas）。没有 client 时探测
	// 无法运行，因此它必须拒绝声称“不适用”，并让错误暴露出来 ——
	// 绝不能悄悄隐藏一个可能的真实 finding。
	cloud := &Evaluator{mode: "cloud"}
	if cloud.notApplicable(missing) {
		t.Error("cloud：没有可验证的集群范围缺失检查时，UNKNOWN_TABLE 绝不能" +
			"被当作不适用 —— 它可能意味着该表存在于另一个 replica 上（真实的崩溃）")
	}
	if cloud.notApplicable(badColumn) {
		t.Error("cloud：缺列必须保持为真正的错误")
	}
}

// TestNotApplicable_CloudClusterProbe 覆盖整个 cloud 设计所依赖的分支 ——
// 也是决定一个危急崩溃告警是被隐藏还是被暴露的分支。使用 queryFn 接缝，
// 因此无需实时集群。每一种不确定的结果都必须失败向暴露。
func TestNotApplicable_CloudClusterProbe(t *testing.T) {
	missing := errString("Code: 60. DB::Exception: Table system.crash_log does not exist. (UNKNOWN_TABLE)")
	// count() 响应根据查询是否针对特定表来区分。
	resp := func(n string) string {
		return `{"meta":[{"name":"c"}],"data":[["` + n + `"]],"rows":1}`
	}

	cases := []struct {
		name string
		fn   func(string) (string, error)
		want bool // true ⇒ 视为不适用（skipped）
	}{
		{
			name: "整个集群都不存在 → 跳过",
			fn: func(sql string) (string, error) {
				if strings.Contains(sql, "name='crash_log'") {
					return resp("0"), nil // 没有 replica 拥有它
				}
				return resp("120"), nil // 但该数据库可见
			},
			want: true,
		},
		{
			name: "存在于部分 replica → 暴露（真实的崩溃不得被隐藏）",
			fn: func(sql string) (string, error) {
				if strings.Contains(sql, "name='crash_log'") {
					return resp("1"), nil // 一个 replica 拥有它
				}
				return resp("120"), nil
			},
			want: false,
		},
		{
			name: "权限盲区（数据库本身不可见）→ 暴露，绝不声称不存在",
			fn: func(sql string) (string, error) {
				return resp("0"), nil // 连数据库检查都返回 0
			},
			want: false,
		},
		{
			name: "探测查询失败 → 暴露",
			fn: func(string) (string, error) {
				return "", errString("connection reset")
			},
			want: false,
		},
		{
			name: "探测响应无法解析 → 暴露",
			fn: func(string) (string, error) {
				return "not json", nil
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := &Evaluator{mode: "cloud", queryFn: c.fn}
			if got := ev.notApplicable(missing); got != c.want {
				t.Errorf("notApplicable = %v，期望 %v", got, c.want)
			}
		})
	}
}

// TestNotApplicable_CloudKeepsDatabase 断言探测检查的是错误中给出的数据库，
// 而不是硬编码的 "system" —— 否则针对 mydb.foo 的规则会被拿到
// system.foo 上校验并被错误地跳过。
func TestNotApplicable_CloudKeepsDatabase(t *testing.T) {
	var seen []string
	ev := &Evaluator{mode: "cloud", queryFn: func(sql string) (string, error) {
		seen = append(seen, sql)
		return `{"meta":[{"name":"c"}],"data":[["5"]],"rows":1}`, nil
	}}
	ev.notApplicable(errString("Table mydb.foo does not exist. (UNKNOWN_TABLE)"))
	joined := strings.Join(seen, " | ")
	if !strings.Contains(joined, "database='mydb'") {
		t.Errorf("探测应针对指定的数据库；实际查询为：%s", joined)
	}
	if strings.Contains(joined, "database='system' AND name='foo'") {
		t.Errorf("对于带限定的名称，探测不得回退到 system：%s", joined)
	}
}

func TestMissingTableNameExtraction(t *testing.T) {
	// 两种措辞都必须能解析：22.8 输出 "doesn't exist"，26.2 输出
	// "does not exist"。只匹配旧形式会让 cloud 的集群范围探测在较新的服务器上
	// 悄悄无法到达（在 Cloud 26.2.1.525 上实际观察到）。
	cases := map[string]string{
		"Code: 60. DB::Exception: Table system.crash_log doesn't exist. (UNKNOWN_TABLE) (version 22.8.21.38)":    "crash_log",
		"Code: 60. DB::Exception: Table system.no_such_log does not exist. (UNKNOWN_TABLE) (version 26.2.1.525)": "no_such_log",
		"Table system.text_log doesn't exist. (UNKNOWN_TABLE)":                                                   "text_log",
		"Code: 47. Missing columns: 'x' (UNKNOWN_IDENTIFIER)":                                                    "",
	}
	for errText, want := range cases {
		m := reMissingTable.FindStringSubmatch(errText)
		got := ""
		if m != nil {
			got = m[1]
			if i := strings.LastIndex(got, "."); i >= 0 {
				got = got[i+1:]
			}
		}
		if got != want {
			t.Errorf("从 %q：得到 %q，期望 %q", errText, got, want)
		}
	}
}

func TestIsMissingTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unknown table (crash_log)", errString("non-OK status: 404, body: Code: 60. DB::Exception: Table system.crash_log doesn't exist. (UNKNOWN_TABLE) (version 22.8.21.38 (official build))"), true},
		{"unknown identifier (missing column) must NOT match", errString("Code: 47. DB::Exception: Missing columns: 'is_killed' … (UNKNOWN_IDENTIFIER) (version 23.8...)"), false},
		{"transport error", errString("dial tcp: connection refused"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMissingTable(c.err); got != c.want {
				t.Errorf("isMissingTable(%v) = %v，期望 %v", c.err, got, c.want)
			}
		})
	}
}

// errString 是一个简单的 error，其消息就是给定字符串。
type errString string

func (e errString) Error() string { return string(e) }

func TestAlertYAML_FilesAreValid(t *testing.T) {
	alertsDir := "../../alerts"
	if _, err := os.Stat(alertsDir); os.IsNotExist(err) {
		t.Skip("alerts/ 目录不存在")
	}

	// 校验根目录以及下一层（版本覆盖目录）中的每条规则，
	// 使覆盖规则获得与根规则相同的必填字段和安全检查 ——
	// 而不仅仅是根目录中的那些。
	type yfile struct{ display, path string }
	var files []yfile
	entries, err := os.ReadDir(alertsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			sub, err := os.ReadDir(filepath.Join(alertsDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range sub {
				if !s.IsDir() && strings.HasSuffix(strings.ToLower(s.Name()), ".yaml") {
					files = append(files, yfile{e.Name() + "/" + s.Name(), filepath.Join(alertsDir, e.Name(), s.Name())})
				}
			}
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") {
			files = append(files, yfile{e.Name(), filepath.Join(alertsDir, e.Name())})
		}
	}

	ev := &Evaluator{mode: "onprem"}
	for _, f := range files {
		t.Run(f.display, func(t *testing.T) {
			raw, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			var def Definition
			if err := yaml.Unmarshal(raw, &def); err != nil {
				t.Fatalf("YAML 解析错误：%v", err)
			}
			if def.Name == "" {
				t.Error("告警没有 name")
			}
			if def.Title == "" {
				t.Error("告警没有 title")
			}
			if def.Query == "" {
				t.Error("告警没有 query")
			}
			// severity 必须是三个已知值之一（可以为空 ——
			// evalFile 会默认设为 warning）。其他任何值都会直接
			// 传到 dashboard，而那里的严重级别标签和导航徽章只统计
			// critical/warning/info：一条 `severity: high` 且真的触发的规则
			// 会在两处汇总中都不被计数、不可见。与其假定“我们掌控严重级别”，
			// 不如强制执行这一假定，代价更低。
			switch def.Severity {
			case "", SeverityCritical, SeverityWarning, SeverityInfo:
			default:
				t.Errorf("severity %q 不是 %q/%q/%q 之一 —— 使用未知严重级别"+
					"的触发规则既不会被徽章计数，也不会被任何标签计数",
					def.Severity, SeverityCritical, SeverityWarning, SeverityInfo)
			}
			// 安全性：展开后的查询必须通过校验
			expanded := ev.expandQuery(def.Query)
			if err := query.ValidateQueryContent(expanded); err != nil {
				t.Errorf("安全校验失败：%v", err)
			}
			// 跟进查询会展示给客户，供其在自建集群上运行：
			// 每条都需要 title，并且必须在每种模式的形式下都是只读 SELECT。
			for i, inv := range def.Investigate {
				if strings.TrimSpace(inv.Title) == "" || strings.TrimSpace(inv.Query) == "" {
					t.Errorf("investigate[%d] 需要 title 和 query", i)
				}
				for _, mode := range []string{"onprem", "cloud", "gov"} {
					if err := query.ValidateQueryContent((&Evaluator{mode: mode}).expandQuery(inv.Query)); err != nil {
						t.Errorf("investigate[%d]（%s）：%v", i, mode, err)
					}
				}
			}
		})
	}
}

// TestAlertYAML_OverridesMatchRoot 防止根规则/覆盖规则漂移：
// 版本目录中的规则（例如 alerts/24.1.1.0/foo.yaml）与其根对应文件几乎相同，
// 只在查询上有差异，因此未来的编辑很容易只落在其中一个上。
// 断言每个覆盖都有一个身份元数据（name/title/severity/tags）相同的根对应文件，
// 并且其 name 与文件名一致（重命名会悄悄创建出第二条规则，而不是一个覆盖 ——
// 见 README 说明）。
func TestAlertYAML_OverridesMatchRoot(t *testing.T) {
	alertsDir := "../../alerts"
	if _, err := os.Stat(alertsDir); os.IsNotExist(err) {
		t.Skip("alerts/ 目录不存在")
	}
	load := func(path string) (Definition, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return Definition{}, err
		}
		var d Definition
		return d, yaml.Unmarshal(raw, &d)
	}
	entries, err := os.ReadDir(alertsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		verDir := filepath.Join(alertsDir, e.Name())
		files, err := os.ReadDir(verDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(strings.ToLower(f.Name()), ".yaml") {
				continue
			}
			name := f.Name()
			t.Run(e.Name()+"/"+name, func(t *testing.T) {
				ov, err := load(filepath.Join(verDir, name))
				if err != nil {
					t.Fatal(err)
				}
				// 关键检查：覆盖规则的 name 必须与
				// 其文件名一致。不一致意味着一次重命名悄悄把覆盖
				// 变成了独立规则。适用于每个覆盖。
				if base := strings.TrimSuffix(name, ".yaml"); ov.Name != base {
					t.Errorf("name %q 与文件名 %q 不匹配", ov.Name, base)
				}
				// 仅存在于版本目录中的文件（没有
				// 根对应文件）是受支持的模式 —— 在较旧的服务器上会被跳过，
				// 见 README。没有可对比的元数据。
				rootPath := filepath.Join(alertsDir, name)
				if _, err := os.Stat(rootPath); err != nil {
					return
				}
				rt, err := load(rootPath)
				if err != nil {
					t.Fatal(err)
				}
				if ov.Name != rt.Name {
					t.Errorf("name 漂移：覆盖 %q 与根 %q", ov.Name, rt.Name)
				}
				if ov.Title != rt.Title {
					t.Errorf("title 漂移：覆盖 %q 与根 %q", ov.Title, rt.Title)
				}
				if ov.Severity != rt.Severity {
					t.Errorf("severity 漂移：覆盖 %q 与根 %q", ov.Severity, rt.Severity)
				}
				if !reflect.DeepEqual(ov.Tags, rt.Tags) {
					t.Errorf("tags 漂移：覆盖 %v 与根 %v", ov.Tags, rt.Tags)
				}
				// 跟进查询也一样：覆盖规则才是较新
				// 服务器上运行的内容，因此缺少它们恰恰会在最需要的地方
				// 隐藏这些查询。不支持按版本不同的跟进查询；
				// 请让它们保持版本中立。
				if !reflect.DeepEqual(ov.Investigate, rt.Investigate) {
					t.Errorf("investigate 漂移：覆盖有 %d 条跟进查询，根有 %d 条 —— 请复制根的 investigate: 列表", len(ov.Investigate), len(rt.Investigate))
				}
			})
		}
	}
}

// ── modes 门控 ───────────────────────────────────────────────────────────────

// 只有在集群扇出时才有意义的规则（per-replica parts 比较）会声明
// `modes: [cloud]`；在其他模式下它会被跳过 —— 从不运行、从不出错 ——
// 这样 onprem 的 bundle 不会为一个它并不拥有的集群显示失败的规则。
func TestDefinition_AppliesTo(t *testing.T) {
	every := Definition{}
	cloudOnly := Definition{Modes: []string{"cloud"}}
	single := Definition{Modes: []string{"onprem", " Gov "}}
	for _, c := range []struct {
		d    Definition
		mode string
		want bool
	}{
		{every, "onprem", true}, {every, "cloud", true}, {every, "gov", true},
		{cloudOnly, "cloud", true}, {cloudOnly, "onprem", false}, {cloudOnly, "gov", false},
		{single, "onprem", true}, {single, "gov", true}, {single, "cloud", false},
	} {
		if got := c.d.appliesTo(c.mode); got != c.want {
			t.Errorf("modes %v 在 %q 中：得到 %v，期望 %v", c.d.Modes, c.mode, got, c.want)
		}
	}
}

// 对于被门控到其他模式的规则，evalFile 必须在接触服务器之前返回
// Skipped 结果：这里的评估器没有 client，任何尝试运行查询的动作都会 panic。
func TestEvalFile_ModeGateSkipsWithoutQuerying(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud_only.yaml")
	if err := os.WriteFile(path, []byte("name: cloud_only\ntitle: t\nmodes: [cloud]\nquery: SELECT 1\nmessage: m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := &Evaluator{mode: "onprem"}
	r := ev.evalFile(path)
	if !r.Skipped || r.Error != "" || len(r.Rows) != 0 {
		t.Fatalf("被门控的规则：skipped=%v error=%q rows=%d", r.Skipped, r.Error, len(r.Rows))
	}
	if !strings.Contains(r.Reason, "onprem") || !strings.Contains(r.Reason, "cloud") {
		t.Errorf("原因应指出运行模式和规则的 modes：%q", r.Reason)
	}
}

// modes: 中的拼写错误必须是规则上的错误，而不是在每种模式下都静默跳过。
// 该评估器没有 client，因此这个测试也证明了检查在任何查询之前运行。
func TestEvalFile_UnknownModeIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "typo.yaml")
	if err := os.WriteFile(path, []byte("name: typo\ntitle: t\nmodes: [on-prem]\nquery: SELECT 1\nmessage: m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := &Evaluator{mode: "onprem"}
	r := ev.evalFile(path)
	if r.Skipped || r.Error == "" || !strings.Contains(r.Error, "on-prem") {
		t.Fatalf("未知模式：skipped=%v error=%q —— 期望一个指出错误取值的错误", r.Skipped, r.Error)
	}
	for _, ok := range []Definition{{}, {Modes: []string{"cloud"}}, {Modes: []string{" Gov ", "ONPREM"}}} {
		if err := ok.validateModes(); err != nil {
			t.Errorf("modes %v 应能通过校验：%v", ok.Modes, err)
		}
	}
}

// shippedRuleModes 把必须携带 modes: 列表的规则映射到它们必须携带的确切列表。
// 由添加门控规则的 PR 扩展；一次丢掉该键的复制粘贴会让仅适用于集群的规则
// 在每次 onprem 运行时以 CLUSTER_DOESNT_EXIST 失败。
var shippedRuleModes = map[string][]string{
	"parts_propagation_lag": {"cloud"},
}

// 每条带 modes: 列表的随包规则都指定真实模式，且 shippedRuleModes 中的
// 每条规则都存在并携带完全相同的列表。
func TestShippedRules_ModesAreGated(t *testing.T) {
	files, err := filepath.Glob("../../alerts/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("没有告警文件：%v", err)
	}
	sub, _ := filepath.Glob("../../alerts/*/*.yaml")
	files = append(files, sub...)
	seen := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var d Definition
		if err := yaml.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, m := range d.Modes {
			if m != "cloud" && m != "onprem" && m != "gov" {
				t.Errorf("%s：未知模式 %q", f, m)
			}
		}
		if w, ok := shippedRuleModes[d.Name]; ok {
			seen[d.Name] = true
			if strings.Join(d.Modes, ",") != strings.Join(w, ",") {
				t.Errorf("%s：modes %v，期望 %v", f, d.Modes, w)
			}
		}
	}
	for n := range shippedRuleModes {
		if !seen[n] {
			t.Errorf("在 alerts/ 下未找到规则 %s", n)
		}
	}
}

// 展示的告警（已触发，或其查询失败）会携带客户可在自建集群上运行的 SQL：
// 规则实际运行的查询原文、按模式展开的占位符，以及规则的跟进查询。
// 干净的规则不携带任何内容，因此 dashboard 不会因没有发现任何东西的规则而膨胀。
const evidenceRule = `
name: pool_rule
title: "Pool saturated"
severity: warning
query: |
  SELECT hostName() AS hostname, avg(CurrentMetric_BackgroundFetchesPoolTask) AS avg_tasks
  FROM {sys.metric_log} GROUP BY hostname
investigate:
  - title: "Every background pool now, per replica"
    query: |
      SELECT hostName() AS hostname, metric, value FROM {sys.metrics} WHERE metric LIKE 'Background%PoolTask'
message: "{hostname}: {avg_tasks}"
`

func writeRule(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rule.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const oneRow = `{"meta":[{"name":"hostname"},{"name":"avg_tasks"}],"data":[["node-1","16"]],"rows":1}`
const noRows = `{"meta":[{"name":"hostname"},{"name":"avg_tasks"}],"data":[],"rows":0}`

func TestEvalFile_FiredResultCarriesTheQueriesToRun(t *testing.T) {
	var ran string
	ev := &Evaluator{mode: "cloud", queryFn: func(sql string) (string, error) { ran = sql; return oneRow, nil }}
	r := ev.evalFile(writeRule(t, evidenceRule))
	if len(r.Rows) != 1 {
		t.Fatalf("规则没有触发：%+v", r)
	}
	if r.Query == "" || r.Query != ran {
		t.Errorf("Query = %q；期望与实跑内容完全一致（%q）", r.Query, ran)
	}
	if !strings.Contains(r.Query, "clusterAllReplicas") || strings.Contains(r.Query, "{sys.") {
		t.Errorf("Query 必须按模式展开：%q", r.Query)
	}
	if len(r.Investigate) != 1 || r.Investigate[0].Title != "Every background pool now, per replica" ||
		!strings.Contains(r.Investigate[0].Query, "clusterAllReplicas") || strings.Contains(r.Investigate[0].Query, "{sys.") {
		t.Errorf("Investigate = %+v；期望展开后的跟进查询", r.Investigate)
	}
}

func TestEvalFile_CleanResultCarriesNoQuery(t *testing.T) {
	ev := &Evaluator{mode: "onprem", queryFn: func(string) (string, error) { return noRows, nil }}
	r := ev.evalFile(writeRule(t, evidenceRule))
	if r.Query != "" || len(r.Investigate) != 0 {
		t.Errorf("干净的规则不得把 SQL 带进 dashboard：%+v", r)
	}
}

func TestEvalFile_FailedQueryIsShown(t *testing.T) {
	ev := &Evaluator{mode: "onprem", queryFn: func(string) (string, error) {
		return "", errString("Code: 47. DB::Exception: Missing columns: 'x' (UNKNOWN_IDENTIFIER)")
	}}
	r := ev.evalFile(writeRule(t, evidenceRule))
	if r.Error == "" || !strings.Contains(r.Query, "system.metric_log") {
		t.Errorf("查询失败的规则会被展示，其查询正是值得一看的内容：%+v", r)
	}
}

func TestEvalFile_FollowUpQueriesMustBeReadOnly(t *testing.T) {
	bad := strings.Replace(evidenceRule, "SELECT hostName() AS hostname, metric, value FROM {sys.metrics} WHERE metric LIKE 'Background%PoolTask'",
		"ALTER TABLE system.metrics DELETE WHERE 1", 1)
	ran := false
	ev := &Evaluator{mode: "onprem", queryFn: func(string) (string, error) { ran = true; return oneRow, nil }}
	r := ev.evalFile(writeRule(t, bad))
	if !strings.Contains(r.Error, "security") || r.Query != "" || len(r.Investigate) != 0 {
		t.Errorf("非 SELECT 的跟进查询必须使规则失效且不展示任何内容：%+v", r)
	}
	if ran {
		t.Error("规则定义不安全，却仍然运行了")
	}
}

func TestWriteSummaryJSON_CarriesNoSQL(t *testing.T) {
	dir := t.TempDir()
	res := []Result{{Name: "pool_rule", Title: "Pool", Severity: "warning", Rows: []map[string]interface{}{{"a": 1}},
		Query: "SELECT 1 FROM system.metric_log", Investigate: []Investigation{{Title: "t", Query: "SELECT 2"}}}}
	if err := WriteSummaryJSON(dir, res, "gov"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "alerts_summary.json"))
	if strings.Contains(string(b), "SELECT") {
		t.Errorf("alerts_summary.json 只应包含计数：\n%s", b)
	}
}

// 在 23.3+ 上，RunAll 会选择池规则的 alerts/23.3.1.0/ 覆盖 ——
// 涵盖所有当前服务器，包括 26.7。跟进查询必须在这一选择中保留下来，
// 否则“是哪个池？”的查询永远不会出现在它们该出现的地方。
func TestRunAll_VersionSelectedRulesKeepTheirFollowUps(t *testing.T) {
	var hours []row
	for i := 1; i <= 3; i++ {
		hours = append(hours, row{"time": hoursBack(i), "avg_fetch_pool_tasks": "16", "avg_schedule_pool_tasks": "512"})
	}
	b := fixture(t, map[string][]row{
		"system.metric_log_7_days": hours,
		"system.server_settings":   {{"name": "background_fetches_pool_size", "value": "16"}, {"name": "background_schedule_pool_size", "value": "512"}},
		"system.replication_queue": {{"database": "db", "table": "t", "last_exception": "Code: 999"}},
	})
	ev := (&Evaluator{mode: "onprem", queryFn: func(string) (string, error) { return oneRow, nil }}).WithBundle(b)
	results := ev.RunAll("../../alerts", internal.Version{Major: 26, Minor: 7, Patch: 1, Build: 0})
	want := map[string]bool{"fetch_pool_saturated": true, "schedule_pool_saturated": true, "replication_queue_errors": true}
	for _, r := range results {
		if !want[r.Name] {
			continue
		}
		delete(want, r.Name)
		if len(r.Investigate) == 0 {
			t.Errorf("%s（%s）在 26.7 上触发了，却没有跟进查询", r.Name, r.File)
		}
	}
	for n := range want {
		t.Errorf("%s 在此测试中没有触发", n)
	}
}

// essential 采集不会运行扫描逐表数据的规则（tier: full）；
// 它们被报告为未运行，绝不是干净，而 essential 规则仍然运行。
func TestEvalFile_EssentialSkipsFullTierRules(t *testing.T) {
	heavy := "name: heavy\ntitle: Heavy\nseverity: warning\ntier: full\nquery: |\n  SELECT 1 AS x FROM {sys.parts}\nmessage: \"x\"\n"
	light := "name: light\ntitle: Light\nseverity: warning\nquery: |\n  SELECT 1 AS x FROM {sys.metrics}\nmessage: \"x\"\n"
	ran := 0
	ev := (&Evaluator{mode: "onprem", queryFn: func(string) (string, error) { ran++; return noRows, nil }}).WithEssential(true)
	r := ev.evalFile(writeRule(t, heavy))
	if !r.Skipped || !strings.Contains(r.Reason, "essential") || ran != 0 {
		t.Fatalf("essential 运行中的 full tier 规则：%+v（已发送查询：%d）", r, ran)
	}
	r = ev.evalFile(writeRule(t, light))
	if r.Skipped || r.Error != "" {
		t.Fatalf("essential 规则没有运行：%+v", r)
	}
	if r := (&Evaluator{mode: "onprem", queryFn: func(string) (string, error) { return noRows, nil }}).evalFile(writeRule(t, heavy)); r.Skipped {
		t.Fatalf("full 运行中 full tier 规则被跳过了：%+v", r)
	}
	bad := "name: bad\ntitle: Bad\nseverity: warning\ntier: fast\nquery: |\n  SELECT 1\nmessage: \"x\"\n"
	if r := ev.evalFile(writeRule(t, bad)); r.Error == "" {
		t.Fatal("未知的 tier 被接受了")
	}
}

// 扫描逐表数据的规则是 full tier，并且版本覆盖会保留其根规则的 tier
// （否则 essential 集合会随服务器版本变化）。
func TestAlertYAML_Tiers(t *testing.T) {
	want := map[string]string{"large_parts": "full", "detached_parts_exist": "full", "parts_propagation_lag": "full", "replica_readonly": ""}
	err := filepath.WalkDir("../../alerts", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var def Definition
		if err := yaml.Unmarshal(raw, &def); err != nil {
			return err
		}
		if w, ok := want[def.Name]; ok && def.Tier != w {
			t.Errorf("%s：tier %q，期望 %q", p, def.Tier, w)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEvalFile_KeeperDownAndBudgetSkip(t *testing.T) {
	zk := "name: zk\ntitle: Zk\nseverity: warning\nneeds: keeper\nquery: |\n  SELECT 1 AS x FROM {sys.zookeeper_connection}\nmessage: \"x\"\n"
	light := "name: light\ntitle: Light\nseverity: warning\nquery: |\n  SELECT 1 AS x FROM {sys.metrics}\nmessage: \"x\"\n"
	ran := 0
	fn := func(string) (string, error) { ran++; return noRows, nil }
	ev := (&Evaluator{mode: "onprem", queryFn: fn}).WithKeeperDown("no Keeper session")
	if r := ev.evalFile(writeRule(t, zk)); !r.Skipped || !strings.Contains(r.Reason, "Keeper 会话") || ran != 0 {
		t.Fatalf("Keeper 宕机时的 needs-keeper 规则：%+v", r)
	}
	if r := ev.evalFile(writeRule(t, light)); r.Skipped {
		t.Fatalf("其他规则被跳过了：%+v", r)
	}
	spent := (&Evaluator{mode: "onprem", queryFn: fn}).WithStop(func() bool { return true })
	if r := spent.evalFile(writeRule(t, light)); !r.Skipped || !strings.Contains(r.Reason, "时间预算") {
		t.Fatalf("预算已用尽：%+v", r)
	}
}
