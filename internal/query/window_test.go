package query

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWindowDefaultSQL(t *testing.T) {
	cases := []struct {
		spec string
		want string
		ok   bool
	}{
		{"now", "now()", true},
		{"1d", "now() - INTERVAL 1 DAY", true},
		{"15d", "now() - INTERVAL 15 DAY", true},
		{"12h", "now() - INTERVAL 12 HOUR", true},
		{"30m", "now() - INTERVAL 30 MINUTE", true},
		// 被拒绝：这些必须失败关闭，而不是放宽窗口。
		{"7dd", "", false},
		{"d", "", false},
		{"7", "", false},
		{"0d", "", false},
		{"-1d", "", false},
		{"7y", "", false},
		{"", "", false},
		{"NOW", "", false},
	}
	for _, c := range cases {
		got, ok := windowDefaultSQL(c.spec)
		if ok != c.ok {
			t.Errorf("windowDefaultSQL(%q) ok = %v，期望 %v", c.spec, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("windowDefaultSQL(%q) = %q，期望 %q", c.spec, got, c.want)
		}
	}
}

// TestWindowDefaultExpansion 针对随包查询实际使用的形态，
// 检查模板 -> SQL 的映射。
func TestWindowDefaultExpansion(t *testing.T) {
	cases := []struct {
		templated string
		original  string
	}{
		{
			"WHERE (event_time > {from:7d} AND event_time <= {to:now})",
			"WHERE (event_time > now() - INTERVAL 7 DAY AND event_time <= now())",
		},
		{
			"WHERE event_time > {from:7d} AND event_time <= {to:now}",
			"WHERE event_time > now() - INTERVAL 7 DAY AND event_time <= now()",
		},
		{
			"WHERE event_date >= toDate({from:1d}) AND event_date <= toDate({to:now})",
			"WHERE event_date >= toDate(now() - INTERVAL 1 DAY) AND event_date <= toDate(now())",
		},
	}
	for _, c := range cases {
		got := Apply(c.templated, Vars{}) // 无 flag -> 默认值
		if got != c.original {
			t.Errorf("默认展开改变了窗口：\n got: %s\nwant: %s", got, c.original)
		}
	}
}

func TestWindowFlagsOverrideDefaults(t *testing.T) {
	from := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	to := time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC)

	got := Apply("WHERE event_time > {from:15d} AND event_time <= {to:now}", Vars{From: from, To: to})
	want := "WHERE event_time > toDateTime('2026-03-01 10:30:00', 'UTC') AND event_time <= toDateTime('2026-03-02 11:00:00', 'UTC')"
	if got != want {
		t.Errorf("两个 flag 都给出：\n got: %s\nwant: %s", got, want)
	}

	// 只给出一端：另一端仍必须回退到它的默认值，
	// 而不是零时间戳（那会选不到任何数据）。
	got = Apply("WHERE event_time > {from:7d} AND event_time <= {to:now}", Vars{From: from})
	want = "WHERE event_time > toDateTime('2026-03-01 10:30:00', 'UTC') AND event_time <= now()"
	if got != want {
		t.Errorf("只给 --from：\n got: %s\nwant: %s", got, want)
	}

	got = Apply("WHERE event_time > {from:7d} AND event_time <= {to:now}", Vars{To: to})
	want = "WHERE event_time > now() - INTERVAL 7 DAY AND event_time <= toDateTime('2026-03-02 11:00:00', 'UTC')"
	if got != want {
		t.Errorf("只给 --to：\n got: %s\nwant: %s", got, want)
	}
}

// TestFlagTimestampsCarryExplicitUTC 固定住 PR 评审中的阻塞性发现：
// ClickHouse 会按列所在时区（服务器的时区）解析裸 datetime 字面量，
// 因此在 <timezone>America/New_York</timezone> 服务器上，未加注的
// '2026-03-01 10:30:00' 会选出一个与运维人员想要的 UTC 时刻相差五小时的
// 窗口 —— 而且悄无声息。因此每个由 flag 提供的时间戳都必须以
// toDateTime('…', 'UTC') 包裹后到达 SQL。默认值（now() 运算）基于纪元，
// 无需加注。
func TestFlagTimestampsCarryExplicitUTC(t *testing.T) {
	ts := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	bare := regexp.MustCompile(`[^(]'\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}'`)

	for name, sql := range map[string]string{
		"window": Apply("t > {from:7d} AND t <= {to:now}", Vars{From: ts, To: ts}),
		"bare":   Apply("t >= {from} AND t <= {to}", Vars{From: ts, To: ts}),
	} {
		if bare.MatchString(sql) {
			t.Errorf("%s: flag 时间戳被输出为裸字面量（按服务器时区"+
				"解析）：%s", name, sql)
		}
		if strings.Count(sql, "toDateTime('2026-03-01 10:30:00', 'UTC')") != 2 {
			t.Errorf("%s: 期望两端都是显式 UTC 的 toDateTime：\n%s", name, sql)
		}
	}
}

