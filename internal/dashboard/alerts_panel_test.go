package dashboard

import (
	"clickhouse-diagnostic/internal/alert"
	"clickhouse-diagnostic/internal/keeper"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 一条alert消息是规则模板替换单行值后的结果，而这些值通常带有完整的
// ClickHouse异常：在一个真实bundle中测得的一条消息就有1764个字符、
// 跨15行，其中前216个字符才是错误本身。replication_queue_errors最多
// 返回50条这样的行，keeper_health最多168条，因此完整渲染每条消息会把
// 面板变成数百行堆栈帧。以下这些边界让面板保持可读；重构时不得丢弃。
func TestTemplate_AlertMessagesCollapseVerboseParts(t *testing.T) {
	for _, want := range []string{
		"const ALERT_HEAD_CHARS=260;",                 // 单个实例的内联长度
		"const ALERT_ROWS_SHOWN=5;",                   // 其余实例折叠前显示的实例数
		"const ALERT_STACK_RE=",                       // 堆栈跟踪是截断点
		`Stack trace \(when copying this message`,     // ClickHouse自身的标记，按字面匹配
		"function alertMessageParts(msg){",            // head / full拆分
		"function alertDisclosure(parts){",            // <details>开关
		"function alertRowLine(a,row){",               // 一行实例
		`'<details class="alert-more">`,               // 原生disclosure，无需JS
		`'<pre class="alert-full">'+esc(parts.full)+`, // 完整文本保留并转义
		"个实例",        // 额外行开关
		"关于此规则的更多信息", // 描述的后半部分

		// 评审中发现的两个差一错误。上限是包含省略号的，
		// 因此切片必须少一个——否则无空格回退会输出261个字符。
		// 并且开关是把内联head与整个扁平消息比较，而不是与去掉版本的副本：
		// 用去掉版本的副本时，一条“只有版本后缀”的消息会被剥成空，
		// 回退恢复它，这种不一致会渲染出一个正文重复上一行的disclosure。
		"head.slice(0,ALERT_HEAD_CHARS-1)",
		"truncated:head!==flat,",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("alert面板丢失了%q", want)
		}
	}

	// head绝不能原样输出：每个打印客户文本的分支都要经过esc()。
	for _, banned := range []string{
		"+parts.head+", "+p.head+", "+ep.head+", "+msg+'</li>", "+a.error+'",
		// 上面两个陷阱修复前的形式。
		"head.slice(0,ALERT_HEAD_CHARS)+", "truncated:head!==flatTrimmed",
	} {
		if strings.Contains(htmlTemplate, banned) {
			t.Errorf("alert面板未转义地插值客户文本：%q", banned)
		}
	}

	// 始终展开的disclosure没有意义；开关默认必须关闭
	// （我们发出的元素上不带`open`属性）。
	if strings.Contains(htmlTemplate, `<details class="alert-more" open`) {
		t.Error("alert disclosure必须默认关闭")
	}
}

// 只有完整文本仍在页面中，折叠才有意义——支持人员需要堆栈跟踪，
// 只是默认不显示在屏幕上。
func TestBuildHTML_AlertStackTraceIsKeptButCollapsed(t *testing.T) {
	trace := "Code: 221. DB::Exception: No interserver IO endpoint named SharedMergeTreePartsUpdate:/clickhouse/tables/<uuid>/default/virtual_parts/node-07. " +
		"(NO_SUCH_INTERSERVER_IO_ENDPOINT) (version 26.2.1.390 (official build)), Stack trace (when copying this message, always include the lines below):\n\n" +
		"0. ./ci/tmp/build/./src/Common/Exception.cpp:141:1: DB::Exception::Exception(DB::Exception::MessageMasked&&, int, bool) @ 0x000000001373469f\n" +
		"1. DB::Exception::Exception(String&&, int, String, bool) @ 0x000000000cbeeb0e\n"
	data := map[string]interface{}{
		"generated_at": "2026-09-25 10:00:00 UTC", "mode": "onprem", "version": "26.2.1.390",
		"alerts": []map[string]interface{}{{
			"name": "replication_queue_errors", "title": "Replication queue entries have exceptions",
			"severity": "critical", "file": "replication_queue_errors.yaml",
			"message": "{database}.{table} (replica {replica_name}): {type} failed after {num_tries} tries — {last_exception}",
			"rows": []map[string]interface{}{
				{"database": "demo", "table": "events", "replica_name": "r-07", "type": "GET_PART", "num_tries": 128, "last_exception": trace},
			},
		}},
	}
	html := buildHTML(data)
	if !strings.Contains(html, "NO_SUCH_INTERSERVER_IO_ENDPOINT") {
		t.Error("完整异常仍必须被嵌入——支持人员在展开后读取堆栈跟踪")
	}
	if !strings.Contains(html, "Exception.cpp:141") {
		t.Error("堆栈帧必须保留到payload中")
	}
}

