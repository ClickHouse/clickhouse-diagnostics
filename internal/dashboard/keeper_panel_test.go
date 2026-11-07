package dashboard

import (
	"strings"
	"testing"
)

// Keeper Health 面板会对 metric_log 执行实时查询——每台服务器每秒一行，
// 约 1700 列。只有在有界窗口内读取少量窄列并按小时聚合时，它才保持低成本；
// 这些边界是重构时不能丢失的。
func TestKeeperMetricSQL_IsBoundedAndDegrades(t *testing.T) {
	g := &Generator{mode: "onprem"}
	full := g.keeperMetricSQL(true, true)
	for _, want := range []string{
		"INTERVAL 7 DAY",
		"toStartOfHour(event_time)",
		"GROUP BY time",
		"ProfileEvent_ZooKeeperTransactions",
		"ProfileEvent_ZooKeeperHardwareExceptions",
		"ProfileEvent_ZooKeeperWaitMicroseconds",
		"CurrentMetric_ZooKeeperSession",
		"FROM system.metric_log",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("keeper metric 查询丢失了 %q：\n%s", want, full)
		}
	}
	// 没有可选列的版本会以相同的输出名给出常量，因此面板 JS 永远不会看到缺失的键。
	degraded := g.keeperMetricSQL(false, false)
	for _, banned := range []string{"ProfileEvent_ZooKeeperWaitMicroseconds", "CurrentMetric_ZooKeeperSession"} {
		if strings.Contains(degraded, banned) {
			t.Errorf("降级查询不得引用 %s：\n%s", banned, degraded)
		}
	}
	for _, want := range []string{"AS wait_us", "AS sessions_min", "AS sessions_max"} {
		if !strings.Contains(degraded, want) {
			t.Errorf("降级查询必须保留输出名 %q：\n%s", want, degraded)
		}
	}
	if !strings.Contains((&Generator{mode: "cloud"}).keeperMetricSQL(true, true), "clusterAllReplicas(default, system.metric_log)") {
		t.Error("cloud 模式必须把 metric_log 分发到各 replica")
	}
}

// error_log 统计每个线程；query_log 只统计查询。回退版本必须存在
// （error_log 需要 24.8+），并且两者必须限定在相同的错误码和窗口内，
// 这样两种形态才可比较。
// 延迟面板必须能区分“此版本未导出 ZooKeeperWaitMicroseconds”与
// “列存在，只是服务器从未联系过 Keeper”。降级查询用 `0 AS wait_us` 替代，
// 因此全零序列在两种情况下看起来一样，JS 无法推断属于哪一种——collect()
// 为此发布 keeper_wait_available，模板必须据此分支，而不是归咎于服务器版本。
func TestKeeperPanel_WaitUnavailableIsNotConfusedWithNoKeeper(t *testing.T) {
	html := buildHTML(keeperIncidentFixture())
	for _, want := range []string{
		"DATA.keeper_wait_available!==false",
		"此版本未导出 ZooKeeperWaitMicroseconds",
		"此窗口内没有记录到 Keeper 等待时间",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("keeper 延迟面板缺少 %q", want)
		}
	}
	// 列存在时不得出现版本提示：它只能出现在 waitCol 条件判断的 false 分支中。
	i := strings.Index(html, "DATA.keeper_wait_available!==false")
	j := strings.Index(html, "此窗口内没有记录到 Keeper 等待时间")
	k := strings.Index(html, "此版本未导出 ZooKeeperWaitMicroseconds")
	if !(i < j && j < k) {
		t.Errorf("期望顺序为 waitCol 判断、无请求提示、版本提示；得到 %d、%d、%d", i, j, k)
	}
}