// TestMalformedWindowIsRefused：拼写错误绝不能悄悄变成无界扫描。
// 它会留在 SQL 中并被报告为未绑定，从而让 executor 拒绝该查询。
func TestMalformedWindowIsRefused(t *testing.T) {
	sql := Apply("WHERE event_time > {from:7dd}", Vars{})
	if !strings.Contains(sql, "{from:7dd}") {
		t.Fatalf("格式错误的规格被悄悄替换了：%s", sql)
	}
	unbound := UnboundPlaceholders(sql)
	if len(unbound) == 0 {
		t.Fatal("格式错误的窗口占位符未被报告为未绑定；" +
			"executor 会把字面花括号发送到服务器")
	}
}

// TestLogFormatBracesUntouched 守护 rePlaceholder 之所以狭窄的原因：
// 某些查询会匹配包含 {} 花括号的 ClickHouse 日志消息。
func TestLogFormatBracesUntouched(t *testing.T) {
	sql := "SELECT 1 WHERE message LIKE 'Selected {}/{} parts by partition key'"
	if got := Apply(sql, Vars{}); got != sql {
		t.Errorf("日志格式花括号被改写了：\n got: %s\nwant: %s", got, sql)
	}
	if u := UnboundPlaceholders(sql); len(u) != 0 {
		t.Errorf("日志格式花括号被报告为未绑定：%v", u)
	}
}

// TestShippedQueriesHaveNoHardcodedWindow 针对真实仓库固定住这项改动：
// 一个硬编码自身区间的新查询会悄悄忽略 --from/--to。
func TestShippedQueriesHaveNoHardcodedWindow(t *testing.T) {
	// toIntervalHour(...) 是 GROUP BY 中的分桶粒度，不是窗口过滤，
	// 因此允许使用。
	bad := regexp.MustCompile(`(?i)INTERVAL\s+\d+\s+(DAY|HOUR|MINUTE)|toIntervalDay\(\s*\d|today\(\)\s*-\s*\d`)

	var files []string
	for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.EqualFold(filepath.Ext(p), ".sql") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("查询目录不存在")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s：%v", f, err)
		}
		if m := bad.FindString(string(b)); m != "" {
			t.Errorf("%s 硬编码了时间窗口（%q）；请使用 {from:<default>} / "+
				"{to:now}，以便 --from/--to 可以覆盖它", f, m)
		}
	}
}

// TestShippedQueryWindowsAllExpand 确保仓库中实际存在的每个默认规格
// 都是 windowDefaultSQL 接受的 —— 否则拼写错误只会在采集时、对着
// 一台实时服务器才暴露。
func TestShippedQueryWindowsAllExpand(t *testing.T) {
	var files []string
	for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.EqualFold(filepath.Ext(p), ".sql") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("查询目录不存在")
	}
	seen := 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, m := range reWindowPlaceholder.FindAllStringSubmatch(string(b), -1) {
			seen++
			if _, ok := windowDefaultSQL(m[2]); !ok {
				t.Errorf("%s: %q 的默认规格 %q 不可用", f, m[0], m[2])
			}
		}
		if u := UnboundPlaceholders(Apply(string(b), Vars{})); len(u) > 0 {
			// '%salt%' 在 gov 文件中是预期的；具名花括号则不是。
			t.Errorf("%s: 默认展开后仍未绑定：%v", f, u)
		}
	}
	if seen == 0 {
		t.Error("在随包发布的查询中未找到窗口占位符；" +
			"这个守护会形同虚设地通过")
	}
	t.Logf("在 %d 个文件中检查了 %d 个窗口占位符", seen, len(files))
}