// 页眉标出系统表所描述的节点，并重复执行日志的采集说明；
// Replication部分带有后台池面板，池大小作为参考线。这些内容都来自一次
// fetch延迟升级事件，当时节点被误读，池大小是从metric_log手工读取的。
func TestTemplate_NodeHeaderAndPoolsPanel(t *testing.T) {
	for _, want := range []string{
		"DATA.node?'<br>节点：'+esc(DATA.node)",
		"' &middot; 采集时已运行 '",
		"DATA.collection?'<br>采集：'+esc(DATA.collection)",
		`id="chart-pool-fetch"`, `id="chart-pool-schedule"`, `id="chart-pool-merge"`,
		"DATA.pools_hourly", "DATA.pool_sizes",
		"label:'池大小'", // 虚线参考线
		"[['fetch','fetch_tasks',0.9],['schedule','schedule_tasks',0.95],['merge','merge_tasks',0.9]]",
		"const anchor=DATA.pools_now?T(DATA.pools_now):T(labels[labels.length-1]);", // 采集时间锚点
		"const cut=anchor-24*3600e3;", // 24个时钟小时，而非24个采样点
		".chart-card.alert-card{",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("模板丢失了%q", want)
		}
	}
	// 页眉中的客户主机名要经过esc()，绝不原样输出。
	for _, banned := range []string{"'+DATA.node+'", "'+DATA.collection+'"} {
		if strings.Contains(htmlTemplate, banned) {
			t.Errorf("页眉未转义地插值主机名：%q", banned)
		}
	}
}

func TestBuildHTML_HeaderCarriesNodeAndCollection(t *testing.T) {
	g := NewGenerator(nil, "cloud").WithCollection("ch-01 (ch-01.example.internal)", "4d 18h", "onprem → cloud: SharedMergeTree detected (cloud_mode = 1)")
	if g.node == "" || g.nodeUptime != "4d 18h" || g.collection == "" {
		t.Fatal("WithCollection丢弃了一个值")
	}
	// collect()需要在线服务器；这里固定它发出的payload键，
	// 值与它会从generator复制的值相同。
	html := buildHTML(map[string]interface{}{
		"generated_at": "2026-09-30 10:00:00 UTC", "mode": "cloud", "version": "26.7.5.10",
		"node": g.node, "uptime": g.nodeUptime, "collection": g.collection,
		"pools_hourly": []map[string]interface{}{{"time": "2026-09-30 09:00:00", "fetch_tasks": 16, "schedule_tasks": 512, "merge_tasks": 3}},
		"pool_sizes":   map[string]int64{"fetch": 16, "schedule": 512, "merge": 16},
	})
	for _, want := range []string{"ch-01.example.internal", "SharedMergeTree detected", `"schedule_tasks":512`} {
		if !strings.Contains(html, want) {
			t.Errorf("payload丢失了%q", want)
		}
	}
}

// 当服务器有xid列时（24.3+），Keeper连接面板会选择xid。
// 没有客户端时每个探测都会失败为“present”，这是本测试能断言的形态；
// 缺失分支由22.8运行覆盖。
func TestKeeperConnectionSQL_SelectsXidWhenPresent(t *testing.T) {
	g := NewGenerator(nil, "onprem")
	if !strings.Contains(g.keeperConnectionSQL(), "toString(xid) AS xid") {
		t.Error("keeperConnectionSQL在列存在时必须选择xid")
	}
}