// 服务器不导出两个可选的 metric_log 计数器时，它们都会被替换为字面量 0，
// 因此每个 DATA 使用方都需要知道属于哪一种。面板读取 keeper_wait_available；
// skill 直接从 DATA 读取 sessions_min / sessions_max（bundle-layout §6），
// 出于同样的原因需要 keeper_session_available。两者必须同时发布——
// 只发布其中一个而不发布另一个，正是让等待列产生歧义的原因。
func TestKeeperPanel_OptionalColumnFlagsTravelTogether(t *testing.T) {
	html := buildHTML(keeperIncidentFixture())
	for _, want := range []string{`"keeper_wait_available":true`, `"keeper_session_available":true`} {
		if !strings.Contains(html, want) {
			t.Errorf("DATA 缺少 %s——读者无法区分被替换的 0 和真实的 0", want)
		}
	}
	// 标志所描述的替换必须仍然存在于降级查询中，
	// 否则这些标志毫无意义。
	degraded := (&Generator{mode: "onprem"}).keeperMetricSQL(false, false)
	for _, want := range []string{"0 AS wait_us", "0 AS sessions_min", "0 AS sessions_max"} {
		if !strings.Contains(degraded, want) {
			t.Errorf("降级查询不再替换 %q：\n%s", want, degraded)
		}
	}
}

func TestKeeperErrorsSQL_SourceSwitch(t *testing.T) {
	g := &Generator{mode: "onprem"}
	withErrLog := g.keeperErrorsSQL(true)
	fallback := g.keeperErrorsSQL(false)
	if !strings.Contains(withErrLog, "FROM system.error_log") || strings.Contains(withErrLog, "query_log") {
		t.Errorf("error_log 变体读取了错误的表：\n%s", withErrLog)
	}
	if !strings.Contains(fallback, "FROM system.query_log") || !strings.Contains(fallback, "errorCodeToName(exception_code)") {
		t.Errorf("query_log 回退必须读取 query_log 并解析错误码名称：\n%s", fallback)
	}
	for _, q := range []string{withErrLog, fallback} {
		for _, want := range []string{"INTERVAL 7 DAY", "999, 242, 319, 571, 252", "AS code_name", "AS count", "GROUP BY time, code_name"} {
			if !strings.Contains(q, want) {
				t.Errorf("keeper errors 查询丢失了 %q：\n%s", want, q)
			}
		}
	}
}

func TestKeeperConnectionSQL_CloudNamesReplica(t *testing.T) {
	if strings.Contains((&Generator{mode: "onprem"}).keeperConnectionSQL(), "hostName()") {
		t.Error("onprem 连接查询不得添加 replica 列")
	}
	cloud := (&Generator{mode: "cloud"}).keeperConnectionSQL()
	if !strings.Contains(cloud, "hostName() AS replica") || !strings.Contains(cloud, "clusterAllReplicas(default, system.zookeeper_connection)") {
		t.Errorf("cloud 连接查询必须标明 replica 并分发到各节点：\n%s", cloud)
	}
}

// 该面板是有条件的（没有 metric_log 行时隐藏），其判定规则必须与
// alert 和 skill 使用的一致，这样三个界面在同一小时上永远不会互相矛盾。
func TestTemplate_KeeperPanel(t *testing.T) {
	for _, want := range []string{
		`id="sec-keeper"`, `id="nav-keeper"`,
		"const rows=DATA.keeper_metric_hourly||[];",
		"if(!rows.length)return;",
		"chart-keeper-traffic", "chart-keeper-exceptions", "chart-keeper-latency", "chart-keeper-errors",
		"tbl-keeper-verdict", "tbl-keeper-connection",
		"hw[i]>1000&&pct<50)return 'UNAVAILABLE'",                                 // UNAVAILABLE：异常 AND 流量崩塌
		"if(pct===null)return hw[i]>1000?'exceptions, no traffic baseline':'ok';", // 没有基线 → 绝不是短暂抖动
		"DATA.keeper_errors_source",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("keeper 面板缺少 %q", want)
		}
	}
}

// Replica Details 每张复制表一行（大型共享集群上可达 25 000 行）。
// 图表可以使用每一行；表格必须分页，以免 DOM 承受不住，
// 并且第一页必须是需要关注的 replica。
func TestTemplate_ReplicasTableIsPaginated(t *testing.T) {
	for _, want := range []string{
		`id="replicas-pagination"`, `id="replicas-scope"`,
		"const REPLICA_PAGE=50;",
		"rows.slice(start,start+REPLICA_PAGE)",
		"window._replicaPg",
		"const rows=[...allRows].sort((a,b)=>rank(b)-rank(a));", // 需要关注的行排在前面，在分页之前
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("replicas 表分页缺少 %q", want)
		}
	}
	if strings.Contains(htmlTemplate, "renderTable('tbl-replicas',rows,") {
		t.Error("replicas 表又渲染了每一行——请分页")
	}
}