// TestPlaceholderInCommentIgnored：查询文件会在说明文字中解释 ClickHouse 宏
// （queries.gov/system.replication_queue.sql 提到了 {replica} 宏）。
// 把它当作未绑定占位符，会仅因一条注释就拒绝一个合法的查询。
func TestPlaceholderInCommentIgnored(t *testing.T) {
	sql := "SELECT replica_name\n-- conventionally the {replica} macro\nFROM system.replication_queue"
	if u := UnboundPlaceholders(sql); len(u) != 0 {
		t.Errorf("注释内的占位符被报告为未绑定：%v", u)
	}
	block := "SELECT 1 /* uses the {shard} macro */ FROM system.parts"
	if u := UnboundPlaceholders(block); len(u) != 0 {
		t.Errorf("块注释内的占位符被报告为未绑定：%v", u)
	}
	// 但注释之外真正的占位符仍必须被捕获。
	if u := UnboundPlaceholders("SELECT 1 WHERE id = {query_id}"); len(u) != 1 {
		t.Errorf("漏掉了真正的未绑定占位符：%v", u)
	}
}

// TestShippedQueryDefaultWindows 按名称固定住每个随包查询的默认回看窗口。
// 没有它，窗口会悄悄漂移：它们分布在三个模式目录中的 20 个文件里，
// 外加版本覆盖，没有别的东西会注意到某个窗口被改成了不同的值。
//
// 这个映射就是规格 —— 窗口变化时，请在改动 SQL 的同一个提交中
// 有意识地更新它。
func TestShippedQueryDefaultWindows(t *testing.T) {
	want := map[string]string{
		"system.query_log_details_7_days.sql":       "7d",
		"system.part_log_3_days.sql":                "3d",
		"system.query_views_log_3_days.sql":         "3d",
		"system.metric_log_7_days.sql":              "7d",
		"system.asynchronous_insert_log_7_days.sql": "7d",
		"system.text_log.sql":                       "1d",
		"system.metric_log_coordination_3_days.sql": "3d",
		"system.metric_log_by_host_3_days.sql":      "3d",
		"system.error_log_7_days.sql":               "7d",
		"system.blob_storage_log_7_days.sql":        "7d",
		"system.distributed_ddl_queue.sql":          "7d",
		"system.text_log_histogram_1_day.sql":       "1d",
		"system.text_log_keeper_1_day.sql":          "1d",
		"system.zookeeper_log_errors_1_day.sql":     "1d",
	}

	checked := map[string]int{}
	for _, dir := range []string{"../../queries.onprem", "../../queries.cloud", "../../queries.gov"} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.EqualFold(filepath.Ext(p), ".sql") {
				return err
			}
			spec, ok := want[filepath.Base(p)]
			if !ok {
				return nil // 查询没有窗口；由 TestShippedQueriesHaveNoHardcodedWindow 覆盖
			}
			b, readErr := os.ReadFile(p)
			if readErr != nil {
				t.Fatalf("读取 %s：%v", p, readErr)
			}
			froms := regexp.MustCompile(`\{from:([A-Za-z0-9]+)\}`).FindAllStringSubmatch(string(b), -1)
			if len(froms) == 0 {
				t.Errorf("%s: 期望 {from:%s} 窗口，但未找到", p, spec)
				return nil
			}
			for _, m := range froms {
				if m[1] != spec {
					t.Errorf("%s: 默认窗口是 {from:%s}，期望 {from:%s}", p, m[1], spec)
				}
			}
			checked[filepath.Base(p)]++
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s：%v", dir, err)
		}
	}

	if len(checked) == 0 {
		t.Skip("查询目录不存在")
	}
	for name := range want {
		if checked[name] == 0 {
			t.Errorf("%s 在任何模式目录中都没有找到；守护没有"+
				"覆盖它（被重命名或删除了？）", name)
		}
	}
	t.Logf("在 %d 个查询文件中固定了窗口", len(checked))
}

// TestSysPlaceholderWithoutModeIsRefused 固定住评审发现 #3：用空 mode 展开
// {sys.<table>} 会悄悄表现得像 onprem —— 写进 queries.cloud/ 的第一个
// {sys.query_log} 将只采集一个 replica 而不是扇出。没有 mode 时，
// 占位符必须经过 Apply 后仍然存在并被报告为未绑定，从而让 executor 拒绝。
func TestSysPlaceholderWithoutModeIsRefused(t *testing.T) {
	sql := Apply("SELECT 1 FROM {sys.query_log}", Vars{})
	if !strings.Contains(sql, "{sys.query_log}") {
		t.Fatalf("空 mode 展开悄悄产出了：%s", sql)
	}
	if u := UnboundPlaceholders(sql); len(u) == 0 {
		t.Fatal("未展开的 {sys.} 占位符未被报告为未绑定")
	}
	// 而给出 mode 时，它必须像以前一样展开。
	if got := Apply("SELECT 1 FROM {sys.query_log}", Vars{Mode: "cloud"}); !strings.Contains(got, "clusterAllReplicas") {
		t.Errorf("cloud 展开出错：%s", got)
	}
}