// Keeper成员面板：每个成员对自己的报告，以及pre-pass的判定。
// 它有自己的可见性——bundle可以带有keeper/文件而没有小时级Keeper指标——
// 并且文件中的每个值都要经过esc()。
func TestTemplate_KeeperMembersPanel(t *testing.T) {
	for _, want := range []string{
		`id="keeper-members-block" style="display:none"`, `id="tbl-keeper-members"`, `id="keeper-leaderless"`,
		`id="keeper-health-body"`, `id="keeper-connections-block"`,
		"DATA.keeper_members", "DATA.keeper_leaderless",
		"esc(m.member)", "esc(m.ensemble", "esc(m.state)", "esc(f.check+': '+f.text)",
		"needs a full snapshot",
		"bundle 之外的成员仍可能构成法定人数",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("模板丢失了%q", want)
		}
	}
	for _, banned := range []string{"'+m.member+'", "'+m.role+'", "'+f.text+'"} {
		if strings.Contains(htmlTemplate, banned) {
			t.Errorf("成员面板未转义地插值：%q", banned)
		}
	}
}

func TestKeeperPanelData(t *testing.T) {
	run := t.TempDir()
	if p := keeperPanelData(run); len(p) != 0 {
		t.Errorf("没有keeper/文件时不应向页面添加任何内容：%v", p)
	}
	if err := os.MkdirAll(filepath.Join(run, "keeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	ns := "This instance is not currently serving requests"
	for _, n := range []string{"keeper-a", "keeper-b"} {
		body := "# target: " + n + ":9181\n# ensemble: default\n\n## ruok\nimok\n\n## srvr\n" + ns + "\n\n## mntr\n" + ns + "\n"
		if err := os.WriteFile(filepath.Join(run, "keeper", n+"_9181.txt"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := keeperPanelData(run)
	if ms, _ := p["keeper_members"].([]keeper.Member); len(ms) != 2 {
		t.Errorf("keeper_members = %v", p["keeper_members"])
	}
	if ll, _ := p["keeper_leaderless"].([]string); len(ll) != 1 {
		t.Errorf("keeper_leaderless = %v；两个成员未在服务且无人服务", p["keeper_leaderless"])
	}
}

// 展示的alert会提供客户可在自己集群上运行的SQL以查看证据：
// 先执行它运行时的查询，再执行后续查询。默认折叠——
// 打开前卡片里只有一行摘要——并像面板中其他所有值一样转义。
func TestTemplate_AlertQueriesAreCollapsedAndEscaped(t *testing.T) {
	for _, want := range []string{
		"function alertQueries(a){",
		`'<details class="alert-more alert-queries"><summary>可在你的集群上运行的查询`,
		"html+=alertQueries(a);",
		"esc(q.title)", "esc(q.query)",
		`class="alert-copy"`,
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("模板丢失了%q", want)
		}
	}
	if strings.Contains(htmlTemplate, `alert-queries" open`) || strings.Contains(htmlTemplate, `alert-queries open`) {
		t.Error("查询块默认必须折叠")
	}
	for _, banned := range []string{"'+q.query+'", "'+q.title+'"} {
		if strings.Contains(htmlTemplate, banned) {
			t.Errorf("查询块未转义地插值：%q", banned)
		}
	}
}

func TestBuildHTML_OnlyDisplayedAlertsCarrySQL(t *testing.T) {
	html := buildHTML(map[string]interface{}{
		"generated_at": "2026-10-19 10:00:00 UTC", "mode": "onprem", "version": "26.7.5.10",
		"alerts": []alert.Result{
			{Name: "fetch_pool_saturated", Title: "Background fetch pool saturated", Severity: "warning",
				Message: "{hostname}", Rows: []map[string]interface{}{{"hostname": "node-1"}},
				Query:       "SELECT hostName() AS hostname FROM system.metric_log WHERE fired_marker = 1",
				Investigate: []alert.Investigation{{Title: "Every background pool now", Query: "SELECT metric FROM system.metrics WHERE followup_marker = 1"}}},
			{Name: "too_many_parts", Title: "Too many parts", Severity: "critical"}, // 干净的
		},
	})
	for _, want := range []string{"fired_marker = 1", "followup_marker = 1", "Every background pool now"} {
		if !strings.Contains(html, want) {
			t.Errorf("payload丢失了%q", want)
		}
	}
}
