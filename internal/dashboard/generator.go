package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/alert"
	"clickhouse-diagnostic/internal/bundle"
	"clickhouse-diagnostic/internal/hostinfo"
	"clickhouse-diagnostic/internal/keeper"
	"clickhouse-diagnostic/internal/query"
	"clickhouse-diagnostic/pkg"
)

// Generator从ClickHouse数据创建离线HTML诊断仪表盘。
type Generator struct {
	client        *pkg.ClickHouseClient
	mode          string
	serverVersion internal.Version
	analysis      query.AnalysisOpts
	analysisDir   string
	hostInfo      *hostinfo.Report
	probeCache    map[string]bool // 缓存的hasColumn/hasTable结果
	node          string          // system表所描述服务器的hostName()（FQDN()）
	nodeUptime    string          // 采集时该节点已运行多久
	collection    string          // execution_log的`collection:`备注（SharedMergeTree onprem → cloud切换）
	airgap        bool            // 内联Chart.js而不是从CDN加载（-airgap）
	// collect是本轮的采集模式，供横幅使用：kind为
	// "essential"或"full"，summary是执行日志的`collect:`行，
	// skipped是未运行的collector。
	collectMode struct {
		kind, summary string
		skipped       []string
	}
	skippedPanels []string // essential运行中未构建的重型面板
	detachedScope string   // detached面板的{detached_scope}（见query.DetachedScope）
	keeperDown    bool     // 无Keeper会话：跳过需要等待会话的面板
	stop          func() bool
	bundle        *bundle.Bundle // 运行目录：设置后面板读取已采集的文件（fromfiles.go）
	windowFrom    time.Time      // 显式的-from / -to，用于面板标题
	windowTo      time.Time
}

// keeperPanels需要实时Keeper会话。
var keeperPanels = map[string]bool{"keeper_connection": true}

// WithKeeperDown跳过需要实时Keeper会话的面板。
func (g *Generator) WithKeeperDown(down bool) *Generator {
	g.keeperDown = down
	return g
}

// WithStop在stop()返回true后跳过剩余的实时查询（本轮
// 时间预算已用完）。
func (g *Generator) WithStop(stop func() bool) *Generator {
	g.stop = stop
	return g
}

// WithDetachedScope像collector一样限制detached part面板。
func (g *Generator) WithDetachedScope(scope string) *Generator {
	g.detachedScope = scope
	return g
}

// heavyPanels读取所有表（system.parts / tables / detached_parts）：
// essential运行不会构建它们，横幅会列出它们。
var heavyPanels = map[string]bool{
	"storage_by_db": true, "engines_dist": true, "tables_list": true,
	"top_tables": true, "detached": true, "high_part_count": true,
}

// WithCollectMode说明本次是essential还是完整采集、
// 原因，以及跳过了哪些collector：dashboard会以
// 横幅显示，essential运行还会跳过读取所有表的面板。
func (g *Generator) WithCollectMode(kind, summary string, skipped []string) *Generator {
	g.collectMode.kind, g.collectMode.summary, g.collectMode.skipped = kind, summary, skipped
	return g
}

func (g *Generator) essential() bool { return g.collectMode.kind == "essential" }

// NewGenerator创建一个新的Generator。
func NewGenerator(client *pkg.ClickHouseClient, mode string) *Generator {
	return &Generator{client: client, mode: strings.ToLower(mode)}
}

// WithServerVersion记录所连接服务器的版本，使
// 查询分析部分解析出与AnalysisCollector.Collect相同的
// 版本目录覆盖（见collectAnalysis）。返回
// receiver以支持链式调用。
func (g *Generator) WithServerVersion(v internal.Version) *Generator {
	g.serverVersion = v
	return g
}

// WithHostInfo附加已为host_info.json采集的host facts，
// 使dashboard能在system表报告的内容旁显示OS/内核/CPU/内存上下文。
// 为nil（跳过host-info，或非Linux运行）时
// 直接省略该面板。返回receiver以支持链式调用。
func (g *Generator) WithHostInfo(r *hostinfo.Report) *Generator {
	g.hostInfo = r
	return g
}

// WithCollection在dashboard头部重复执行日志的node / uptime / collection
// 头行，使先打开HTML的读者
// 能看到这是哪个节点，以及本轮是否在SharedMergeTree集群上切换到了cloud
// 采集。空字符串保持头部原样。
// 返回receiver以支持链式调用。
func (g *Generator) WithCollection(node, uptime, collection string) *Generator {
	g.node, g.nodeUptime, g.collection = node, uptime, collection
	return g
}

// hostChecks依据CLICKHOUSE自身在启动时执行的检查
// 评估已采集的可调参数，并为每项检查返回一行。
//
// 这里判定"warning"的门槛刻意定得很窄：服务器自身也必须
// 就此告警。此列表中的每一项都对应
// programs/server/Server.cpp::sanityChecks中的真实检查（对于max_map_count
// 则是src/Common/Exception.cpp），因此dashboard绝不会凭空给出
// ClickHouse并未提出的建议。
//
// 刻意不标记：vm.swappiness。这是数据库调优中常见的
// 民间说法，但ClickHouse既不在启动时检查，也未记录
// 目标值，因此只作为事实报告。
func hostChecks(r *hostinfo.Report) []map[string]interface{} {
	if r == nil {
		return nil
	}
	t := r.Tunables
	out := []map[string]interface{}{}
	add := func(name, value, status, note string) {
		// 无法读取的可调参数绝不能渲染成健康状态。
		if value == "" {
			value, status = "(unreadable)", "unknown"
		}
		out = append(out, map[string]interface{}{
			"setting": name, "value": value, "status": status, "note": note,
		})
	}

	thpStatus, thpNote := "ok", `ClickHouse warns at startup when this is "always"`
	if strings.Contains(t.TransparentHugepages, "always") {
		thpStatus = "warning"
		thpNote = `ClickHouse warns at startup: transparent hugepages set to "always"`
	}
	add("transparent_hugepages", t.TransparentHugepages, thpStatus, thpNote)
	add("transparent_hugepages_defrag", t.THPDefrag, "info", "")

	ocStatus, ocNote := "ok", ""
	if strings.TrimSpace(t.OvercommitMemory) == "2" {
		ocStatus = "warning"
		ocNote = "ClickHouse warns at startup: memory overcommit is disabled (mode 2)"
	}
	add("vm.overcommit_memory", t.OvercommitMemory, ocStatus, ocNote)

	add("vm.max_map_count", t.MaxMapCount, "info",
		"ClickHouse errors when its live mappings exceed 90% of this")
	add("vm.swappiness", t.Swappiness, "info",
		"reported as a fact: ClickHouse does not check or document a target")
	add("fs.nr_open", t.FsNrOpen, "info", "")
	add("fs.file-max", t.FsFileMax, "info", "")
	add("cgroup memory limit", t.CgroupMemoryLimit, "info", "")
	add("cgroup cpu max", t.CgroupCPUMax, "info", "")
	// 仅在读到了内容时拼接："" + " / " + ""会trim成"/"，
	// 从而绕过add()的空值保护并渲染为健康。
	nofile := ""
	if t.ClickHouseNofileSoft != "" || t.ClickHouseNofileHard != "" {
		nofile = strings.TrimSpace(t.ClickHouseNofileSoft + " / " + t.ClickHouseNofileHard)
	}
	add("open files (soft/hard)", nofile, "info", "")
	add("max processes (soft)", t.ClickHouseNprocSoft, "info",
		"RLIMIT_NPROC — the OS cap on tasks, not the max_threads setting")

	// ClickHouse在启动时执行的唯一一项内存检查。
	if r.Memory.Available && r.Memory.AvailableBytes > 0 {
		const twoGiB = 2 << 30
		status, note := "ok", ""
		if r.Memory.AvailableBytes < twoGiB {
			status = "warning"
			note = "ClickHouse warns at startup: available memory below 2 GiB"
		}
		add("available memory", formatBytes(r.Memory.AvailableBytes), status, note)
	}
	return out
}

// WithAnalysis附加查询分析选项+存放
// .sql文件的目录。当opts启用时，Generate会向dashboard添加"Query
// Analysis"部分，使用AnalysisCollector写入磁盘的相同SQL
// 渲染。返回receiver以支持链式调用。
func (g *Generator) WithAnalysis(opts query.AnalysisOpts, dir string) *Generator {
	g.analysis = opts
	g.analysisDir = dir
	return g
}

// chResult是ClickHouse的JSONCompact响应信封。
type chResult struct {
	Meta []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"meta"`
	Data [][]json.RawMessage `json:"data"`
	Rows int                 `json:"rows"`
}

// records将chResult的行转换为column→value映射的切片。
func (r *chResult) records() []map[string]interface{} {
	if r == nil || r.Rows == 0 {
		return []map[string]interface{}{}
	}
	out := make([]map[string]interface{}, 0, r.Rows)
	for _, row := range r.Data {
		rec := make(map[string]interface{}, len(r.Meta))
		for i, col := range r.Meta {
			if i < len(row) {
				var v interface{}
				_ = json.Unmarshal(row[i], &v)
				rec[col.Name] = v
			}
		}
		out = append(out, rec)
	}
	return out
}

// sysTable返回当前模式下正确的system表引用。
// 委托给共享模板helper — 参见internal/query/template.go
// 中的cloud-shared-tables允许列表（唯一事实来源）。
func (g *Generator) sysTable(table string) string {
	return query.SysTable(g.mode, table)
}

// parseUInt64从JSONCompact单元格中提取整数。ClickHouse
// 默认将UInt64/Int64序列化为JSON字符串
// （output_format_json_quote_64bit_integers=1），因此直接unmarshal到
// float64会静默失败。此helper同时接受两种形式。
func parseUInt64(raw json.RawMessage) int64 {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	var n float64
	_ = json.Unmarshal(raw, &n)
	return int64(n)
}

// execJSON运行一条SQL语句（不带FORMAT子句）并解析JSONCompact响应。
func (g *Generator) execJSON(sql string) (*chResult, error) {
	raw, err := g.client.ExecuteQueryWithFormat(sql)
	if err != nil {
		return nil, err
	}
	var res chResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("JSON 解析失败：%w（%.200s）", err, raw)
	}
	return &res, nil
}

// safeQuery运行查询，失败时打印警告，并且始终返回records。
func (g *Generator) safeQuery(label, sql string) []map[string]interface{} {
	if g.essential() && heavyPanels[label] {
		g.skippedPanels = append(g.skippedPanels, label)
		return []map[string]interface{}{}
	}
	if g.keeperDown && keeperPanels[label] {
		g.skippedPanels = append(g.skippedPanels, label+"（无 Keeper 会话）")
		return []map[string]interface{}{}
	}
	if g.stop != nil && g.stop() {
		g.skippedPanels = append(g.skippedPanels, label+"（时间预算）")
		return []map[string]interface{}{}
	}
	res, err := g.execJSON(sql)
	if err != nil {
		fmt.Printf("  [dashboard] %s: %v\n", label, err)
		return []map[string]interface{}{}
	}
	return res.records()
}

// scalarCount运行sql（预期返回单个count()）并返回
// 该值。count()是UInt64，JSON渲染为带引号的字符串，因此
// 字符串和数值两种形式都会被解析。
func (g *Generator) scalarCount(sql string) (int64, error) {
	r, err := g.execJSON(sql)
	if err != nil {
		return 0, err
	}
	if r == nil || r.Rows == 0 || len(r.Data) == 0 || len(r.Data[0]) == 0 {
		return 0, nil
	}
	var s string
	if json.Unmarshal(r.Data[0][0], &s) == nil {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n, nil
	}
	var f float64
	if json.Unmarshal(r.Data[0][0], &f) == nil {
		return int64(f), nil
	}
	return 0, nil
}

// probe运行一次模式存在性count查询（按key缓存），并报告
// 对象是否存在。
//
// 探测出错时它FAIL OPEN — 假定对象存在并打印
// 一条[dashboard]警告。临时性探测失败不得静默
// 将dashboard降级为最旧服务器的形态（本工具存在的意义
// 就是诊断；静默降级是错误的默认行为）。若对象
// 确实不存在，后续的完整查询会经safeQuery失败，
// 并给出自己的可见警告。真正的零计数意味着
// "不存在"，从而选择回退方案。
func (g *Generator) probe(key, sql string) bool {
	// 没有可用于探测的client（例如隔离构建SQL的单元测试）：
	// 假定存在，以便输出完整的现代查询。
	if g.client == nil {
		return true
	}
	if g.probeCache == nil {
		g.probeCache = map[string]bool{}
	}
	if v, ok := g.probeCache[key]; ok {
		return v
	}
	n, err := g.scalarCount(sql)
	if err != nil {
		fmt.Printf("  [dashboard] schema 探测 %q 失败：%v（假定存在）\n", key, err)
		g.probeCache[key] = true
		return true
	}
	present := n > 0
	g.probeCache[key] = present
	return present
}

// hasColumn / hasTable让dashboard的实时查询在
// 支持的版本范围（ClickHouse 22.8+）内保持兼容，而无需
// 硬编码每个列的"引入版本"。dashboard动态
// 构建SQL（不同于使用版本目录机制的
// .sql查询文件），因此运行时模式检查既
// 更简单也更稳健 — 它还覆盖了可能在任何版本上
// 被配置禁用的可选表。模式在各副本间
// 相同，因此这些检查直接查询本地system表。
func (g *Generator) hasColumn(table, column string) bool {
	// 防止权限盲区导致的误判。ClickHouse会
	// 按grant静默过滤system.columns — 没有system.<table>
	// SELECT权限的用户得到零行，而不是ACCESS_DENIED。这会被读成
	// "列不存在"，并会让每个受控面板在现代服务器上
	// 降级为最旧服务器的形态，且没有任何警告（在仅具
	// 数据库级grant的Cloud 26.2服务上观察到）。
	//
	// 真正存在的表一定有列，因此"看不到此表的任何
	// 一列"意味着探测无法作答 — fail open。
	if !g.probeCanSeeColumns(table) {
		return true
	}
	return g.probe(table+"."+column, fmt.Sprintf(
		"SELECT count() FROM system.columns WHERE database='system' AND table='%s' AND name='%s'",
		table, column))
}

// probeCanSeeColumns报告system.columns是否向当前用户
// 暴露给定system表的任何列。当无法看到时
// 每张表警告一次，因为此后该表的所有hasColumn()答案都只是猜测。
func (g *Generator) probeCanSeeColumns(table string) bool {
	key := "visible:" + table
	if g.probeCache != nil {
		if v, ok := g.probeCache[key]; ok {
			return v
		}
	}
	visible := g.probe(key, fmt.Sprintf(
		"SELECT count() FROM system.columns WHERE database='system' AND table='%s'", table))
	if !visible {
		// 不要归咎原因：此探测读取的是本地
		// system.columns，因此零行可能意味着缺少SELECT grant，或者 —
		// 在cloud中 — 表只存在于远程副本。两者都
		// 导致相同的fail-open，因此只陈述结果，让操作者
		// 判断原因。
		fmt.Printf("  [dashboard] 无法在本地检查 system.%s 的列 "+
			"— 假定现代列均存在\n", table)
	}
	return visible
}

func (g *Generator) hasTable(table string) bool {
	// 与列模式（各副本相同）不同，表的
	// *存在性*可能因副本而异 — system.crash_log只会
	// 在崩溃的节点上出现。cloud模式下我们探测每个副本，这样
	// 非服务节点上的崩溃至少能被DETECTED（探测返回
	// true，且面板查询会执行）。
	//
	// 诚实的局限：在这种部分存在的情况下，受控面板
	// 查询 — clusterAllReplicas(default, system.<table>) — 仍会在
	// 缺少该表的副本上抛出UNKNOWN_TABLE，因此结果
	// 是可见的[dashboard]错误，而不是崩溃节点的行。
	// 这正是预期的"暴露而非隐藏"权衡：要渲染这些行
	// 需要容错的逐节点扇出。
	//
	// （system.tables列在SharedSystemTables中 — 参见
	// internal/query/template.go — 因为它的用户表行在
	// 各处相同；这说的是行内容，而不是
	// 逐节点system日志表的存在性，后者才是我们在这里探测的。）
	tablesRef := "system.tables"
	if g.mode == "cloud" {
		tablesRef = "clusterAllReplicas(default, system.tables)"
	}
	// 与hasColumn相同的权限盲区保护：system.tables会
	// 按grant过滤而不报错，而服务器总会有SOME
	// system表 — 因此零行意味着探测无法作答。fail open
	// 并让面板查询暴露自己的错误。
	//
	// 只在第一张表上警告，而不是每个受控面板一次：探测结果
	// 已缓存，但在其后直接Printf会为
	// crash_log、asynchronous_insert_log等重复同一行。
	const visKey = "visible:system-tables"
	_, alreadyProbed := g.probeCache[visKey]
	if !g.probe(visKey, fmt.Sprintf(
		"SELECT count() FROM %s WHERE database='system'", tablesRef)) {
		if !alreadyProbed {
			fmt.Printf("  [dashboard] 无法读取 %s — 假定系统日志表存在 "+
				"（若面板为空请检查 SELECT 授权）\n", tablesRef)
		}
		return true
	}
	return g.probe("table:"+table, fmt.Sprintf(
		"SELECT count() FROM %s WHERE database='system' AND name='%s'", tablesRef, table))
}

// Generate在outputDir中生成dashboard.html。
// 若跳过了告警评估，alertResults可能为nil或空。
func (g *Generator) Generate(outputDir string, alertResults []alert.Result) error {
	fmt.Println("\n正在生成 HTML 仪表盘...")
	payload := g.collect()
	// 模式关系图是dashboard.html旁独立自包含的第二页面，
	// 从Schema标签页按需加载（为何不内联见schema_graph.go）。
	// 它先写入，只有当它存在后payload才指向它：
	// 此后从bundle复制出的dashboard.html会在该标签页处显示提示，
	// 而不是一个损坏的框架。
	version, _ := payload["version"].(string)
	var sg map[string]interface{}
	switch {
	case g.bundle != nil:
		sg = g.schemaGraphFromFiles(version) // system.tables未采集时为nil
	case g.essential():
		g.skippedPanels = append(g.skippedPanels, "模式关系图")
	default:
		sg = g.collectSchemaGraph(version)
	}
	if sg != nil {
		dst := filepath.Join(outputDir, schemaGraphFile)
		if err := os.WriteFile(dst, []byte(buildSchemaGraphHTML(sg)), 0640); err != nil {
			fmt.Printf("  [dashboard] 未写入模式关系图：%v\n", err)
		} else {
			payload["schema_graph"] = schemaGraphSummary(sg)
			fmt.Printf("模式关系图已保存：%s\n", dst)
		}
	}
	// 需要outputDir，因此在这里而不是在collect()中完成。
	payload["bundle_files"] = collectBundleFiles(outputDir)
	payload["collect"] = map[string]interface{}{
		"kind": g.collectMode.kind, "summary": g.collectMode.summary,
		"skipped_collectors": nonNil(g.collectMode.skipped), "skipped_panels": nonNil(g.skippedPanels),
	}
	for k, v := range keeperPanelData(outputDir) {
		payload[k] = v
	}
	if alertResults == nil {
		alertResults = []alert.Result{}
	}
	payload["alerts"] = alertResults
	html := renderHTML(payload, g.airgap)
	dst := filepath.Join(outputDir, "dashboard.html")
	if err := os.WriteFile(dst, []byte(html), 0640); err != nil {
		return fmt.Errorf("写入 dashboard 失败：%w", err)
	}
	fmt.Printf("仪表盘已保存：%s\n", dst)
	return nil
}

// keeperPanelData读取keeper/<member>.txt — 在dashboard之前写入，
// 位于同一运行目录 — 供Keeper成员面板使用。文件
// 不存在时（旧bundle、cloud默认、-keeper-mntr关闭）返回空，
// 因此面板完全不渲染，而不是显示为空。
func keeperPanelData(runDir string) map[string]interface{} {
	ms := keeper.ReadMembers(runDir)
	if len(ms) == 0 {
		return nil
	}
	return map[string]interface{}{"keeper_members": ms, "keeper_leaderless": keeper.Leaderless(ms)}
}

// tablesListSQL构建所有表的查询（含大小）。system.tables和
// system.parts在各副本间共享，因此同一查询适用于
// cloud、onprem和gov。
func (g *Generator) tablesListSQL() string {
	notSystem := "database NOT IN ('system','information_schema','INFORMATION_SCHEMA')"
	return fmt.Sprintf(`
		SELECT t.database, t.name AS table_name, t.engine,
			coalesce(p.parts, 0) AS parts,
			-- total_rows is a ROW COUNT, not a byte size: formatReadableSize would
			-- render 1,992,000 rows as "1.90 MiB". formatReadableQuantity exists
			-- well below the 22.8 floor.
			formatReadableQuantity(coalesce(p.total_rows, 0)) AS total_rows,
			formatReadableSize(coalesce(p.bytes_on_disk, 0)) AS size,
			coalesce(p.bytes_on_disk, 0) AS bytes_on_disk,
			t.partition_key, t.sorting_key, t.storage_policy
		FROM %s AS t
		LEFT JOIN (
			SELECT database, table, count() AS parts,
				sum(rows) AS total_rows, sum(bytes_on_disk) AS bytes_on_disk
			FROM %s WHERE active = 1
			GROUP BY database, table
		) AS p ON t.database = p.database AND t.name = p.table
		WHERE t.%s
		ORDER BY bytes_on_disk DESC
		LIMIT 2000`, g.sysTable("tables"), g.sysTable("parts"), notSystem)
}

// sampleQueryCol返回每个
// normalized_query_hash的代表性SQL文本投影 — 或在gov模式下返回脱敏占位。
//
// 这些分组中的每一行共享一个normalized_query_hash，即
// 相同的查询SHAPE，因此any(query)是该分组的真实示例，
// 而不是任意混合；只有字面量属于被选中的那次执行。
// 它被截断，因为病态查询可能有数兆字节，
// 而这段文本会直接嵌入HTML。
//
// gov模式得到占位：queries.gov/*.sql会哈希数据库和表名，
// 而query_log.query是客户针对这些同名对象运行的原始SQL，
// 在此处输出它会破坏哈希。
func (g *Generator) sampleQueryCol() string {
	if g.mode == "gov" {
		return "'' AS sample_query"
	}
	// leftUTF8：按字节的left()会在查询文本中拆分多字节
	// 字符，留下无效UTF-8。与collector中的原因相同。
	return "leftUTF8(any(query), 600) AS sample_query"
}

// querySlowSQL构建最慢查询模式面板。
//
// hash是完整的normalized_query_hash，以字符串表示。它是UInt64，
// 绝不能以JSON数字到达页面：JavaScript会舍入超过2^53的值，
// 从而静默破坏读者本应复制的标识符。
func (g *Generator) querySlowSQL() string {
	// 与queryByUserSQL相同的窗口约定：异常行必须IN
	// 窗口内，否则countIf(exception_code != 0)在结构上恒为零 —
	// 失败会记录为ExceptionWhileProcessing/ExceptionBeforeStart，绝不会
	// 是QueryFinish。成本聚合仍仅限QueryFinish（用sum/greatest而不是
	// avgIf，因此全失败的形态得到0，而不是对JSON不友好的nan）。
	return fmt.Sprintf(
		`SELECT toString(normalized_query_hash) AS hash,
				query_kind, user,
				countIf(type = 'QueryFinish') AS executions,
				round(sumIf(query_duration_ms, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1), 0) AS avg_duration_ms,
				maxIf(query_duration_ms, type = 'QueryFinish') AS max_duration_ms,
				round(sumIf(read_bytes, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1) / 1048576, 2) AS avg_read_mb,
				round(sumIf(memory_usage, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1) / 1048576, 2) AS avg_memory_mb,
				countIf(exception_code != 0) AS errors,
				%s
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND ((type = 'QueryFinish' AND query_duration_ms > 0)
				OR type IN ('ExceptionWhileProcessing', 'ExceptionBeforeStart'))
		 GROUP BY hash, query_kind, user
		 ORDER BY avg_duration_ms DESC LIMIT 20`,
		g.sampleQueryCol(), g.sysTable("query_log"),
	)
}

// queryHeavySQL构建读取量最大的面板。与
// querySlowSQL相同的完整hash和示例查询约定。
func (g *Generator) queryHeavySQL() string {
	return fmt.Sprintf(
		`SELECT toString(normalized_query_hash) AS hash,
				query_kind, user,
				count() AS executions,
				round(avg(read_bytes)/1048576, 2) AS avg_read_mb,
				formatReadableSize(sum(read_bytes)) AS total_read,
				round(avg(query_duration_ms), 0) AS avg_duration_ms,
				%s
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND type = 'QueryFinish'
		 GROUP BY hash, query_kind, user
		 ORDER BY avg_read_mb DESC LIMIT 20`,
		g.sampleQueryCol(), g.sysTable("query_log"),
	)
}

// textLogRowCap限制Logs面板嵌入的行数。
//
// 这个数字正是面板安全的原因。原始服务器日志以每文件
// 最多50 MiB（--logs-max-mb）尾部拷贝进bundle，
// 将其内联到自包含HTML会生成一个浏览器必须先作为
// 单个字符串字面量解析完才能绘制的文档。因此
// 面板嵌入来自system.text_log的有界、结构化、已分诊的行，
// 而“已采集文件”面板链接到原始文件。
const textLogRowCap = 1000

// textLogMessageCap截断每条消息。system.text_log消息无界
// （堆栈跟踪、整条查询）；在行数上限下，未截断的列是
// 约350 KiB负载与数兆字节负载之间的差别。
const textLogMessageCap = 300

// textLogSQL构建Logs面板查询：仅最近的Warning及更严重级别。
//
// 刻意排除Info/Debug/Trace。它们构成了大部分数据量，
// 也是朴素日志面板不可用的原因；需要它们的人想要的是
// 原始文件，已在“已采集文件”下链接。
func (g *Generator) textLogSQL() string {
	host := ""
	if g.hasColumn("text_log", "hostname") {
		host = "hostname,"
	}
	// 在SUBQUERY中过滤和切片，在外层SELECT中格式化。
	//
	// 分析器会根据SELECT别名解析WHERE子句中的标识符，因此
	// 扁平的`SELECT toString(event_time) AS event_time ... WHERE event_time >
	// now() - INTERVAL 24 HOUR`会将String与DateTime比较并以
	// NO_COMMON_TYPE（code 386）失败 — 已在26.4.5.143上验证。这与
	// exceptions面板中已为exception_code记录的陷阱相同；这里
	// 内层查询让event_time保持DateTime，使谓词有类型，
	// 只有外层投影将其转成字符串。
	//
	// 按字符串化值对外层查询排序仍然按时间顺序：
	// 'YYYY-MM-DD hh:mm:ss'按字典序排序即为时间顺序。
	//
	// 用leftUTF8而不是left：left()按字节计数，因此截断消息
	// 可能拆分字符并留下无效UTF-8。json.Marshal会将其掩盖为U+FFFD
	// 而不破坏页面，但归档collector会把服务器字节直接写入
	// 磁盘，无法承受这一点，因此两者使用
	// 相同的函数。
	return fmt.Sprintf(
		`SELECT toString(event_time) AS event_time,
				%s level, logger_name,
				leftUTF8(message, %d) AS message
		 FROM (
			SELECT event_time, %s level, logger_name, message
			FROM %s
			WHERE event_time > now() - INTERVAL 24 HOUR
			  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
			ORDER BY event_time DESC
			LIMIT %d
		 )
		 ORDER BY event_time DESC`,
		host, textLogMessageCap, host, g.sysTable("text_log"), textLogRowCap,
	)
}

// collectBundleFiles为运行写入dashboard.html旁的每个文件建立索引，
// 使dashboard能指向bundle的其余部分，而不是将其
// 吞掉。这里不嵌入任何内容：仅原始服务器日志就以每文件
// 最多50 MiB（--logs-max-mb）尾部拷贝，内联会生成
// 浏览器无法打开的文档。
//
// 路径是bundle相对的，因此链接在解压的归档中有效，
// 如果单独寄出dashboard.html则只会404 — 这就是
// 面板说明文件位于其旁边的缘故。
func collectBundleFiles(outputDir string) []map[string]interface{} {
	out := []map[string]interface{}{}
	err := filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is skipped, not fatal
		}
		rel, relErr := filepath.Rel(outputDir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// dashboard不列出自己 — 也不列出它从自身标签页打开的
		// 模式关系图页面 — 操作系统垃圾文件也不是产物。
		if rel == "dashboard.html" || rel == schemaGraphFile || strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		dir := path0(rel)
		out = append(out, map[string]interface{}{
			"file":  rel,
			"group": dir,
			"kind":  strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), "."),
			"size":  formatBytes(info.Size()),
			"bytes": info.Size(),
			"href":  rel,
		})
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		gi, gj := out[i]["group"].(string), out[j]["group"].(string)
		if gi != gj {
			return gi < gj
		}
		return out[i]["file"].(string) < out[j]["file"].(string)
	})
	return out
}

// path0返回bundle相对路径的顶层文件夹，对于位于
// bundle根目录的文件则返回""。
func path0(rel string) string {
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

// formatBytes以最合理的最大单位渲染字节数。
func formatBytes(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(u), 0
	for n := b / u; n >= u && exp < 3; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGT"[exp])
}

// topTablesSQL构建最大表面板。
//
// total_rows使用formatReadableQuantity，而不是formatReadableSize：后者是
// 字节格式化器，会把1,992,000行渲染成"1.90 MiB"。
func (g *Generator) topTablesSQL() string {
	return fmt.Sprintf(
		`SELECT database, table, count() AS parts,
			formatReadableQuantity(sum(rows)) AS total_rows,
			formatReadableSize(sum(bytes_on_disk)) AS compressed_size,
			round(if(sum(data_compressed_bytes)>0,
				sum(data_uncompressed_bytes)/sum(data_compressed_bytes), 0), 2) AS compression_ratio
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database, table
		 ORDER BY sum(bytes_on_disk) DESC LIMIT 20`,
		g.sysTable("parts"),
	)
}

// queryByUserSQL构建每用户活动摘要。
//
// 窗口必须包含异常类型，而不仅是QueryFinish：失败的
// 查询会记录为ExceptionWhileProcessing（执行期间抛出）或
// ExceptionBeforeStart（解析/分析期间抛出，例如UNKNOWN_TABLE），
// 绝不会是QueryFinish。将整个查询限定在QueryFinish会使
// countIf(exception_code != 0)在结构上恒为0，因此面板的错误
// 列 — 以及以它为键的alert-row高亮 — 永远无法触发。
//
// executions和成本聚合仍仅限QueryFinish，使其含义
// 不变。平均耗时是sum/count而不是avgIf，因此只有
// 失败的用户得到0，而不是对JSON不友好的nan。
func (g *Generator) queryByUserSQL() string {
	return fmt.Sprintf(
		`SELECT user,
				countIf(type = 'QueryFinish') AS executions,
				round(sumIf(query_duration_ms, type = 'QueryFinish')
					  / greatest(countIf(type = 'QueryFinish'), 1), 0) AS avg_duration_ms,
				round(sumIf(read_bytes, type = 'QueryFinish')/1073741824, 3) AS total_read_gb,
				round(sumIf(memory_usage, type = 'QueryFinish')/1073741824, 3) AS total_memory_gb,
				countIf(exception_code != 0) AS error_count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND type IN ('QueryFinish', 'ExceptionWhileProcessing', 'ExceptionBeforeStart')
		 GROUP BY user ORDER BY executions DESC LIMIT 20`,
		g.sysTable("query_log"),
	)
}

// dictionariesSQL构建字典查询，对gov省略last_exception。
// cloud模式下查询经过clusterAllReplicas，因此
// 每个(dictionary, replica)得到一行 — 字典的RUNTIME状态
// （status、bytes_allocated、hit_rate、query_count）是按pod的，
// 尽管定义通过Keeper共享；在pod A上被查询过而在pod B上没有的
// 字典会在A上显示LOADED，在B上显示
// NOT_LOADED。hostname()在每个副本上远程求值，
// 因此可以用产生该行的pod标记每一行。
//
// 列选择依据system.dictionaries参考：
//
//	https://clickhouse.com/docs/operations/system-tables/dictionaries
//
// gov模式下我们额外脱敏`source`、`origin`、`comment`
// 和`last_exception` — 它们可能携带连接字符串、文件
// 路径、异常文本和用户注释，从而泄露gov模式
// 原本正在哈希的模式或基础设施细节。
//
// 注意：目前不可达 — cmd/main.go在gov模式下
// 完全拒绝生成dashboard，因为其余约20个面板会选取原始
// 标识符，而本脱敏并未覆盖。保留它（与异步面板和
// collectAnalysis中的gov分支一样），以便后续恢复哈希gov dashboard
// 的后续工作；它NOT是今天的生效保证。
func (g *Generator) dictionariesSQL() string {
	exceptionCol, sourceCol, originCol, commentCol := "last_exception", "source", "origin", "comment"
	if g.mode == "gov" {
		exceptionCol = "'' AS last_exception"
		sourceCol = "'' AS source"
		originCol = "'' AS origin"
		commentCol = "'' AS comment"
	}
	// error_count是在22.8之后加入system.dictionaries的；回退
	// 到0，使dashboard列在较旧服务器上仍能渲染。
	errorCountCol := "error_count"
	if !g.hasColumn("dictionaries", "error_count") {
		errorCountCol = "0 AS error_count"
	}
	return fmt.Sprintf(`
		SELECT
			hostname()                                          AS hostname,
			database, name, status, type, toString(uuid)        AS uuid,
			bytes_allocated,
			formatReadableSize(bytes_allocated)                 AS bytes_allocated_human,
			element_count, query_count, %s,
			round(hit_rate * 100, 2)                            AS hit_rate_pct,
			round(found_rate * 100, 2)                          AS found_rate_pct,
			round(load_factor, 4)                               AS load_factor,
			arrayStringConcat(key.names, ', ')                  AS key_names,
			arrayStringConcat(key.types, ', ')                  AS key_types,
			arrayStringConcat(attribute.names, ', ')            AS attribute_names,
			arrayStringConcat(attribute.types, ', ')            AS attribute_types,
			lifetime_min, lifetime_max,
			toString(loading_start_time)                        AS loading_start_time,
			toString(last_successful_update_time)               AS last_update,
			round(loading_duration, 2)                          AS loading_duration_s,
			%s, %s, %s, %s
		FROM %s
		ORDER BY database, name, hostname`,
		errorCountCol, sourceCol, originCol, commentCol, exceptionCol, g.sysTable("dictionaries"))
}

// collect从ClickHouse收集所有指标并返回可直接JSON化的map。
// keeperMetricSQL是Keeper Health面板的主序列：
// Keeper健康测试所基于的两个计数器（见alerts/keeper_health.yaml
// 和skill的HC-3.8），按小时覆盖7天，外加
// 延迟线和会话范围所需的输入。metric_log是每服务器每秒一行，
// 因此读取一周的五个窄列只有几MiB — 足够便宜，
// 可用于实时dashboard查询。某版本缺少的列
// 会替换为0，使面板降级而不是报错。
func (g *Generator) keeperMetricSQL(hasWait, hasSession bool) string {
	wait := "0 AS wait_us"
	if hasWait {
		wait = "sum(ProfileEvent_ZooKeeperWaitMicroseconds) AS wait_us"
	}
	sess := "0 AS sessions_min, 0 AS sessions_max"
	if hasSession {
		sess = "min(CurrentMetric_ZooKeeperSession) AS sessions_min, max(CurrentMetric_ZooKeeperSession) AS sessions_max"
	}
	return fmt.Sprintf(
		`SELECT toString(toStartOfHour(event_time)) AS time,
				sum(ProfileEvent_ZooKeeperTransactions)       AS transactions,
				sum(ProfileEvent_ZooKeeperHardwareExceptions) AS hw_exceptions,
				sum(ProfileEvent_ZooKeeperUserExceptions)     AS user_exceptions,
				%s,
				%s
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		 GROUP BY time ORDER BY time`,
		wait, sess, g.sysTable("metric_log"))
}

// poolsMetricSQL是后台池面板的序列：每小时，
// fetch、schedule和merge池中任务的每小时平均值，以及
// 该平均值在各副本上的最大值，这样空闲节点中的一台饱和节点
// 不会被平均掉。7天metric_log的三个窄列。
func (g *Generator) poolsMetricSQL() string {
	return fmt.Sprintf(
		`SELECT time,
				max(fetch)    AS fetch_tasks,
				max(schedule) AS schedule_tasks,
				max(merge)    AS merge_tasks
		 FROM (
			SELECT toString(toStartOfHour(event_time)) AS time, hostName() AS h,
				   avg(CurrentMetric_BackgroundFetchesPoolTask)   AS fetch,
				   avg(CurrentMetric_BackgroundSchedulePoolTask)  AS schedule,
				   avg(CurrentMetric_BackgroundMergesAndMutationsPoolTask) AS merge
			FROM %s
			WHERE event_time > now() - INTERVAL 7 DAY
			GROUP BY time, h)
		 GROUP BY time ORDER BY time`,
		g.sysTable("metric_log"))
}

// poolSizes从system.server_settings（23.3+）或
// system.settings（更旧）读取background_*_pool_size；每个池一个标量。cloud采集中
// 该表会扇出，并取各副本的min()：图表的序列是
// 最差副本的占用，因此最小的池是判定时必须
// 对照的线 — 更大的兄弟池会掩盖饱和。缺失的
// 表或设置得到0 — 没有参考线。
func (g *Generator) poolSizes() map[string]int64 {
	out := map[string]int64{"fetch": 0, "schedule": 0, "merge": 0}
	// 来源跟随服务器版本，而不是表可见性：从23.3起
	// 大小位于system.server_settings，而system.settings仍
	// 以旧的会话默认值列出这些名称（8 / 128，而真实
	// 值为16 / 512）。server_settings不可读（grant）的23.3+服务器
	// 因此完全没有线，而不是一条错误的线。对于未知
	// 版本（单元测试）则回退到存在的任一张表。
	table := "settings"
	v := g.serverVersion
	switch {
	case v != (internal.Version{}) && (v.Major > 23 || (v.Major == 23 && v.Minor >= 3)):
		if !g.hasTable("server_settings") {
			return out
		}
		table = "server_settings"
	case v != (internal.Version{}):
		// 低于23.3：system.settings是权威来源
	case g.hasTable("server_settings"):
		table = "server_settings"
	}
	for key, setting := range map[string]string{
		"fetch": "background_fetches_pool_size", "schedule": "background_schedule_pool_size", "merge": "background_pool_size",
	} {
		if n, err := g.scalarCount(fmt.Sprintf("SELECT min(toUInt64OrZero(value)) FROM %s WHERE name = '%s'", g.sysTable(table), setting)); err == nil {
			out[key] = n
		}
	}
	return out
}

// keeperErrorsSQL按小时统计依赖Keeper的错误码：999
// KEEPER_EXCEPTION, 242 TABLE_IS_READ_ONLY, 319 UNKNOWN_STATUS_OF_INSERT,
// 571 DATABASE_REPLICATION_FAILED和252 TOO_MANY_PARTS（通常
// 是merge未被调度的后果）。system.error_log（24.8+）
// 更受青睐，因为它统计每个线程 — 后台merge、fetch和
// DDL worker从不进入query_log；query_log回退会在
// 面板副标题中说明这一点。
func (g *Generator) keeperErrorsSQL(useErrorLog bool) string {
	if useErrorLog {
		return fmt.Sprintf(
			`SELECT toString(toStartOfHour(event_time)) AS time,
					error AS code_name, sum(value) AS count
			 FROM %s
			 WHERE event_time > now() - INTERVAL 7 DAY
			   AND code IN (999, 242, 319, 571, 252)
			 GROUP BY time, code_name ORDER BY time`,
			g.sysTable("error_log"))
	}
	return fmt.Sprintf(
		`SELECT toString(toStartOfHour(event_time)) AS time,
				errorCodeToName(exception_code) AS code_name, count() AS count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND type = 'ExceptionWhileProcessing'
		   AND exception_code IN (999, 242, 319, 571, 252)
		 GROUP BY time, code_name ORDER BY time`,
		g.sysTable("query_log"))
}

// keeperConnectionSQL列出实时Keeper连接（23.8+）：哪个主机、
// 会话有多旧、是否过期。cloud模式下按副本。
func (g *Generator) keeperConnectionSQL() string {
	host := ""
	if g.mode == "cloud" {
		host = "hostName() AS replica, "
	}
	// xid（24.3+）是会话的请求计数器；它在2^31处回绕，
	// 客户端会续期会话 — 告警keeper_xid_renewal_due将其
	// 转换为小时余量，此列让读者看到原始值。
	xid := ""
	if g.hasColumn("zookeeper_connection", "xid") {
		xid = ", toString(xid) AS xid"
	}
	return fmt.Sprintf(
		`SELECT %sname, host, toString(port) AS port, toString(index) AS index,
				toString(connected_time) AS connected_time,
				toString(session_uptime_elapsed_seconds) AS session_uptime_s,
				toString(is_expired) AS is_expired,
				toString(keeper_api_version) AS api_version%s
		 FROM %s
		 ORDER BY name`,
		host, xid, g.sysTable("zookeeper_connection"))
}

func (g *Generator) collect() map[string]interface{} {
	if g.bundle != nil {
		return g.collectFromFiles()
	}
	p := map[string]interface{}{
		"generated_at":    time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		"mode":            g.mode,
		"version":         "",
		"uptime":          g.nodeUptime,
		"node":            g.node,
		"collection":      g.collection,
		"total_databases": 0,
		"total_tables":    0,
		"active_parts":    0,
		"total_size":      "N/A",
	}

	// 版本
	if r, err := g.execJSON("SELECT version() AS version"); err == nil && r.Rows > 0 {
		var v string
		_ = json.Unmarshal(r.Data[0][0], &v)
		p["version"] = v
	}

	// 运行时间 — 本轮记录了执行日志的短格式时使用它，
	// 使两个产物说法一致；否则询问服务器。
	if g.nodeUptime == "" {
		if r, err := g.execJSON("SELECT formatReadableTimeDelta(uptime()) AS uptime"); err == nil && r.Rows > 0 {
			var v string
			_ = json.Unmarshal(r.Data[0][0], &v)
			p["uptime"] = v
		}
	}

	// 表/数据库摘要（读取所有表：essential运行中不执行）
	if g.essential() {
		g.skippedPanels = append(g.skippedPanels, "表与 part 计数")
	} else if r, err := g.execJSON(fmt.Sprintf(
		`SELECT uniq(database) AS dbs, count() AS tbls
		 FROM %s
		 WHERE database NOT IN ('system','information_schema','INFORMATION_SCHEMA')`,
		g.sysTable("tables"),
	)); err == nil && r.Rows > 0 {
		p["total_databases"] = parseUInt64(r.Data[0][0])
		p["total_tables"] = parseUInt64(r.Data[0][1])
	}

	// 活动part摘要（读取每个part：essential运行中不执行）
	if g.essential() {
		// 已在上面的表摘要中计数
	} else if r, err := g.execJSON(fmt.Sprintf(
		`SELECT count() AS parts, formatReadableSize(sum(bytes_on_disk)) AS sz
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')`,
		g.sysTable("parts"),
	)); err == nil && r.Rows > 0 {
		p["active_parts"] = parseUInt64(r.Data[0][0])
		var sz string
		_ = json.Unmarshal(r.Data[0][1], &sz)
		p["total_size"] = sz
	}

	// ── 存储 ────────────────────────────────────────────────────────────────

	// 每个聚合只计算一次，并在派生列中引用其别名：
	// CH 25.12的分析器拒绝在同一
	// SELECT中重复的sum(bytes_on_disk) — 第二次出现会被当作嵌套聚合，
	// 抛出ILLEGAL_AGGREGATION。
	p["storage_by_db"] = g.safeQuery("storage_by_db", fmt.Sprintf(
		`SELECT database, count() AS parts, sum(rows) AS rows,
			sum(bytes_on_disk) AS bytes_total,
			formatReadableSize(bytes_total) AS size_human,
			round(if(sum(data_compressed_bytes)>0,
				sum(data_uncompressed_bytes)/sum(data_compressed_bytes), 0), 2) AS compression_ratio
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database ORDER BY bytes_total DESC LIMIT 20`,
		g.sysTable("parts"),
	))

	p["engines_dist"] = g.safeQuery("engines_dist", fmt.Sprintf(
		`SELECT engine, count() AS count
		 FROM %s
		 WHERE database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY engine ORDER BY count DESC LIMIT 15`,
		g.sysTable("tables"),
	))

	// ── 表浏览器 ─────────────────────────────────────────────────────────────

	p["tables_list"] = g.safeQuery("tables_list", g.tablesListSQL())

	// ── 查询日志 ─────────────────────────────────────────────────────────────

	p["query_by_time"] = g.safeQuery("query_by_time", fmt.Sprintf(
		`SELECT toString(toStartOfHour(event_time)) AS time,
				query_kind, count() AS count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY AND type = 'QueryFinish'
		 GROUP BY time, query_kind ORDER BY time`,
		g.sysTable("query_log"),
	))

	p["query_by_kind"] = g.safeQuery("query_by_kind", fmt.Sprintf(
		`SELECT query_kind, count() AS count,
				round(avg(query_duration_ms), 0) AS avg_duration_ms,
				round(avg(memory_usage)/1048576, 2) AS avg_memory_mb
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY AND type = 'QueryFinish'
		 GROUP BY query_kind ORDER BY count DESC`,
		g.sysTable("query_log"),
	))

	// Host facts，直接来自将成为host_info.json的同一Report —
	// 通过JSON往返，使dashboard显示与
	// 文件完全相同的结构，而不是第二个漂移的投影。
	if g.hostInfo != nil {
		g.addHostInfo(p)
	}

	// 日志：仅限有界的Warning+行。text_log需要显式启用（它在
	// OSS中默认关闭），因此表不存在时直接隐藏面板。
	if g.hasTable("text_log") {
		p["text_log"] = g.safeQuery("text_log", g.textLogSQL())
		p["text_log_row_cap"] = textLogRowCap
	}

	p["query_slow"] = g.safeQuery("query_slow", g.querySlowSQL())
	p["query_heavy"] = g.safeQuery("query_heavy", g.queryHeavySQL())

	p["query_by_user"] = g.safeQuery("query_by_user", g.queryByUserSQL())

	// 异常 — 将exception_code作为原始整数返回。在CH 25.12中
	// 分析器根据SELECT别名解析WHERE子句标识符，因此
	// 将toString(exception_code)别名设为AS exception_code会使
	// `WHERE exception_code != 0`将String与UInt8比较 → NO_COMMON_TYPE。
	p["exceptions"] = g.safeQuery("exceptions", fmt.Sprintf(
		`SELECT exception_code,
				any(exception) AS msg, count() AS count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY AND exception_code != 0
		 GROUP BY exception_code ORDER BY count DESC LIMIT 15`,
		g.sysTable("query_log"),
	))

	// ── Part日志 ────────────────────────────────────────────────────────────

	p["part_log_by_time"] = g.safeQuery("part_log_by_time", fmt.Sprintf(
		`SELECT toString(toStartOfDay(event_time)) AS time,
				event_type, count() AS count
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		 GROUP BY time, event_type ORDER BY time`,
		g.sysTable("part_log"),
	))

	p["part_log_by_type"] = g.safeQuery("part_log_by_type", fmt.Sprintf(
		`SELECT event_type, count() AS count,
				formatReadableSize(sum(size_in_bytes)) AS total_size
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		 GROUP BY event_type ORDER BY count DESC`,
		g.sysTable("part_log"),
	))

	// ── 字典 ────────────────────────────────────────────────────────────────

	p["dictionaries"] = g.safeQuery("dictionaries", g.dictionariesSQL())
	// 评审发现：system.dictionaries.source（以及last_exception，它可能
	// 引用连接字符串）在低于23.x的服务器上未经脱敏地进入了此面板。
	// 与JSONL collector和模式关系图使用相同的脱敏器。
	redactDictionaryPanel(p["dictionaries"].([]map[string]interface{}))

	// ── 崩溃日志 ─────────────────────────────────────────────────────────────

	// system.crash_log只有在服务器记录过崩溃后才存在，
	// 因此任何版本的正常实例上都不存在 — 跳过
	// 面板，而不是发出虚假的"表不存在"警告。
	if g.hasTable("crash_log") {
		p["crash_log"] = g.safeQuery("crash_log", fmt.Sprintf(
			`SELECT toString(event_time) AS event_time,
					signal, toString(thread_id) AS thread_id,
					query_id, version
			 FROM %s ORDER BY event_time DESC LIMIT 100`,
			g.sysTable("crash_log"),
		))
	} else {
		p["crash_log"] = []map[string]interface{}{}
	}

	// ── 待处理工作 ───────────────────────────────────────────────────────────

	p["top_tables"] = g.safeQuery("top_tables", g.topTablesSQL())

	// is_killed是在22.8之后加入system.mutations的；仅在
	// 存在时过滤它，否则显示所有进行中的mutation。
	killedFilter := ""
	if g.hasColumn("mutations", "is_killed") {
		killedFilter = "AND is_killed = 0"
	}
	p["mutations"] = g.safeQuery("mutations", fmt.Sprintf(
		`SELECT database, table, mutation_id, command,
				toString(create_time) AS create_time, parts_to_do
		 FROM %s
		 WHERE parts_to_do > 0 %s
		 ORDER BY parts_to_do DESC LIMIT 20`,
		g.sysTable("mutations"), killedFilter,
	))

	// bytes_on_disk在22.11加入system.detached_parts；在更旧的
	// 服务器上回退为仅计数的视图（无大小）。
	detachedSize, detachedOrder := "formatReadableSize(sum(bytes_on_disk)) AS size", "sum(bytes_on_disk)"
	if !g.hasColumn("detached_parts", "bytes_on_disk") {
		detachedSize, detachedOrder = "'n/a' AS size", "count()"
	}
	p["detached"] = g.safeQuery("detached", fmt.Sprintf(
		`SELECT database, table, count() AS count,
				%s,
				arrayStringConcat(groupUniqArray(reason), ', ') AS reasons
		 FROM %s
		 WHERE database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
		   AND %s
		 GROUP BY database, table
		 ORDER BY %s DESC LIMIT 20`,
		detachedSize, g.sysTable("detached_parts"), query.Apply("{detached_scope}", query.Vars{DetachedScope: g.detachedScope}), detachedOrder,
	))

	// 集群节点（仅cloud）。不查询is_active：请求它会使服务器
	// 查询每个Replicated数据库副本的状态（见
	// queries.*/23.5.1.0/system.clusters.sql）。
	if g.mode == "cloud" {
		p["clusters"] = g.safeQuery("clusters",
			`SELECT cluster, host_name,
					toString(shard_num) AS shard, toString(replica_num) AS replica,
					toString(errors_count) AS errors_count
			 FROM system.clusters
			 ORDER BY cluster, shard_num, replica_num`,
		)
	} else {
		p["clusters"] = []map[string]interface{}{}
	}

	p["replication_queue"] = g.safeQuery("replication_queue", fmt.Sprintf(
		`SELECT table, type, toString(is_currently_executing) AS executing, last_exception
		 FROM %s ORDER BY table LIMIT 50`,
		g.sysTable("replication_queue"),
	))

	// ── 副本健康 ─────────────────────────────────────────────────────────────

	// total_replicas / active_replicas会从Keeper读取所询问的每张表，
	// 因此只对system.replicas_problem_tables挑选的最多200张表提供
	// （问题表优先）；其他行中这些字段为空。
	// system.replicas会在读取状态前按database/table过滤，
	// 这正是IN查询廉价的原因。
	p["replicas"] = g.safeQuery("replicas", fmt.Sprintf(
		`SELECT r.database AS database, r.table AS table, is_leader, is_readonly, is_session_expired,
			 future_parts, parts_to_check, queue_size,
			 inserts_in_queue, merges_in_queue, queue_oldest_time,
			 absolute_delay, z.total_replicas AS total_replicas, z.active_replicas AS active_replicas
		 FROM (SELECT database, table, is_leader, is_readonly, is_session_expired,
					  future_parts, parts_to_check, queue_size, inserts_in_queue, merges_in_queue,
					  toString(queue_oldest_time) AS queue_oldest_time, absolute_delay
			   FROM %[1]s) AS r
		 LEFT JOIN (SELECT database, table, toString(total_replicas) AS total_replicas,
						   toString(active_replicas) AS active_replicas
					FROM %[1]s
					WHERE (database, table) IN (
						SELECT database, table FROM %[1]s
						ORDER BY (is_readonly OR is_session_expired OR queue_size > 0 OR absolute_delay > 0
								  OR future_parts > 0 OR parts_to_check > 0) DESC,
								 absolute_delay DESC, database, table
						LIMIT 200)) AS z
		   ON r.database = z.database AND r.table = z.table
		 ORDER BY absolute_delay DESC, is_readonly DESC`,
		g.sysTable("replicas"),
	))

	// ── Keeper健康 ───────────────────────────────────────────────────────────
	//
	// 当metric_log没有行时隐藏该面板（表被禁用，或
	// 不使用Keeper的服务器本来就不会触及这些计数器）。
	if g.hasTable("metric_log") {
		hasWait := g.hasColumn("metric_log", "ProfileEvent_ZooKeeperWaitMicroseconds")
		hasSession := g.hasColumn("metric_log", "CurrentMetric_ZooKeeperSession")
		p["keeper_metric_hourly"] = g.safeQuery("keeper_metric_hourly", g.keeperMetricSQL(hasWait, hasSession))
		// 降级查询会替换为`0 AS wait_us` / `0 AS sessions_*`，
		// 这也是从不联系Keeper的服务器所报告的内容 — 因此
		// 零值无法区分"列不存在"和"确实为零"。
		// 在hasColumn()已知的地方记录是哪种情况，而不是
		// 让读者猜测：否则面板JS会归咎于服务器
		// 版本，而skill会直接从DATA读取这些列
		// （bundle-layout §6），其中0看起来像是丢失了Keeper会话。
		p["keeper_wait_available"] = hasWait
		p["keeper_session_available"] = hasSession
	} else {
		p["keeper_metric_hourly"] = []map[string]interface{}{}
		p["keeper_wait_available"] = false
		p["keeper_session_available"] = false
	}
	// ── 后台池 ───────────────────────────────────────────────────────────────
	//
	// fetch / schedule / merge池中任务的每小时平均值与
	// 池大小的对照（HC-2.13 / 2.14）。池大小为0（设置不可读）时不画
	// 参考线。cloud采集中每个副本的小时数据通过max()折叠
	// 为一个序列：饱和节点即使兄弟节点空闲
	// 也必须显现。
	if g.hasTable("metric_log") {
		p["pools_hourly"] = g.safeQuery("pools_hourly", g.poolsMetricSQL())
	} else {
		p["pools_hourly"] = []map[string]interface{}{}
	}
	p["pool_sizes"] = g.poolSizes()
	// 池面板24小时窗口的服务器端"now"：锚定在
	// 采集时间，与实时告警的now() - 24 h一样，因此停止更新数天的
	// metric_log不会让卡片今天变成琥珀色。
	p["pools_now"] = ""
	if r, err := g.execJSON("SELECT toString(now()) AS now"); err == nil && r.Rows > 0 {
		var v string
		_ = json.Unmarshal(r.Data[0][0], &v)
		p["pools_now"] = v
	}

	useErrorLog := g.hasTable("error_log")
	p["keeper_errors_hourly"] = g.safeQuery("keeper_errors_hourly", g.keeperErrorsSQL(useErrorLog))
	if useErrorLog {
		p["keeper_errors_source"] = "system.error_log — 包含所有线程，包括后台 merge 和 fetch"
	} else {
		p["keeper_errors_source"] = "system.query_log — 仅查询；后台 merge、fetch 和 DDL worker 未计入（system.error_log 需要 24.8+）"
	}
	if g.hasTable("zookeeper_connection") {
		p["keeper_connection"] = g.safeQuery("keeper_connection", g.keeperConnectionSQL())
	} else {
		p["keeper_connection"] = []map[string]interface{}{}
	}

	// ── 磁盘使用 ─────────────────────────────────────────────────────────────

	// system.disks是按副本的，因此cloud模式下会扇出，每个
	// 副本贡献自己的（大体相同的）磁盘列表 — 在3副本服务上测得
	// 11个磁盘 → 33行。没有主机标签时，这些行
	// 会被读成重复。hostName()在产生该行的副本上
	// 求值，与dictionariesSQL完全一样。
	p["disks"] = g.safeQuery("disks", fmt.Sprintf(
		`SELECT hostName() AS hostname,
			 name, path, type,
			 free_space, total_space,
			 formatReadableSize(free_space)       AS free_space_human,
			 formatReadableSize(total_space)      AS total_space_human,
			 if(total_space > 0,
			    round(free_space / total_space * 100, 1),
			    0) AS free_pct
		 FROM %s
		 ORDER BY hostname, total_space DESC`,
		g.sysTable("disks"),
	))

	// ── 服务器错误（累计错误计数器） ──────────────────────────────────────────

	// system.errors保存的是PER-REPLICA计数器，因此硬编码system.errors
	// 只会显示服务节点的错误，完全隐藏其他副本的错误
	// （在3副本Cloud服务上测得）。通过
	// sysTable扇出，再聚合成集群总量：此面板每个错误
	// 渲染为一条柱，因此每个副本发一行会产生
	// 重复的同名柱，而不是一个有意义的计数。
	p["server_errors"] = g.safeQuery("server_errors", fmt.Sprintf(
		// 在子查询中使用NON-colliding别名聚合，然后在外层重命名为
		// `value`。在同一SELECT中将sum(value)别名为AS value
		// 会遮蔽源列，随后分析器会将裸
		// `value`在argMax()/WHERE中解析为聚合 →
		// ILLEGAL_AGGREGATION。与
		// alerts/disk_space_low.yaml使用相同的子查询技巧。
		`SELECT name, code, total AS value, last_error_time, last_error_message
		 FROM (
			 SELECT name, code,
				 sum(value)                                   AS total,
				 toString(max(last_error_time))               AS last_error_time,
				 leftUTF8(argMax(last_error_message, value), 300) AS last_error_message
			 FROM %s
			 GROUP BY name, code
			 HAVING total > 0
		 )
		 ORDER BY value DESC LIMIT 30`,
		g.sysTable("errors"),
	))

	// ── part数过高的表（潜在的"too many parts"） ─────────────────────────────

	p["high_part_count"] = g.safeQuery("high_part_count", fmt.Sprintf(
		`SELECT database, table, partition_id, count() AS parts_count
		 FROM %s
		 WHERE active = 1
		   AND database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
		 GROUP BY database, table, partition_id
		 HAVING parts_count > 100
		 ORDER BY parts_count DESC LIMIT 30`,
		g.sysTable("parts"),
	))

	// ── TTL活动（part_log，最近7天） ─────────────────────────────────────────

	p["ttl_activity"] = g.safeQuery("ttl_activity", fmt.Sprintf(
		`SELECT toString(toStartOfDay(event_time)) AS day,
			 event_type, merge_reason,
			 count()                              AS events,
			 formatReadableSize(sum(size_in_bytes)) AS total_size
		 FROM %s
		 WHERE event_time > now() - INTERVAL 7 DAY
		   AND (merge_reason IN ('TTLDeleteMerge','TTLRecompressMerge')
		        OR event_type = 'RemovePart')
		 GROUP BY day, event_type, merge_reason
		 ORDER BY day`,
		g.sysTable("part_log"),
	))

	// ── 异步插入活动（最近24小时，跳过gov） ─────────────────────────────────

	// system.asynchronous_insert_log在22.10加入；在表不存在的
	// 较旧服务器上跳过该面板。
	if g.mode != "gov" && g.hasTable("asynchronous_insert_log") {
		asyncTable := "system.asynchronous_insert_log"
		if g.mode == "cloud" {
			asyncTable = "clusterAllReplicas(default, merge(system, '^asynchronous_insert_log'))"
		}
		// total_bytes自该表诞生（22.10）就存在；`rows`
		// 列在23.4加入（22.10–23.3中不存在）。始终报告
		// bytes，并在可用时加上rows，使渲染出的列永不
		// 为空（前端两者都显示）；列不可用时'n/a'比字面
		// 0更诚实。
		rowsCol := "'n/a' AS total_rows"
		if g.hasColumn("asynchronous_insert_log", "rows") {
			rowsCol = "toString(sum(rows)) AS total_rows"
		}
		// Queue→flush延迟（毫秒）。我们避免dateDiff('millisecond', …)：
		// 亚秒单位在较旧服务器上会报错（22.12：BAD_ARGUMENTS）。
		// event_time_microseconds / flush_time_microseconds是DateTime64(6)，
		// 自22.10起存在，因此将它们作为浮点数相减可在整个
		// 支持范围内得到真正的亚秒延迟。
		p["async_inserts"] = g.safeQuery("async_inserts", fmt.Sprintf(
			`SELECT toString(toStartOfHour(event_time)) AS hour,
				 status, count() AS flushes,
				 %s,
				 formatReadableSize(sum(bytes)) AS total_bytes,
				 round(avg((toFloat64(flush_time_microseconds) - toFloat64(event_time_microseconds)) * 1000), 0) AS avg_flush_ms
			 FROM %s
			 WHERE event_time > now() - INTERVAL 24 HOUR
			 GROUP BY hour, status ORDER BY hour`,
			rowsCol, asyncTable,
		))
	} else {
		p["async_inserts"] = []map[string]interface{}{}
	}

	// ── 查询分析（可选） ──────────────────────────────────────────────────
	g.collectAnalysis(p)

	return p
}

// addHostInfo嵌入host facts。
func (g *Generator) addHostInfo(p map[string]interface{}) {
	if raw, err := json.Marshal(g.hostInfo); err == nil {
		var hi map[string]interface{}
		if err := json.Unmarshal(raw, &hi); err == nil {
			p["host_info"] = hi
		}
	}
	p["host_checks"] = hostChecks(g.hostInfo)
}

// collectAnalysis运行查询分析SQL文件（与
// AnalysisCollector写入磁盘的相同集合），将JSONCompact输出
// 嵌入dashboard payload，使前端能渲染"Query
// Analysis"部分。未调用WithAnalysis或
// opts.Enabled()为false时不做任何事。
//
// 每个文件的内容在内存中进行模板替换，去掉末尾的
// `FORMAT Native`子句，然后通过safeQuery以
// `FORMAT JSONCompact`发送给ClickHouse。如果文件在替换后仍有
// 未绑定占位符（例如仅提供--normalized-query-hash时的
// 单id文件），则静默跳过 —
// 缺失的key因此不在payload中，前端会隐藏
// 对应卡片。
func (g *Generator) collectAnalysis(p map[string]interface{}) {
	if !g.analysis.Enabled() || g.analysisDir == "" {
		return
	}
	// 零服务器版本排序在所有版本目录之下，因此
	// FindVersionedFiles只会返回根目录文件，dashboard
	// 会在现代服务器上运行22.8基线（第一轮的
	// 回归）。cmd/main.go总会在成功的版本探测后
	// 调用WithServerVersion，因此只有未来的调用者
	// 忘记时才会触发 — 大声警告而不是静默降级。
	if g.serverVersion == (internal.Version{}) {
		fmt.Println("  [dashboard] 警告：服务器版本未知；查询分析面板回退到最旧的（根目录）查询变体")
	}
	vars := query.Vars{
		Mode:                g.mode,
		QueryID:             g.analysis.QueryID,
		NormalizedQueryHash: g.analysis.NormalizedQueryHash,
		From:                g.analysis.From,
		To:                  g.analysis.To,
	}

	// payload key → .sql文件名的映射。key与dashboard模板中的
	// JS取数（DATA.qa_*）匹配。
	files := map[string]string{
		"qa_details":          "query_details.sql",
		"qa_profile":          "profile_events.sql",
		"qa_text_parts":       "text_log_parts.sql",
		"qa_text_full":        "text_log_full.sql",
		"qa_tables":           "tables_for_query.sql",
		"qa_fast_slow":        "fast_slow_query_ids.sql",
		"qa_pe_compare":       "profile_events_compare.sql",
		"qa_by_host":          "hash_by_host.sql",
		"qa_summary":          "hash_summary.sql",
		"qa_failed_over_time": "failed_over_time.sql",
		"qa_failed":           "failed_queries.sql",
		"qa_executions":       "executions_timeline.sql",
	}

	// 完全按照AnalysisCollector的方式解析版本目录覆盖，
	// 使dashboard渲染与归档写入磁盘的相同变体。
	// 直接读取根文件会在现代服务器上运行降级的
	// 22.8基线。（在解析器ERROR时
	// 两条路径故意不同：Collect会中止归档
	// bundle，而dashboard会警告并降级到根文件，
	// 使该部分不完全空白。）
	resolved := map[string]string{} // 基名 → 完整路径
	vf, err := query.FindVersionedFiles(g.analysisDir, g.serverVersion, ".sql")
	if err != nil {
		// 不要在解析器错误（目录不可读、符号链接损坏等）时
		// 静默清空整个Query Analysis部分：警告并
		// 回退到下面的根文件。
		fmt.Printf("  [dashboard] 查询分析文件解析失败：%v（回退到根目录文件）\n", err)
	}
	for _, f := range vf {
		resolved[f.Name] = f.FullPath
	}
	for key, fname := range files {
		path, ok := resolved[fname]
		if !ok {
			// 回退到根文件。对于被此服务器门控的仅版本目录
			// 文件，没有根文件，因此下面的os.ReadFile
			// 会失败，我们跳过它 — 与Collect()一致。
			path = filepath.Join(g.analysisDir, fname)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			// 解析后的路径读取失败（删除/权限竞态）
			// 值得诊断 — 像Collect那样记录日志。仅版本目录的
			// 文件若低于其门控就根本没有根路径，按设计会
			// 静默落到这里。
			if ok {
				fmt.Printf("  [dashboard] %s：读取 %s：%v\n", key, path, err)
			}
			continue
		}
		sql := query.Apply(string(raw), vars)
		if len(query.UnboundPlaceholders(sql)) > 0 {
			continue
		}
		// 去掉末尾的`FORMAT Native`，使safeQuery能通过标准
		// execJSON路径追加JSONCompact。
		sql = stripTrailingFormat(sql)
		if err := query.ValidateQueryContent(sql); err != nil {
			fmt.Printf("  [dashboard] %s：%s 中的分析查询被拦截：%v\n", key, fname, err)
			continue
		}
		rows := g.safeQuery(key, sql)
		// gov模式的纵深防御：dashboard JSON是
		// 支持归档的一部分，因此JS本来会隐藏的任何字段
		// 也应从嵌入的payload中清除。
		// system.query_log中的`query`和`exception`列
		// 包含客户运行的原始文本
		// （引用了gov模式在其他地方哈希的相同表名）；
		// 在嵌入前将它们清空。
		if g.mode == "gov" {
			for _, r := range rows {
				for _, sensitive := range []string{"query", "exception", "sample_query", "sample_exception"} {
					if _, ok := r[sensitive]; ok {
						r[sensitive] = "(redacted in gov mode)"
					}
				}
			}
		}
		p[key] = rows
	}

	p["qa_enabled"] = true
	p["qa_query_id"] = g.analysis.QueryID
	p["qa_hash"] = g.analysis.NormalizedQueryHash
	p["qa_from"] = g.analysis.From.UTC().Format(time.RFC3339)
	p["qa_to"] = g.analysis.To.UTC().Format(time.RFC3339)
	// qa_mode控制焦点查询SQL文本的渲染：在
	// gov模式下我们仍在queries.gov/*.sql中哈希数据库/表名，
	// 但query_log.query保存客户运行的原始SQL，它
	// 引用了相同的名称。在dashboard中暴露它会
	// 破坏哈希。当mode == 'gov'时JS会隐藏该卡片。
	p["qa_mode"] = g.mode
}

// stripTrailingFormat删除末尾的`FORMAT <name>`子句（以及
// 其后的任何空白/分号）。dashboard始终需要
// JSONCompact，由底层execJSON包装器追加。
func stripTrailingFormat(sql string) string {
	t := strings.TrimRight(sql, " \t\r\n;")
	idx := strings.LastIndex(strings.ToUpper(t), "FORMAT")
	if idx == -1 {
		return t
	}
	tail := strings.TrimSpace(t[idx:])
	parts := strings.Fields(tail)
	if len(parts) == 2 && strings.EqualFold(parts[0], "FORMAT") {
		return strings.TrimRight(t[:idx], " \t\r\n")
	}
	return t
}

// buildHTML将payload序列化到HTML模板，从
// CDN加载Chart.js。
func buildHTML(data map[string]interface{}) string {
	return renderHTML(data, false)
}

// renderHTML是带-airgap选择的buildHTML：设置airgap时，
// Chart.js会内联（见airgap.go），页面无需网络。
func renderHTML(data map[string]interface{}, airgap bool) string {
	page := htmlTemplate
	if airgap {
		page = inlineChartJS(page)
	}
	b, _ := json.Marshal(data)
	return strings.Replace(page, "/*DATA*/null", string(b), 1)
}

// htmlTemplate是完整的自包含dashboard页面。
// Chart.js从CDN加载（使用-airgap时内联）；所有ClickHouse数据
// 都内联嵌入。
// themeTokensCSS是Click UI token层 — 调色板原语、浅色和
// 深色语义角色、排版、间距、圆角和阴影 — 只声明一次，
// 由dashboard.html和schema_graph.html共享，使两个页面永远不会
// 在主题上漂移。两个深色作用域都在这里：操作系统偏好（媒体
// 查询，通过:where()实现零特异性）和页面内切换
// （data-cui-theme）。编辑后必须保留的内容见theme_test.go。
const themeTokensCSS = `:root{
  color-scheme:light;
  /* 实际引用的原语 */
  --click-palette-neutral-0:#ffffff;
  --click-palette-neutral-750:#1F1F1C;
  --click-palette-neutral-725:#282828;
  --click-palette-neutral-900:#151515;
  --click-palette-slate-50:#f6f7fa;
  --click-palette-slate-100:#e6e7e9;
  --click-palette-slate-600:#696e79;
  --click-palette-slate-900:#161517;
  --click-palette-brand-300:#FAFF69;

  /* 全局语义token（light.json） */
  --click-global-color-background-default:#ffffff;
  --click-global-color-background-muted:#f6f7fa;
  --click-global-color-text-default:#161517;
  --click-global-color-text-muted:#696e79;
  --click-global-color-stroke-default:#e6e7e9;
  --click-global-color-accent-default:#151515;

  /* 报告级表面角色，以token表达 */
  --surface-page:var(--click-global-color-background-muted);
  --surface-card:var(--click-global-color-background-default);
  --surface-sunken:var(--click-palette-slate-50);
  --surface-hover:var(--click-palette-slate-50);
  --ink:var(--click-global-color-text-default);
  --ink-muted:var(--click-global-color-text-muted);
  --stroke:var(--click-global-color-stroke-default);

  /* 两种主题下都保持深色的界面外框（ClickHouse产品头部） */
  --header-bg:var(--click-palette-neutral-900);
  --header-ink:var(--click-palette-neutral-0);
  --header-logo:var(--click-palette-brand-300);

  /* 状态色阶 — 语义token，保留含义，从不用于数据序列 */
  --status-critical:#f10000;        /* danger.500  */
  --status-warning:#F55A00;         /* warning.500 */
  --status-good:#008A0B;            /* success.700 */
  --status-info:#1D64EC;            /* info.500    */
  --status-neutral:#696e79;         /* slate.600 — "规则本身出错" */
  --status-critical-bg:#ffdddd;     /* danger.50   */
  --status-warning-bg:#FFE2D1;      /* warning.50  */
  --status-good-bg:#E5FFE8;         /* success.50  */
  --status-info-bg:#E7EFFD;         /* info.50     */
  --status-neutral-bg:#f6f7fa;      /* slate.50    */
  --status-critical-ink:#910000;    /* danger.700  */
  --status-warning-ink:#A33C00;     /* warning.700 */
  --status-good-ink:#008A0B;        /* success.700 */
  --status-info-ink:#0D3E9B;        /* info.700    */
  --status-neutral-ink:#53575f;     /* slate.700   */
  --link:#1D64EC;                   /* info.500    */

  /* 排版（primitives.json → typography.font.*） */
  --click-font-regular:"Inter","SF Pro Display",-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Oxygen,Ubuntu,Cantarell,"Open Sans","Helvetica Neue",sans-serif;
  --click-font-mono:"Inconsolata",Consolas,"SFMono Regular",ui-monospace,monospace;
  --click-font-size-0:0.625rem; --click-font-size-1:0.75rem; --click-font-size-2:0.875rem;
  --click-font-size-3:1rem;     --click-font-size-4:1.125rem; --click-font-size-5:1.25rem;
  --click-font-size-6:2rem;
  --click-font-weight-1:400; --click-font-weight-2:500; --click-font-weight-3:600; --click-font-weight-4:700;
  --click-line-height-1:150%; --click-line-height-2:160%;

  /* 间距+边框+阴影（primitives.json → spaces/border/shadow） */
  --click-space-1:0.25rem; --click-space-2:0.5rem;  --click-space-3:0.75rem;
  --click-space-4:1rem;    --click-space-5:1.5rem;  --click-space-6:2rem; --click-space-7:2.5rem;
  --click-radii-1:0.25rem; --click-radii-2:0.5rem;  --click-radii-3:0.75rem; --click-radii-full:9999px;
  --click-border-width-1:1px;
  --click-shadow-1:0 4px 6px -1px rgba(0,0,0,.08), 0 2px 4px -1px rgba(0,0,0,.06);
  --click-shadow-5:0 2px 2px 0 rgba(0,0,0,.03);
  --click-transition-smooth:150ms;
}

/* 深色主题。在两个作用域下都声明，使操作系统设置和页面内
   切换各自在应胜出的地方胜出：:where()让媒体查询块保持零
   特异性，使显式的浅色标记能胜过系统深色。 */
@media (prefers-color-scheme:dark){
  :root:where(:not([data-cui-theme="light"])){
    color-scheme:dark;
    --click-global-color-background-default:#1F1F1C;
    --click-global-color-background-muted:#282828;
    --click-global-color-text-default:#ffffff;
    --click-global-color-text-muted:#b3b6bd;
    --click-global-color-stroke-default:#323232;
    --click-global-color-accent-default:#FAFF69;
    --surface-page:var(--click-palette-neutral-900);
    --surface-card:var(--click-global-color-background-default);
    --surface-sunken:var(--click-global-color-background-muted);
    --surface-hover:var(--click-global-color-background-muted);
    --status-neutral:#808691;
    --status-critical-bg:#300000; --status-warning-bg:#471A00; --status-good-bg:#004206;
    --status-info-bg:#061C47;     --status-neutral-bg:#282828;
    --status-critical-ink:#ffbaba; --status-warning-ink:#FFCBAD; --status-good-ink:#99FFA1;
    --status-info-ink:#A1BEF7;     --status-neutral-ink:#b3b6bd;
    --link:#A1BEF7;                /* info.200 — info.500在这里太暗，难以辨认 */
    --click-shadow-1:0 4px 6px -1px rgba(0,0,0,.5), 0 2px 4px -1px rgba(0,0,0,.4);
    --click-shadow-5:0 2px 2px 0 rgba(0,0,0,.3);
  }
}
:root[data-cui-theme="dark"]{
  color-scheme:dark;
  --click-global-color-background-default:#1F1F1C;
  --click-global-color-background-muted:#282828;
  --click-global-color-text-default:#ffffff;
  --click-global-color-text-muted:#b3b6bd;
  --click-global-color-stroke-default:#323232;
  --click-global-color-accent-default:#FAFF69;
  --surface-page:var(--click-palette-neutral-900);
  --surface-card:var(--click-global-color-background-default);
  --surface-sunken:var(--click-global-color-background-muted);
  --surface-hover:var(--click-global-color-background-muted);
  --status-neutral:#808691;
  --status-critical-bg:#300000; --status-warning-bg:#471A00; --status-good-bg:#004206;
  --status-info-bg:#061C47;     --status-neutral-bg:#282828;
  --status-critical-ink:#ffbaba; --status-warning-ink:#FFCBAD; --status-good-ink:#99FFA1;
  --status-info-ink:#A1BEF7;     --status-neutral-ink:#b3b6bd;
  --link:#A1BEF7;                /* info.200 — info.500在这里太暗，难以辨认 */
  --click-shadow-1:0 4px 6px -1px rgba(0,0,0,.5), 0 2px 4px -1px rgba(0,0,0,.4);
  --click-shadow-5:0 2px 2px 0 rgba(0,0,0,.3);
}
`

// htmlTemplate是完整的自包含dashboard页面，由
// 头部、共享token层和主体组装而成，使token只有
// 一个事实来源（见themeTokensCSS）。
var htmlTemplate = htmlTemplateHead + themeTokensCSS + htmlTemplateTail

const htmlTemplateHead = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>ClickHouse 诊断仪表盘</title>
<script>/* 在首次绘制前写入已保存的主题，使页面永不闪烁 */
try{var _t=localStorage.getItem("chdiag-theme");if(_t)document.documentElement.setAttribute("data-cui-theme",_t);}catch(e){}
/* 侧栏同理：已保存的"collapsed"在布局前生效，而
   窄视口默认折叠，除非读者另有选择 */
try{var _n=localStorage.getItem("chdiag-nav");if(_n==="collapsed"||(!_n&&window.innerWidth<900))document.documentElement.classList.add("nav-collapsed");}catch(e){}
</script>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.3/dist/chart.umd.min.js"></script>
<style>
/* ─────────────────────────────────────────────────────────────────────────────
   Click UI token层。

   Click UI以React库（@clickhouse/click-ui + ClickUIProvider）形式发布，
   本报告无法使用它：本报告是一个从tar包离线打开的
   自包含HTML文件，没有构建步骤。设计系统针对这种情况
   记录的逃生通道就是它的token层 — CSS自定义
   属性，通过<html>上的data-cui-theme设置主题。因此下面的值
   逐字取自ClickHouse/click-ui tokens/themes/{primitives,light,
   dark}.json；每个十六进制值都是真实token，没有一个是凭眼估的。

   选择表面色使图表位于#ffffff（浅色）/ #1F1F1C（深色）上 — 这是下面
   脚本中的分类调色板已验证所依据的两个
   表面。重新运行该验证前不要更改--surface-card。
   ───────────────────────────────────────────────────────────────────────── */
`

const htmlTemplateTail = `
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:var(--click-font-regular);background:var(--surface-page);color:var(--ink);font-size:var(--click-font-size-2);line-height:var(--click-line-height-1)}
/* Header和nav作为一个整体吸附。它们过去分别吸附，
   nav 固定在硬编码的 53px 顶部偏移，该值必须等于 header 高度——
   但并非如此（header实测约74px），因此滚动后header遮住了
   nav顶部21px，其标签被切成两半。改用单个吸顶
   包装器消除了这个常量，而不是重新调整它。--topbar-h在运行时
   测量，驱动scroll-margin和滚动监听，因此其他任何地方
   也不必硬编码此高度。 */
.topbar{position:sticky;top:0;z-index:100}
header{background:var(--header-bg);color:var(--header-ink);padding:var(--click-space-3) var(--click-space-6);display:flex;align-items:center;gap:var(--click-space-3)}
/* 品牌块：第一行是产品名，页面标题作为
   下方更小的第二行，使头部读起来像一个整体。 */
header .brand{display:flex;flex-direction:column;line-height:1.15}
header .logo{font-size:var(--click-font-size-5);font-weight:var(--click-font-weight-4);color:var(--header-logo);letter-spacing:-.5px}
header h1{font-size:var(--click-font-size-1);font-weight:var(--click-font-weight-2);opacity:.8;letter-spacing:.2px}
header .meta{margin-left:auto;text-align:right;font-size:var(--click-font-size-1);color:rgba(255,255,255,.75);line-height:var(--click-line-height-2)}
header .meta .badge{margin:0 var(--click-space-1) 0 0;vertical-align:1px}
#theme-toggle{margin-left:var(--click-space-4);background:transparent;color:var(--header-ink);border:var(--click-border-width-1) solid rgba(255,255,255,.25);border-radius:var(--click-radii-full);padding:var(--click-space-1) var(--click-space-3);font:inherit;font-size:var(--click-font-size-1);cursor:pointer;white-space:nowrap;transition:background var(--click-transition-smooth)}
#theme-toggle:hover{background:rgba(255,255,255,.12)}
/* 分节导航是左侧固定侧栏，位于吸顶
   header下方，header中有一个箭头用于隐藏和显示它。它过去是
   header下方的水平条；在22个分节时，普通宽度下会溢出
   视口且溢出部分横向滚动，因此边缘之外
   的分节实际上无法到达。垂直列表
   随分节数量伸缩。--nav-w是所有偏移所依据的唯一宽度；
   折叠时侧栏滑出，<main>收回
   空间。状态保存在<html>上（class nav-collapsed），
   使<head>中的引导脚本能在首次绘制前写入。 */
:root{--nav-w:180px}
#nav-toggle{display:inline-flex;align-items:center;gap:var(--click-space-2);background:transparent;color:var(--header-ink);border:var(--click-border-width-1) solid rgba(255,255,255,.25);border-radius:var(--click-radii-full);padding:var(--click-space-1) var(--click-space-2);font:inherit;font-size:var(--click-font-size-1);cursor:pointer;white-space:nowrap;flex:none;transition:background var(--click-transition-smooth)}
#nav-toggle:hover{background:rgba(255,255,255,.12)}
#nav-toggle svg{width:18px;height:18px;flex:none;fill:none;stroke:currentColor;stroke-width:2;stroke-linecap:round;stroke-linejoin:round;transition:transform .2s ease}
/* 一个箭头，指向侧栏边缘：显示时朝左（隐藏），隐藏时朝右（显示） */
html.nav-collapsed #nav-toggle svg{transform:scaleX(-1)}
/* 当前分节名称 — 在可见侧栏旁是多余的，
   侧栏隐藏后则是"你在这里" */
#nav-current{display:none;max-width:14em;overflow:hidden;text-overflow:ellipsis;padding-right:var(--click-space-1)}
html.nav-collapsed #nav-current:not(:empty){display:block}
nav{position:fixed;top:var(--topbar-h,74px);left:0;bottom:0;width:var(--nav-w);z-index:90;background:var(--surface-card);border-right:var(--click-border-width-1) solid var(--stroke);display:flex;flex-direction:column;overflow-y:auto;padding:var(--click-space-2) 0 var(--click-space-4);transition:transform .2s ease,visibility .2s}
html.nav-collapsed nav{transform:translateX(-100%);visibility:hidden}
.nav-head{padding:var(--click-space-2) var(--click-space-5);font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);color:var(--ink-muted);text-transform:uppercase;letter-spacing:.5px}
nav a{padding:var(--click-space-2) var(--click-space-5);color:var(--ink-muted);text-decoration:none;font-size:var(--click-font-size-1);font-weight:var(--click-font-weight-2);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;border-left:3px solid transparent;display:block}
nav a:hover{color:var(--ink);background:var(--surface-sunken)}
nav a:focus-visible{outline:2px solid var(--status-info);outline-offset:-2px}
/* 你所在的分节：强调条+字重，使其读起来是状态，
   而不是指针恰好悬停的链接。 */
nav a.active{color:var(--ink);border-left-color:var(--status-info);font-weight:var(--click-font-weight-3)}
/* 低于900px时侧栏会留给图表太少空间，因此
   显示时它覆盖内容而不是推移内容，并且选择
   分节后会再次隐藏它（见切换脚本）。 */
@media (max-width:900px){nav{box-shadow:var(--click-shadow-5);z-index:95}}
@media (prefers-reduced-motion:reduce){nav,main,#nav-toggle svg{transition:none}}
@media print{nav,#nav-toggle{display:none}html:not(.nav-collapsed) main{padding-left:var(--click-space-5);max-width:1600px}}
.badge{display:inline-block;padding:2px var(--click-space-2);border-radius:var(--click-radii-full);font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);text-transform:uppercase;letter-spacing:.5px;margin-top:2px}
.badge-cloud{background:var(--status-info);color:#fff}
.badge-onprem{background:var(--status-good);color:#fff}
.badge-gov{background:#8800CC;color:#fff}
main{max-width:1600px;margin:0 auto;padding:var(--click-space-5) var(--click-space-5) var(--click-space-7);transition:padding-left .2s ease}
html:not(.nav-collapsed) main{padding-left:calc(var(--nav-w) + var(--click-space-5));max-width:calc(1600px + var(--nav-w))}
@media (max-width:900px){html:not(.nav-collapsed) main{padding-left:var(--click-space-5);max-width:1600px}}
section{margin-bottom:var(--click-space-6);scroll-margin-top:calc(var(--topbar-h, 124px) + var(--click-space-2))}
section h2{font-size:var(--click-font-size-3);font-weight:var(--click-font-weight-3);color:var(--ink);margin-bottom:var(--click-space-3);padding-bottom:var(--click-space-2);border-bottom:var(--click-border-width-1) solid var(--stroke);display:flex;align-items:center;gap:var(--click-space-2)}
.stats-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(155px,1fr));gap:var(--click-space-3);margin-bottom:var(--click-space-5)}
.stat-card{background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-2);padding:var(--click-space-4);box-shadow:var(--click-shadow-5)}
.stat-card .val{font-size:var(--click-font-size-6);font-weight:var(--click-font-weight-3);color:var(--ink);line-height:1.15;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}
.stat-card .val.val-md{font-size:var(--click-font-size-5)}
.stat-card .val.val-sm{font-size:var(--click-font-size-4)}
.stat-card .lbl{font-size:var(--click-font-size-1);color:var(--ink-muted);margin-top:var(--click-space-1)}
.charts-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(460px,1fr));gap:var(--click-space-4)}
.chart-card{background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-2);padding:var(--click-space-4);box-shadow:var(--click-shadow-5)}
.chart-card.alert-card{border-color:var(--status-warning)}
.chart-card h3{font-size:var(--click-font-size-2);font-weight:var(--click-font-weight-3);color:var(--ink);margin-bottom:var(--click-space-3)}
.chart-wrap{position:relative}
.h200{height:200px}.h220{height:220px}.h260{height:260px}.h300{height:300px}.h360{height:360px}.h420{height:420px}
table.dt{width:100%;border-collapse:collapse;font-size:var(--click-font-size-1);color:var(--ink)}
table.dt th{background:var(--surface-sunken);color:var(--ink-muted);font-weight:var(--click-font-weight-3);padding:var(--click-space-2) var(--click-space-3);text-align:left;border-bottom:var(--click-border-width-1) solid var(--stroke);white-space:nowrap;cursor:pointer;user-select:none}
table.dt th:hover{color:var(--ink)}
table.dt td{padding:var(--click-space-2) var(--click-space-3);border-bottom:var(--click-border-width-1) solid var(--stroke);vertical-align:top;max-width:400px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
table.dt tr:hover td{background:var(--surface-hover)}
table.dt .num{text-align:right;font-variant-numeric:tabular-nums}
table.dt a{color:var(--link);text-decoration:none}
table.dt a:hover{text-decoration:underline}
.tbl-wrap{overflow-x:auto;border-radius:var(--click-radii-2);border:var(--click-border-width-1) solid var(--stroke);background:var(--surface-card)}
.no-data{color:var(--ink-muted);font-style:italic;padding:var(--click-space-5);text-align:center}
.alert-row td{background:var(--status-warning-bg) !important}
.error-row td{background:var(--status-critical-bg) !important}
.ok-badge,.err-badge,.warn-badge{padding:2px var(--click-space-2);border-radius:var(--click-radii-full);font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);white-space:nowrap}
.ok-badge{background:var(--status-good-bg);color:var(--status-good-ink)}
.err-badge{background:var(--status-critical-bg);color:var(--status-critical-ink)}
.warn-badge{background:var(--status-warning-bg);color:var(--status-warning-ink)}
/* 搜索/过滤栏 */
.filter-bar{display:flex;flex-wrap:wrap;gap:var(--click-space-2);align-items:center;margin-bottom:var(--click-space-3);background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);padding:var(--click-space-3);border-radius:var(--click-radii-2)}
.filter-bar input[type=text],.filter-bar select{padding:var(--click-space-2) var(--click-space-3);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);font:inherit;font-size:var(--click-font-size-1);background:var(--surface-card);color:var(--ink);outline:none}
.filter-bar input[type=text]{flex:1;min-width:200px}
.filter-bar input[type=text]:focus,.filter-bar select:focus{border-color:var(--ink-muted)}
.count-badge{font-size:var(--click-font-size-1);color:var(--ink-muted);white-space:nowrap}
.pagination{display:flex;gap:var(--click-space-2);align-items:center;justify-content:center;padding:var(--click-space-3);font-size:var(--click-font-size-1);color:var(--ink-muted)}
.pagination button{padding:var(--click-space-1) var(--click-space-3);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);background:var(--surface-card);color:var(--ink);cursor:pointer;font:inherit;font-size:var(--click-font-size-1)}
.pagination button:hover{background:var(--surface-hover)}
.pagination .cur{font-weight:var(--click-font-weight-3);color:var(--ink)}
/* 模式关系图标签页 */
.schema-actions button{padding:var(--click-space-2) var(--click-space-4);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);background:var(--click-global-color-accent-default);color:var(--surface-card);cursor:pointer;font:inherit;font-size:var(--click-font-size-1);font-weight:var(--click-font-weight-3)}
.schema-actions button:hover{opacity:.85}
.schema-actions a{color:var(--link);font-size:var(--click-font-size-1)}
#schema-frame{width:100%;height:85vh;border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-2);background:var(--surface-card);display:block}
/* 子节标题 */
#tbl-host-tunables td:last-child,#tbl-host-os td:last-child{white-space:normal;max-width:38ch}
#tbl-host-procs td:last-child{white-space:normal;max-width:60ch;font-family:var(--click-font-mono);font-size:var(--click-font-size-0)}
.host-note{color:var(--ink-muted);font-size:var(--click-font-size-1);margin-top:var(--click-space-2)}
.sql-peek{margin-top:var(--click-space-3);background:var(--surface-sunken);color:var(--ink);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);padding:var(--click-space-3);font-family:var(--click-font-mono);font-size:var(--click-font-size-1);line-height:var(--click-line-height-2);white-space:pre-wrap;word-break:break-word;overflow:auto;max-height:220px}
.sub-title{font-size:var(--click-font-size-2);font-weight:var(--click-font-weight-3);color:var(--ink);margin:var(--click-space-5) 0 var(--click-space-2)}
footer{text-align:center;color:var(--ink-muted);font-size:var(--click-font-size-1);padding:var(--click-space-5);margin-top:var(--click-space-2)}
@media(max-width:700px){.charts-grid{grid-template-columns:1fr}}
/* 告警 */
.alert-ok{background:var(--status-good-bg);border:var(--click-border-width-1) solid var(--status-good);border-left:4px solid var(--status-good);padding:var(--click-space-3) var(--click-space-5);border-radius:var(--click-radii-2);color:var(--status-good-ink);font-weight:var(--click-font-weight-3)}
.alert-skipped{background:var(--surface-sunken);border:var(--click-border-width-1) solid var(--stroke);border-left:4px solid var(--status-neutral);padding:var(--click-space-2) var(--click-space-5);border-radius:var(--click-radii-2);color:var(--ink-muted);font-size:var(--click-font-size-1);margin-top:var(--click-space-2)}
.alert-item{background:var(--surface-card);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-2);padding:var(--click-space-3) var(--click-space-4);margin-bottom:var(--click-space-2);border-left:4px solid var(--status-neutral);box-shadow:var(--click-shadow-5)}
.alert-item.alert-critical{border-left-color:var(--status-critical)}
.alert-item.alert-warning{border-left-color:var(--status-warning)}
.alert-item.alert-info{border-left-color:var(--status-info)}
.alert-item.alert-error{border-left-color:var(--status-neutral)}
.alert-header{display:flex;align-items:center;gap:var(--click-space-2);margin-bottom:var(--click-space-1);flex-wrap:wrap}
.alert-title{font-weight:var(--click-font-weight-3);font-size:var(--click-font-size-2)}
/* 状态从不只靠颜色表达：每个严重级别都配有图标+标签。 */
.alert-icon{font-style:normal;line-height:1}
.alert-count{font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);padding:2px var(--click-space-2);border-radius:var(--click-radii-full)}
.alert-count.badge-critical{background:var(--status-critical-bg);color:var(--status-critical-ink)}
.alert-count.badge-warning{background:var(--status-warning-bg);color:var(--status-warning-ink)}
.alert-count.badge-info{background:var(--status-info-bg);color:var(--status-info-ink)}
.alert-count.badge-error{background:var(--status-neutral-bg);color:var(--status-neutral-ink)}
.alert-desc{color:var(--ink-muted);font-size:var(--click-font-size-1);margin-bottom:var(--click-space-2);line-height:var(--click-line-height-2)}
.alert-messages{padding-left:18px;margin:0}
.alert-messages li{font-size:var(--click-font-size-1);color:var(--ink);margin:3px 0;font-family:var(--click-font-mono);word-break:break-word;white-space:pre-wrap}
.collect-banner{margin:0 0 var(--click-space-3,12px);padding:10px 14px;border-radius:8px;border:1px solid var(--stroke);background:var(--surface-card);color:var(--ink);font-size:var(--click-font-size-1)}
.collect-banner.essential{border-color:var(--status-warning-ink,#b58105);background:var(--status-warning-bg)}
.collect-banner b{font-weight:600}
.collect-banner .muted{color:var(--ink-muted)}
.alert-err-msg{font-size:var(--click-font-size-1);color:var(--ink-muted);margin-top:var(--click-space-1);font-style:italic}
/* 告警内部的折叠披露。ClickHouse异常会内联携带堆栈跟踪
   — 实测一条在15行中有1764个字符，其中216个是
   消息 — 而一条规则可能返回数十行，因此冗长部分放在
   <details>后面，告警列表保持可扫读。 */
.alert-more{margin:2px 0 0}
.alert-more>summary{cursor:pointer;color:var(--ink-muted);font-size:var(--click-font-size-0);font-family:var(--click-font-regular);font-style:normal;list-style:none;display:inline-flex;align-items:center;gap:4px;user-select:none}
.alert-more>summary::-webkit-details-marker{display:none}
.alert-more>summary::before{content:'\25B8';display:inline-block;transition:transform .12s ease}
.alert-more[open]>summary::before{transform:rotate(90deg)}
.alert-more>summary:hover{color:var(--ink);text-decoration:underline}
.alert-more>summary:focus-visible{outline:2px solid var(--status-info);outline-offset:2px;border-radius:var(--click-radii-1)}
.alert-full{background:var(--surface-sunken);border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);padding:var(--click-space-2);margin:var(--click-space-1) 0 var(--click-space-2);font-family:var(--click-font-mono);font-size:var(--click-font-size-0);line-height:var(--click-line-height-1);color:var(--ink-muted);white-space:pre-wrap;word-break:break-word;max-height:360px;overflow:auto}
.alert-desc-rest{margin-top:var(--click-space-1)}
/* 用于在客户自己的集群上运行所显示告警的SQL —
   折叠在一行摘要下；.alert-full为它提供堆栈跟踪所用的
   等宽、可滚动、限高盒子。 */
.alert-q{margin:var(--click-space-1) 0 0}
.alert-q-title{font-size:var(--click-font-size-0);color:var(--ink-muted);display:flex;align-items:center;gap:var(--click-space-1)}
.alert-copy{font-size:var(--click-font-size-0);padding:0 6px;border:var(--click-border-width-1) solid var(--stroke);border-radius:var(--click-radii-1);background:var(--surface-sunken);color:var(--ink-muted);cursor:pointer}
.alert-copy:hover{color:var(--ink)}
.alert-sql{white-space:pre;word-break:normal}
.alert-tags{display:flex;gap:var(--click-space-1);flex-wrap:wrap;margin-top:var(--click-space-2)}
.alert-tag{background:var(--surface-sunken);border:var(--click-border-width-1) solid var(--stroke);color:var(--ink-muted);border-radius:var(--click-radii-full);padding:1px var(--click-space-2);font-size:var(--click-font-size-0)}
.alert-summary-bar{display:flex;gap:var(--click-space-2);flex-wrap:wrap;margin-bottom:var(--click-space-3)}
.alert-summary-chip{padding:var(--click-space-1) var(--click-space-3);border-radius:var(--click-radii-full);font-size:var(--click-font-size-1);font-weight:var(--click-font-weight-3);display:inline-flex;align-items:center;gap:var(--click-space-1)}
.chip-critical{background:var(--status-critical-bg);color:var(--status-critical-ink)}
.chip-error{background:var(--status-neutral-bg);color:var(--status-neutral-ink)}
.chip-warning{background:var(--status-warning-bg);color:var(--status-warning-ink)}
.chip-info{background:var(--status-info-bg);color:var(--status-info-ink)}
</style>
</head>
<body>

<div class="topbar">
<header>
  <button id="nav-toggle" type="button" aria-label="隐藏分节侧栏" aria-controls="main-nav" aria-expanded="true">
    <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 6l-6 6 6 6"/></svg>
    <span id="nav-current"></span>
  </button>
  <div class="brand">
    <div class="logo">ClickHouse</div>
    <h1>诊断仪表盘</h1>
  </div>
  <div class="meta" id="hdr-meta"></div>
  <button id="theme-toggle" type="button" aria-label="切换配色主题"></button>
</header>
</div><!-- .topbar -->

<!-- 分节侧栏。刻意放在.topbar之外：该条带是吸顶的，
     并为--topbar-h测量，侧栏根据该测量结果定位在其下方，
     而不是它的一部分。 -->
<nav id="main-nav" aria-label="分节">
  <div class="nav-head">分节</div>
  <a href="#sec-alerts" id="nav-alerts">告警</a>
  <a href="#sec-qa" id="nav-qa" style="display:none">查询分析</a>
  <a href="#sec-overview">概览</a>
  <a href="#sec-storage">存储</a>
  <a href="#sec-tables">表</a>
  <a href="#sec-schema" id="nav-schema" style="display:none">模式关系图</a>
  <a href="#sec-queries">查询活动</a>
  <a href="#sec-deepdive">查询深入分析</a>
  <a href="#sec-exceptions">异常</a>
  <a href="#sec-partlog">Part 日志</a>
  <a href="#sec-dicts">字典</a>
  <a href="#sec-crashlog" id="nav-crashlog" style="display:none">崩溃日志</a>
  <a href="#sec-pending">待处理工作</a>
  <a href="#sec-replication">复制</a>
  <a href="#sec-replicas" id="nav-replicas" style="display:none">副本</a>
  <a href="#sec-keeper" id="nav-keeper" style="display:none">Keeper</a>
  <a href="#sec-clusters" id="nav-clusters" style="display:none">集群</a>
  <a href="#sec-disks">磁盘</a>
  <a href="#sec-logs" id="nav-logs" style="display:none">日志</a>
  <a href="#sec-files" id="nav-files" style="display:none">已采集文件</a>
  <a href="#sec-server-errors">服务器错误</a>
  <a href="#sec-async-inserts" id="nav-async-inserts" style="display:none">异步插入</a>
</nav>

<main>

<!-- ── 告警 ── -->
<section id="sec-alerts">
  <div id="collect-banner" class="collect-banner" style="display:none"></div>
  <h2>🚨 告警摘要</h2>
  <div id="alerts-summary-bar"></div>
  <div id="alerts-panel"></div>
</section>

<!-- ── 查询分析 ── -->
<section id="sec-qa" style="display:none">
  <h2>🔍 查询分析</h2>
  <div id="qa-focus" class="alert-item" style="border-left-color:var(--status-info);margin-bottom:var(--click-space-4)"></div>

  <!-- 查询文本卡片 — gov模式下隐藏 -->
  <div id="qa-query-card" class="chart-card" style="display:none;margin-bottom:18px">
    <h3>焦点查询 — SQL 文本</h3>
    <pre id="qa-query-text" style="background:var(--surface-sunken);color:var(--ink);border:var(--click-border-width-1) solid var(--stroke);padding:var(--click-space-3);border-radius:var(--click-radii-1);overflow:auto;max-height:280px;font-family:var(--click-font-mono);font-size:var(--click-font-size-1);white-space:pre-wrap;line-height:var(--click-line-height-2)"></pre>
  </div>

  <!-- 每次执行的散点图 — 每次单独查询执行一个点。
       五个图表共享此形状；每个从qa_executions数组中选取不同指标，
       并使用相同的自适应单位+颜色
       逻辑（绿色成功，红色叉失败，提示框含query_id）。 -->
  <div class="charts-grid">
    <div class="chart-card">
      <h3>每次执行耗时</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-scatter"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>每次执行内存占用</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-mem"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>每次执行用户 CPU</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-cpu"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>每次执行读取行数</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-rrows"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>每次执行读取字节</h3>
      <div class="chart-wrap h300"><canvas id="chart-qa-rbytes"></canvas></div>
    </div>
  </div>

  <!-- 计数图表 — 按MINUTE分桶。这里不适用按执行
       （每个点的计数都是"1"没有信息量）；分钟是最细的
       有用粒度。 -->
  <div class="charts-grid" style="margin-top:18px">
    <div class="chart-card">
      <h3>每分钟执行次数</h3>
      <div class="chart-wrap h260"><canvas id="chart-qa-execs"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>每分钟失败查询数（按错误类型）</h3>
      <div class="chart-wrap h260"><canvas id="chart-qa-failed"></canvas></div>
    </div>
  </div>

  <!-- 单次执行图表 -->
  <div class="charts-grid" style="margin-top:18px">
    <div class="chart-card">
      <h3>前 30 个 ProfileEvents（最慢执行）</h3>
      <div class="chart-wrap h420"><canvas id="chart-qa-profile"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>快 vs 慢 — ProfileEvents（按 |delta| 前 30）</h3>
      <div class="chart-wrap h420"><canvas id="chart-qa-compare"></canvas></div>
    </div>
  </div>

  <div class="sub-title">各主机分布（hash）</div>
  <div class="tbl-wrap"><div id="tbl-qa-host"></div></div>
  <div class="sub-title" style="margin-top:18px">失败查询 — 按表 × 错误类型细分</div>
  <div class="tbl-wrap"><div id="tbl-qa-failed"></div></div>
  <div class="sub-title" style="margin-top:18px">焦点查询引用的表</div>
  <div class="tbl-wrap"><div id="tbl-qa-tables"></div></div>
  <div class="sub-title" style="margin-top:18px">扫描的 part / mark / stream（text_log，最慢执行）</div>
  <div class="tbl-wrap"><div id="tbl-qa-parts"></div></div>
  <div class="sub-title" style="margin-top:18px">最慢执行的完整 text_log</div>
  <div class="tbl-wrap" style="max-height:480px;overflow-y:auto"><div id="tbl-qa-textlog"></div></div>
</section>

<!-- ── 概览 ── -->
<section id="sec-overview">
  <h2>📈 概览</h2>
  <div class="stats-grid" id="stats-grid"></div>

  <!-- Host facts：与host_info.json相同的内容，显示在system表
       报告的内容旁，使OS上下文无需第二个文件。 -->
  <div id="host-block" style="display:none">
    <div class="sub-title">主机 &mdash; 操作系统、内核与硬件</div>
    <div class="charts-grid">
      <div class="chart-card">
        <h3>机器</h3>
        <div class="tbl-wrap"><div id="tbl-host-os"></div></div>
      </div>
      <div class="chart-card">
        <h3>ClickHouse 相关可调参数</h3>
        <div class="tbl-wrap"><div id="tbl-host-tunables"></div></div>
        <p class="host-note">只标记 ClickHouse 自身在启动时检查的设置；其余仅作为事实报告。</p>
      </div>
    </div>
    <div class="sub-title">按常驻内存排序的进程</div>
    <div class="tbl-wrap"><div id="tbl-host-procs"></div></div>
    <div id="host-notes"></div>
  </div>
</section>

<!-- ── 存储 ── -->
<section id="sec-storage">
  <h2>📦 存储</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>各数据库数据大小</h3>
      <div class="chart-wrap h360"><canvas id="chart-storage-db"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>表引擎分布</h3>
      <div class="chart-wrap h260"><canvas id="chart-engines"></canvas></div>
    </div>
  </div>
  <div class="sub-title">按大小排序的前 20 张表</div>
  <div class="tbl-wrap"><div id="tbl-top-tables"></div></div>
</section>

<!-- ── 表浏览器 ── -->
<section id="sec-tables">
  <h2>📋 表浏览器</h2>
  <div class="filter-bar">
    <input type="text" id="tbl-search" placeholder="按名称、引擎、键搜索…" oninput="tablesFilter()">
    <select id="tbl-db-filter" onchange="tablesFilter()"><option value="">所有数据库</option></select>
    <select id="tbl-engine-filter" onchange="tablesFilter()"><option value="">所有引擎</option></select>
    <span class="count-badge" id="tbl-count"></span>
  </div>
  <div class="tbl-wrap"><div id="tbl-explorer"></div></div>
  <div class="pagination" id="tbl-pagination"></div>
</section>

<!-- ── 模式关系图 ── -->
<!-- 关系图位于本页旁的schema_graph.html中，仅在请求时加载到
     框架中：设置iframe.src是一次导航，file://允许这样做，
     而获取同级文件的fetch()/XHR会被阻止。在打开标签页之前
     不会为关系图付出任何代价。 -->
<section id="sec-schema" style="display:none">
  <h2>🕸 模式关系图</h2>
  <p class="host-note" id="schema-note"></p>
  <div class="filter-bar schema-actions">
    <button type="button" id="schema-load">加载关系图</button>
    <a id="schema-open" href="schema_graph.html" target="_blank" rel="noopener">在新标签页打开 ↗</a>
    <span class="count-badge" id="schema-count"></span>
  </div>
  <div id="schema-frame-wrap" style="display:none">
    <iframe id="schema-frame" title="表依赖关系图" loading="eager"></iframe>
  </div>
  <p class="host-note">表、materialized view、字典和 Distributed 表，以及数据流经的边。节点按引擎着色；点击节点可查看其键、列和 CREATE 语句（引擎参数中的凭据显示为 <code>[HIDDEN]</code>）。该页面写在本页旁边 — 如果框架一直空白，请打开完整的 bundle 目录，而不是复制出来的 dashboard.html。</p>
</section>

<!-- ── 查询活动 ── -->
<section id="sec-queries">
  <h2>📊 查询活动（<span data-win="7d">最近 7 天</span>）</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>每小时查询数（按类型）</h3>
      <div class="chart-wrap h260"><canvas id="chart-query-time"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>查询数（按类型）</h3>
      <div class="chart-wrap h200"><canvas id="chart-query-kind"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>平均耗时（按类型，ms）</h3>
      <div class="chart-wrap h200"><canvas id="chart-query-duration"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>平均内存（按类型，MB）</h3>
      <div class="chart-wrap h200"><canvas id="chart-query-memory"></canvas></div>
    </div>
  </div>
</section>

<!-- ── 查询深入分析 ── -->
<section id="sec-deepdive">
  <h2>🔍 查询深入分析（<span data-win="7d">最近 7 天</span>）</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>最慢的 20 个查询模式（平均耗时 ms）</h3>
      <div class="chart-wrap h420"><canvas id="chart-slow-queries"></canvas></div>
      <pre class="sql-peek" id="peek-slow-queries" style="display:none"></pre>
    </div>
    <div class="chart-card">
      <h3>读取量最大的 20 个查询（平均 MB/查询）</h3>
      <div class="chart-wrap h420"><canvas id="chart-heavy-reads"></canvas></div>
      <pre class="sql-peek" id="peek-heavy-reads" style="display:none"></pre>
    </div>
    <div class="chart-card" style="grid-column:1/-1">
      <h3>用户活动 — 执行次数与错误（<span data-win="7d">最近 7 天</span>）</h3>
      <div class="chart-wrap h200"><canvas id="chart-user-activity"></canvas></div>
    </div>
  </div>
  <div class="sub-title">慢查询详情</div>
  <div class="tbl-wrap"><div id="tbl-slow-queries"></div></div>
  <div class="sub-title" style="margin-top:18px">用户摘要</div>
  <div class="tbl-wrap"><div id="tbl-user-summary"></div></div>
</section>

<!-- ── 异常 ── -->
<section id="sec-exceptions">
  <h2>⚠️ 异常（<span data-win="7d">最近 7 天</span>）</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>异常码 Top</h3>
      <div class="chart-wrap h260"><canvas id="chart-exceptions"></canvas></div>
    </div>
    <div class="chart-card" style="display:flex;flex-direction:column">
      <h3>异常详情</h3>
      <div class="tbl-wrap" style="max-height:320px;overflow-y:auto"><div id="tbl-exceptions"></div></div>
    </div>
  </div>
</section>

<!-- ── Part日志 ── -->
<section id="sec-partlog">
  <h2>🔧 Part 日志事件（<span data-win="3d">最近 7 天</span>）</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>每日 Part 事件数（按类型）</h3>
      <div class="chart-wrap h260"><canvas id="chart-partlog-time"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Part 事件类型分布</h3>
      <div class="chart-wrap h260"><canvas id="chart-partlog-type"></canvas></div>
    </div>
  </div>
</section>

<!-- ── 字典 ── -->
<section id="sec-dicts">
  <h2>📖 字典</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>状态分布</h3>
      <div class="chart-wrap h200"><canvas id="chart-dict-status"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>各字典分配字节数</h3>
      <div class="chart-wrap h300"><canvas id="chart-dict-bytes"></canvas></div>
    </div>
    <div class="chart-card" style="grid-column:1/-1">
      <h3>生存时间配置 — 最小与最大（秒）</h3>
      <div class="chart-wrap h200"><canvas id="chart-dict-lifetime"></canvas></div>
    </div>
  </div>
  <div class="sub-title">字典详情</div>
  <div class="tbl-wrap"><div id="tbl-dicts"></div></div>
</section>

<!-- ── 崩溃日志 ── -->
<section id="sec-crashlog" style="display:none">
  <h2>💥 崩溃日志</h2>
  <div class="tbl-wrap"><div id="tbl-crash-log"></div></div>
</section>

<!-- ── 待处理工作 ── -->
<section id="sec-pending">
  <h2>⏳ 待处理操作</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>待处理 mutation</h3>
      <div class="tbl-wrap"><div id="tbl-mutations"></div></div>
    </div>
    <div class="chart-card">
      <h3>分离 part</h3>
      <div class="tbl-wrap"><div id="tbl-detached"></div></div>
    </div>
  </div>
</section>

<!-- ── 复制队列 ── -->
<section id="sec-replication">
  <h2>🔄 复制队列</h2>
  <div class="tbl-wrap"><div id="tbl-replication"></div></div>
  <div class="sub-title">后台池（<span data-win="7d">最近 7 天</span>，每小时平均任务数与池大小）</div>
  <p class="host-note" id="pools-note">每个池在达到容量上限时的失败方式各不相同。<b>Fetch</b>：队列本身没有队列 — 取任务时找不到空闲槽位会被静默丢弃并稍后重试；在 SharedMergeTree 副本上，这表现为 parts 传播滞后（告警 <code>fetch_pool_saturated</code>）。<b>Schedule</b>：周期性任务不会丢失，但会延迟 — parts 集合更新、复制队列处理和清理都在等待槽位；数万张复制表会最先占满它（<code>schedule_pool_saturated</code>）。<b>Merge / mutation</b>：merge 等待、part 不断累积 — 典型的 part 过多堆积（HC-2.7）。虚线 = 池大小；曲线是最差副本的每小时平均值。</p>
  <div class="charts-grid" id="pools-charts">
    <div class="chart-card">
      <h3>Fetch 池（BackgroundFetchesPoolTask）</h3>
      <div class="chart-wrap h260"><canvas id="chart-pool-fetch"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Schedule 池（BackgroundSchedulePoolTask）</h3>
      <div class="chart-wrap h260"><canvas id="chart-pool-schedule"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>Merge / mutation 池（BackgroundMergesAndMutationsPoolTask）</h3>
      <div class="chart-wrap h260"><canvas id="chart-pool-merge"></canvas></div>
    </div>
  </div>
</section>

<!-- ── 集群 ── -->
<section id="sec-clusters" style="display:none">
  <h2>🌐 集群节点</h2>
  <div class="tbl-wrap"><div id="tbl-clusters"></div></div>
</section>

<!-- ── 副本健康 ── -->
<section id="sec-replicas" style="display:none">
  <h2>🔁 副本健康</h2>
  <div class="charts-grid">
    <div class="chart-card">
      <h3>复制延迟分布</h3>
      <div class="chart-wrap h260"><canvas id="chart-replica-delay"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>各表队列大小（前 15）</h3>
      <div class="chart-wrap h260"><canvas id="chart-replica-queue"></canvas></div>
    </div>
  </div>
  <div class="sub-title">副本详情</div>
  <p class="host-note" id="replicas-scope"></p>
  <div class="tbl-wrap"><div id="tbl-replicas"></div></div>
  <div class="pagination" id="replicas-pagination"></div>
</section>

<!-- ── Keeper健康 ── -->
<section id="sec-keeper" style="display:none">
  <h2>🔑 Keeper 健康（<span data-win="7d">最近 7 天</span>）</h2>
  <div id="keeper-health-body" style="display:none">
  <p class="host-note">每小时有两个计数器决定判定：<b>事务数</b>（Keeper 为此服务器完成的工作）和<b>硬件异常数</b>（连接丢失、会话过期、操作超时）。流量保持时异常超过 1000 次记为 <b>blip</b> — 会话丢失后重新建立。流量降到 7 天中位数一半以下时出现异常，说明 Keeper 对此服务器<b>不可用</b>：插入以 999/319 失败，merge 不再调度，DDL 停滞，随后出现 TOO_MANY_PARTS。</p>
  <div class="tbl-wrap"><div id="tbl-keeper-verdict"></div></div>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>每小时 Keeper 事务数（已完成工作）</h3>
      <div class="chart-wrap h260"><canvas id="chart-keeper-traffic"></canvas></div>
    </div>
    <div class="chart-card" style="grid-column:1/-1">
      <h3>每小时 Keeper 硬件异常数（连接丢失、会话过期、超时）— 柱条按判定着色</h3>
      <div class="chart-wrap h260"><canvas id="chart-keeper-exceptions"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>每小时 Keeper 请求平均延迟（ms）</h3>
      <p class="host-note">ZooKeeperWaitMicroseconds / ZooKeeperTransactions。异常出现<i>之前</i>的几个小时里延迟上升，说明 Keeper 先被压满了。</p>
      <div class="chart-wrap h260"><canvas id="chart-keeper-latency"></canvas></div>
    </div>
    <div class="chart-card">
      <h3>每小时依赖 Keeper 的错误数</h3>
      <p class="host-note" id="keeper-errors-source"></p>
      <div class="chart-wrap h260"><canvas id="chart-keeper-errors"></canvas></div>
    </div>
  </div>
  </div>
  <div id="keeper-members-block" style="display:none">
    <div class="sub-title">Keeper 成员 — 各成员自报信息（keeper/&lt;member&gt;.txt）</div>
    <p class="host-note">来自各成员 four-letter 回复的角色、Raft 日志位置和快照设置。⛔ / ⚠ 标记遵循分析 skill 的检查（HC-3.18–3.20；悬停查看原因）：日志末尾早于 leader 最早保留条目的成员只能通过完整快照重新加入；超过 2 GiB 的快照若未分块发送（<code>snapshot_transfer_chunk_size = 0</code>）会失败 — 在成员落后之前设置该参数及 <code>nuraft_snapshot_sync_ctx_timeout_ms</code>（P-61）。</p>
    <div id="keeper-leaderless"></div>
    <div class="tbl-wrap"><div id="tbl-keeper-members"></div></div>
  </div>
  <div id="keeper-connections-block" style="display:none">
  <div class="sub-title">Keeper 连接（system.zookeeper_connection）</div>
  <div class="tbl-wrap"><div id="tbl-keeper-connection"></div></div>
  </div>
</section>

<!-- ── 磁盘使用 ── -->
<section id="sec-disks">
  <h2>💾 磁盘使用</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>各磁盘可用与已用空间</h3>
      <div class="chart-wrap h220"><canvas id="chart-disk-usage"></canvas></div>
    </div>
  </div>
  <div class="sub-title">磁盘详情</div>
  <div class="tbl-wrap"><div id="tbl-disks"></div></div>
</section>

<!-- ── 日志 ── -->
<section id="sec-logs" style="display:none">
  <h2>📜 日志</h2>
  <p class="host-note" id="logs-scope"></p>
  <div class="filter-bar">
    <input type="text" id="log-search" placeholder="搜索消息或 logger…" oninput="logsFilter()">
    <select id="log-level-filter" onchange="logsFilter()"><option value="">所有级别</option></select>
    <span class="count-badge" id="log-count"></span>
  </div>
  <div class="tbl-wrap"><div id="tbl-logs"></div></div>
  <div class="pagination" id="log-pagination"></div>
</section>

<!-- ── 已采集文件 ── -->
<section id="sec-files" style="display:none">
  <h2>📁 已采集文件</h2>
  <p class="host-note" id="files-scope"></p>
  <div class="filter-bar">
    <input type="text" id="file-search" placeholder="按名称筛选…" oninput="filesFilter()">
    <select id="file-group-filter" onchange="filesFilter()"><option value="">所有文件夹</option></select>
    <span class="count-badge" id="file-count"></span>
  </div>
  <div class="tbl-wrap"><div id="tbl-files"></div></div>
  <p class="host-note">链接会从本页旁边的解压 bundle 中打开文件。这里不内嵌任何内容 — 仅原始服务器日志每个就以最多 50&nbsp;MiB 尾部拷贝，内嵌会让本页无法打开。</p>
</section>

<!-- ── 服务器错误 ── -->
<section id="sec-server-errors">
  <h2>🛑 服务器错误计数器</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>前 20 个错误码（自上次重启以来累计）</h3>
      <div class="chart-wrap h300"><canvas id="chart-server-errors"></canvas></div>
    </div>
  </div>
  <div class="sub-title">part 数过多的分区（&gt;100 个 part — 存在 code 252 TOO_MANY_PARTS 风险）</div>
  <div class="tbl-wrap"><div id="tbl-high-parts"></div></div>
  <div class="sub-title" style="margin-top:18px">TTL 活动（<span data-win="3d">最近 7 天</span>）</div>
  <div class="tbl-wrap"><div id="tbl-ttl-activity"></div></div>
</section>

<!-- ── 异步插入 ── -->
<section id="sec-async-inserts" style="display:none">
  <h2>⚡ 异步插入活动（最近 24 小时）</h2>
  <div class="charts-grid">
    <div class="chart-card" style="grid-column:1/-1">
      <h3>每小时异步 flush 次数（按状态）</h3>
      <div class="chart-wrap h260"><canvas id="chart-async-inserts"></canvas></div>
    </div>
  </div>
  <div class="tbl-wrap"><div id="tbl-async-inserts"></div></div>
</section>

</main>
<footer>由 ClickHouse 诊断工具生成 &mdash; 需要 Chart.js CDN</footer>

<script>
const DATA = /*DATA*/null;

// ── 调色板 ────────────────────────────────────────────────────────────────
//
// 五个分类槽位，每个十六进制值都是一个Click UI token。该集合刻意
// 与模式无关 — 同样五种在浅色卡片（#ffffff）和深色
// 卡片（#1F1F1C）上都可用 — 这使主题切换只需Chart.update()，
// 而不是拆除重建。
//
// 经过验证，而非凭眼估（dataviz validate_palette.js，两种模式）：
//   亮度带 PASS · 色度下限 PASS · 对比度>=3:1 PASS
//   相邻CVD dE 20.1（目标>=8） · 正常视觉dE 31.3（下限>=15）
// 全配对形式（散点、环形、小倍数）只对
// 前三个槽位通过门槛，因此这些形式超过三个就折叠 — 见foldSeries。
//
// 不要扩展此数组。第六个生成的色相在CVD下与
// 现有槽位无法区分；尾部改为折叠进OTHER。槽位按
// 实体索引分配而从不按排名，因此过滤不会重绘
// 幸存者。
const C = [
  '#089B83',  // 1 青绿     — teal.600
  '#AA00FF',  // 2 紫罗兰   — violet.500
  '#B28800',  // 3 琥珀    — sunrise.600
  '#CC0099',  // 4 品红  — fuchsia.600
  '#959900'   // 5 橄榄    — brand.600
];
const OTHER = '#808691';        // slate.500 — 弱化/折叠尾部的灰色
const ALL_PAIRS_CAP = 3;        // scatter/donut/small-multiple上限

// 有序分桶（延迟区间）的序数色阶。单色相、单调亮度，
// 按模式分级，使浅色端在自身表面上仍能达到2:1 —
// 已用validate_palette.js --ordinal在两种模式下验证。这是唯一与模式
// 相关的调色板，因此主题切换时会重绘。
const ORDINAL = {
  light:['#6D9BF3','#437EEF','#1D64EC','#104EC6','#0D3E9B'],  // info.300→700
  dark: ['#A1BEF7','#6D9BF3','#437EEF','#1D64EC','#104EC6']   // info.200→600
};

// 状态色阶。保留含义 — 从不用作数据序列颜色，也从不只靠
// 色相表达（每次使用都配有图标+标签）。
const STATUS = {
  critical:'#f10000',  // danger.500
  warning: '#F55A00',  // warning.500
  good:    '#008A0B',  // success.700
  info:    '#1D64EC',  // info.500
  neutral: '#808691'   // slate.500
};
const alpha = (h,a) => h + Math.round(a*255).toString(16).padStart(2,'0');
const DICT_STATUS_COLOR = {
  LOADED:STATUS.good, FAILED:STATUS.critical, LOADING:STATUS.warning,
  NOT_LOADED:STATUS.neutral, UNKNOWN:STATUS.info
};

// 按索引取槽位 — 折叠而不是循环。
const slot = (i) => i < C.length ? C[i] : OTHER;

// 按权重保留前'cap'个类别，将其余折叠进"Other"，使
// 图表永远不需要第六个色相。返回幸存类别的顺序。
function foldSeries(cats, weight, cap){
  cap = cap || C.length;
  if(cats.length <= cap) return cats;
  const keep = [...cats].sort((a,b)=>(weight(b)||0)-(weight(a)||0)).slice(0,cap-1);
  const ordered = cats.filter(c=>keep.includes(c));
  ordered.push('Other');
  return ordered;
}

// ── 主题 ──────────────────────────────────────────────────────────────────
const themeVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
const CHARTS = [];
function mkChart(el, cfg){
  if(!el) return null;
  const c = new Chart(el, cfg);
  CHARTS.push(c);
  return c;
}

// Chart.js在绘制时读取这些值，因此重新设置坐标轴、刻度、图例和
// 网格的主题只需替换默认值加一次update — 没有图表被重建。
function applyChartTheme(){
  const ink=themeVar('--ink-muted'), stroke=themeVar('--stroke');
  Chart.defaults.color        = ink;
  Chart.defaults.borderColor  = stroke;
  Chart.defaults.font.family  = themeVar('--click-font-regular');
  Chart.defaults.font.size    = 11;
  // 网格和坐标轴线是界面外框，不是数据 — 它们采用stroke token
  // 并位于标记之后。否则Chart.js会回退到defaults.color，
  // 在深色模式下把近白色网格直接画在序列上。
  Chart.defaults.scale.grid.color       = stroke;
  Chart.defaults.scale.grid.tickColor   = stroke;
  Chart.defaults.scale.grid.drawTicks   = false;
  Chart.defaults.scale.grid.z           = -1;
  Chart.defaults.scale.border.color     = stroke;
  Chart.defaults.scale.ticks.color      = ink;
  Chart.defaults.scale.ticks.padding    = 6;
  Chart.defaults.plugins.legend.labels.color        = ink;
  Chart.defaults.plugins.legend.labels.boxWidth     = 10;
  Chart.defaults.plugins.legend.labels.boxHeight    = 10;
  Chart.defaults.plugins.legend.labels.usePointStyle= true;
  Chart.defaults.plugins.tooltip.backgroundColor = themeVar('--surface-card');
  Chart.defaults.plugins.tooltip.titleColor      = themeVar('--ink');
  Chart.defaults.plugins.tooltip.bodyColor       = themeVar('--ink');
  Chart.defaults.plugins.tooltip.borderColor     = stroke;
  Chart.defaults.plugins.tooltip.borderWidth     = 1;
  // 细标记，仅在数据端使用4px圆角（基线端
  // 保持方角，使柱子读起来像有锚点）。
  Chart.defaults.datasets.bar.maxBarThickness = 22;
  Chart.defaults.datasets.bar.borderSkipped   = 'start';
  Chart.defaults.datasets.bar.borderRadius    = 4;
  Chart.defaults.elements.line.borderWidth    = 2;
  Chart.defaults.elements.point.radius        = 4;
  Chart.defaults.elements.point.hoverRadius   = 6;
}

// 相接填充之间的2px间隔以表面色绘制，因此它
// 是唯一必须跟随主题的序列侧值。
function repaintSurfaceGaps(){
  applyChartTheme();
  const surf=themeVar('--surface-card'), ink=themeVar('--ink-muted'),
        stroke=themeVar('--stroke'), fg=themeVar('--ink');
  const ramp = ORDINAL[currentTheme()] || ORDINAL.light;
  CHARTS.forEach(c=>{
    Object.values((c.options && c.options.scales) || {}).forEach(sc=>{
      if(!sc) return;
      if(sc.grid)   { sc.grid.color=stroke; sc.grid.tickColor=stroke; }
      if(sc.border) { sc.border.color=stroke; }
      if(sc.ticks)  { sc.ticks.color=ink; }
      if(sc.title)  { sc.title.color=ink; }
    });
    const pl=(c.options && c.options.plugins) || {};
    if(pl.legend && pl.legend.labels) pl.legend.labels.color=ink;
    if(pl.tooltip){
      pl.tooltip.backgroundColor=surf; pl.tooltip.titleColor=fg;
      pl.tooltip.bodyColor=fg; pl.tooltip.borderColor=stroke;
    }
    c.data.datasets.forEach(ds=>{
      if(ds._surfaceGap) ds.borderColor=surf;
      if(ds._ordinal){
        ds.backgroundColor=ds.data.map((_,i)=>ramp[Math.min(i,ramp.length-1)]);
        ds.borderColor=ds.backgroundColor;
      }
    });
    c.update('none');
  });
}

function currentTheme(){
  const stamped = document.documentElement.getAttribute('data-cui-theme');
  if(stamped) return stamped;
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function setTheme(t){
  document.documentElement.setAttribute('data-cui-theme', t);
  try{ localStorage.setItem('chdiag-theme', t); }catch(e){}
  const btn=document.getElementById('theme-toggle');
  if(btn) btn.textContent = t==='dark' ? '☀ 浅色' : '☾ 深色';
  // repaintSurfaceGaps重新应用Chart默认值并更新每个图表 —
  // 一趟完成，而不是过去的两趟布局+绘制。
  repaintSurfaceGaps();
}

// ── 辅助函数 ──────────────────────────────────────────────────────────────
function fmt(n){
  if(n==null||n==='')return '—';
  if(typeof n==='number'){
    if(n>=1e12)return(n/1e12).toFixed(2)+' T';
    if(n>=1e9)return(n/1e9).toFixed(2)+' B';
    if(n>=1e6)return(n/1e6).toFixed(2)+' M';
    if(n>=1e3)return(n/1e3).toFixed(1)+' K';
    return n.toLocaleString();
  }
  return String(n);
}

// 完整的normalized_query_hash。它以字符串形式到达，正是为了能
// 整体显示和复制 — 截断到最后8位（过去的行为）
// 会使它无法用于grep query_log，而这正是读者
// 想要hash的唯一用途。
function fullHash(h){ return String(h==null?'':h); }

// 用于轴标签的查询单行指纹。将SQL折叠到
// 一行并裁剪；完整语句一键可达，完整
// hash始终在提示框中，因此这里裁剪不会丢失信息。
function queryLabel(r, max){
  const q=String(r.sample_query||'').replace(/\s+/g,' ').trim();
  if(!q || q==='(redacted in gov mode)') return fullHash(r.hash);
  return q.length>max ? q.slice(0,max-1)+'…' : q;
}

// 在图表下方显示所点击柱条的完整SQL。
function bindQueryPeek(chart, rows, peekId){
  const el=document.getElementById(peekId);
  if(!chart||!el)return;
  const has=rows.some(r=>r.sample_query && r.sample_query!=='(redacted in gov mode)');
  if(!has)return;
  el.textContent='点击柱条显示对应查询。';
  el.style.display='';
  chart.options.onClick=(evt,els)=>{
    if(!els||!els.length)return;
    const r=rows[els[0].index];
    el.textContent='-- normalized_query_hash: '+fullHash(r.hash)
      +'   用户: '+(r.user||'?')+'   执行次数: '+(r.executions||'?')
      +'\n-- 一次代表性执行；两次运行的字面量可能不同\n\n'
      +String(r.sample_query||'').trim();
  };
  chart.canvas.style.cursor='pointer';
  chart.update('none');
}

// 透视 [{timeF, catF, valF}] → Chart.js 数据集。
//
// 超过调色板长度的类别折叠进"Other"，而不是循环第六个
// 色相；每个类别不论大小都保留其槽位，使
// 颜色跟随实体而不是其排名。
function pivot(rows,tf,cf,vf){
  const times=[...new Set(rows.map(r=>r[tf]))].sort();
  const raw=[...new Set(rows.map(r=>r[cf]))];
  const total={};
  rows.forEach(r=>{ total[r[cf]]=(total[r[cf]]||0)+Number(r[vf]||0); });
  const cats=foldSeries(raw, c=>total[c], C.length);
  const kept=new Set(cats);
  const lk={};
  rows.forEach(r=>{
    const c=kept.has(r[cf])?r[cf]:'Other';
    const k=r[tf]+'|'+c;
    lk[k]=(lk[k]||0)+Number(r[vf]||0);
  });
  return{
    labels:times,
    datasets:cats.map((c,i)=>({
      label:c===''?'(未知)':c,
      backgroundColor:c==='Other'?OTHER:slot(i),
      borderColor:c==='Other'?OTHER:slot(i),
      borderWidth:1.5,
      borderRadius:4,
      data:times.map(t=>lk[t+'|'+c]||0)
    }))
  };
}

// 为堆叠图表在相接分段之间提供2px表面间隔。
function stackWithGaps(d){
  const surf=themeVar('--surface-card');
  d.datasets.forEach(ds=>{
    ds.stack='s';
    ds.borderColor=surf;
    ds.borderWidth=2;
    ds.borderRadius=4;
    ds._surfaceGap=true;
  });
  return d;
}

// 渲染普通HTML表格（可选的row-class回调）
// 转义所有以文本形式到达DOM的内容。
//
// 这里的单元格值由客户控制：SQL文本、异常消息、
// 表和列名，现在还有整行日志。原样插值这些内容
// （过去如此）意味着含引号的消息会破坏title
// 属性，含标记的内容会被当作标记解析。bundle中的
// 任何内容都不会仅仅因为来自客户自己的服务器就被当作可信输入。
function esc(v){
  return String(v??'')
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

// 可见的表头。键本身仍是数据键：rowClass
// 回调和htmlCols按英文名称查找它们，因此这里只翻译
// 渲染出的标签。
const COL_LABELS={
  setting:'设置', value:'值', status:'状态', note:'备注',
  hostname:'主机名', database:'数据库', name:'名称', table:'表', table_name:'表名',
  engine:'引擎', total_rows:'总行数', size:'大小', size_uncompressed:'未压缩大小',
  partition_key:'分区键', sorting_key:'排序键', storage_policy:'存储策略',
  parts:'part 数', compressed_size:'压缩后大小', compression_ratio:'压缩比',
  pid:'PID', rss:'RSS', threads:'线程数', state:'状态', command:'命令',
  executions:'执行次数', avg_duration_ms:'平均耗时（ms）', p95_duration_ms:'P95 耗时（ms）',
  max_duration_ms:'最大耗时（ms）', min_duration_ms:'最小耗时（ms）',
  avg_memory:'平均内存', max_memory:'最大内存', errors:'错误数',
  error_type:'错误类型', tables_touched:'涉及的表', user:'用户',
  first_seen:'首次出现', last_seen:'最后出现', sample_exception:'示例异常',
  ts:'时间', level:'级别', logger_name:'logger 名称', message:'消息',
  query_kind:'查询类型', avg_read_mb:'平均读取（MB）', avg_memory_mb:'平均内存（MB）',
  total_read_gb:'总读取（GB）', total_memory_gb:'总内存（GB）', error_count:'错误数',
  sample_query:'示例查询', hash:'hash',
  exception_code:'异常码', count:'数量', msg:'消息',
  event_type:'事件类型', merge_reason:'合并原因', events:'事件数',
  total_size:'总大小', day:'日期',
  file:'文件', kind:'类型', event_time:'事件时间', signal:'信号', thread_id:'线程 ID',
  query_id:'查询 ID', version:'版本', mutation_id:'mutation ID', parts_to_do:'待处理 part 数',
  create_time:'创建时间', reasons:'原因', type:'类型', executing:'正在执行', last_exception:'最后异常',
  cluster:'集群', shard:'分片', replica:'副本', host_name:'主机名', errors_count:'错误数',
  is_readonly:'只读', is_session_expired:'会话已过期', is_leader:'Leader',
  queue_size:'队列大小', inserts_in_queue:'队列中的插入', merges_in_queue:'队列中的合并',
  future_parts:'未来 part', parts_to_check:'待检查 part', absolute_delay:'绝对延迟',
  active_replicas:'活动副本', total_replicas:'总副本',
  verdict:'判定', hours:'小时数', first:'首次', last:'最后', meaning:'含义',
  hour:'小时', flushes:'flush 次数', total_bytes:'总字节', avg_flush_ms:'平均 flush（ms）',
  partition_id:'分区 ID', parts_count:'part 数',
  bytes_allocated_human:'已分配字节', element_count:'元素数', load_factor:'负载因子',
  query_count:'查询数', hit_rate_pct:'命中率（%）', found_rate_pct:'找到率（%）',
  key_names:'键名', key_types:'键类型', attribute_names:'属性名', attribute_types:'属性类型',
  lifetime_min:'最小生存时间', lifetime_max:'最大生存时间', loading_start_time:'加载开始时间',
  last_update:'最后更新', loading_duration_s:'加载耗时（s）',
  origin:'来源', comment:'注释', uuid:'UUID', source:'来源',
  free_space_human:'可用空间', total_space_human:'总空间', free_pct:'可用比例', path:'路径',
  member:'成员', ensemble:'集合',
  'Raft log first → last':'Raft 日志首 → 末',
  'committed / leader':'已提交 / Leader',
  'latest snapshot':'最新快照',
  'chunk size':'分块大小',
  'snapshot format':'快照格式',
  'on disk (snapshots / log)':'磁盘占用（快照 / 日志）'
};

// 渲染普通HTML表格（可选的row-class回调）。
//
// htmlCols指定其值是THIS FILE构建的标记（状态
// 徽章、链接）且不得转义的列。其他所有内容都会被转义。将
// 客户控制的列传入htmlCols是一个bug。
function renderTable(id, rows, cols, rowClass, htmlCols){
  const el=document.getElementById(id);
  if(!el)return;
  if(!rows||rows.length===0){el.innerHTML='<p class="no-data">暂无数据</p>';return;}
  const keys=cols||Object.keys(rows[0]);
  const raw=new Set(htmlCols||[]);
  let h='<table class="dt"><thead><tr>'+keys.map(k=>'<th>'+esc(COL_LABELS[k]||k)+'</th>').join('')+'</tr></thead><tbody>';
  rows.forEach(r=>{
    const cls=rowClass?rowClass(r):'';
    h+='<tr'+(cls?' class="'+esc(cls)+'"':'')+'>'
      +keys.map(k=>raw.has(k)
        ? '<td>'+(r[k]??'')+'</td>'
        : '<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>').join('')
      +'</tr>';
  });
  h+='</tbody></table>';
  el.innerHTML=h;
}

// 为字典格式化状态徽章
function dictStatusBadge(status){
  const cl={'LOADED':'ok-badge','FAILED':'err-badge','LOADING':'warn-badge',
            'NOT_LOADED':'warn-badge'}[status]||'warn-badge';
  return '<span class="'+cl+'">'+esc(status)+'</span>';
}

// ── 可搜索的表浏览器 ──────────────────────────────────────────────────────
(function(){
  const PAGE=50;
  let allData=[], filtered=[], page=0;
  const COLS=['database','table_name','engine','parts','total_rows','size',
              'partition_key','sorting_key','storage_policy'];

  function render(){
    const el=document.getElementById('tbl-explorer');
    const pg=document.getElementById('tbl-pagination');
    const cnt=document.getElementById('tbl-count');
    if(!el)return;
    if(!filtered.length){
      el.innerHTML='<p class="no-data">没有匹配的表</p>';
      pg.innerHTML=''; cnt.textContent='0 张表';
      return;
    }
    const start=page*PAGE, end=Math.min(start+PAGE,filtered.length);
    const slice=filtered.slice(start,end);
    let h='<table class="dt"><thead><tr>'
      +COLS.map(k=>'<th>'+esc(COL_LABELS[k]||k)+'</th>').join('')+'</tr></thead><tbody>';
    slice.forEach(r=>{
      // database / table_name / partition_key / sorting_key是客户DDL。
      h+='<tr>'+COLS.map(k=>'<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>').join('')+'</tr>';
    });
    h+='</tbody></table>';
    el.innerHTML=h;
    cnt.textContent=filtered.length+' / '+allData.length+' 张表';
    const total=Math.ceil(filtered.length/PAGE);
    if(total<=1){pg.innerHTML='';return;}
    let phtml='';
    if(page>0) phtml+='<button onclick="window._tblPg('+(page-1)+')">&#9664;</button>';
    phtml+='<span class="cur">'+( page+1)+' / '+total+'</span>';
    if(page<total-1) phtml+='<button onclick="window._tblPg('+(page+1)+')">&#9654;</button>';
    pg.innerHTML=phtml;
  }

  window._tblPg=function(p){page=p;render();};

  window.tablesFilter=function(){
    const srch=(document.getElementById('tbl-search').value||'').toLowerCase();
    const db=document.getElementById('tbl-db-filter').value;
    const eng=document.getElementById('tbl-engine-filter').value;
    filtered=allData.filter(r=>{
      if(db&&r.database!==db)return false;
      if(eng&&r.engine!==eng)return false;
      if(srch){
        return COLS.some(k=>String(r[k]??'').toLowerCase().includes(srch));
      }
      return true;
    });
    page=0; render();
  };

  window._tablesInit=function(data){
    allData=data||[];
    filtered=[...allData];
    // 填充下拉框
    const dbSel=document.getElementById('tbl-db-filter');
    const engSel=document.getElementById('tbl-engine-filter');
    [...new Set(allData.map(r=>r.database))].sort().forEach(d=>{
      dbSel.innerHTML+='<option value="'+esc(d)+'">'+esc(d)+'</option>';
    });
    [...new Set(allData.map(r=>r.engine))].sort().forEach(e=>{
      engSel.innerHTML+='<option value="'+esc(e)+'">'+esc(e)+'</option>';
    });
    render();
  };
})();

// ── 告警渲染器 ────────────────────────────────────────────────────────────
//
// 一条告警在读者需要请求更多之前显示多少内容。ClickHouse
// 异常会在消息文本中嵌入堆栈跟踪：在真实bundle中实测一条
// 在15行中长达1764个字符，其中
// 前216个是实际错误。replication_queue_errors最多返回50
// 条这样的行，keeper_health最多168条，因此完整渲染每条消息
// 会把页面其余部分埋在堆栈帧之下。
const ALERT_HEAD_CHARS=260;   // 一行实例的内联长度
const ALERT_ROWS_SHOWN=5;     // 其余折叠前列出的实例数

const ALERT_STACK_RE=/\s*Stack trace \(when copying this message[\s\S]*$/;
// 每个ClickHouse异常都以产生它的构建版本结尾。它在每一行都是相同的
// 字符串，而dashboard头部已经说明了版本，因此
// 从内联行中去掉它，保留在完整文本中。
const ALERT_VERSION_RE=/\s*\(version [0-9][^)]*\([^)]*\)\)[\s,.;]*$/;

// alertMessageParts将替换后的消息拆分为内联显示的行
// 和为折叠保留的完整文本。没有实际丢弃内容时
// 返回truncated=false，因此短消息不会得到无用的切换按钮。
function alertMessageParts(msg){
  const full=String(msg==null?'':msg);
  const flat=full.replace(/\s+/g,' ').trim();
  let head=full.replace(ALERT_STACK_RE,'').replace(/\s+/g,' ').trim()
               .replace(ALERT_VERSION_RE,'').replace(/[\s,;]+$/,'');
  if(head.length>ALERT_HEAD_CHARS){
    // ALERT_HEAD_CHARS包含省略号：少切一个字符，使
    // 下面的无空格回退得到260个字符而不是261个。
    const cut=head.slice(0,ALERT_HEAD_CHARS-1);
    const sp=cut.lastIndexOf(' ');
    head=(sp>ALERT_HEAD_CHARS*0.6?cut.slice(0,sp):cut)+'\u2026';
  }
  if(!head) head=flat;
  // truncated驱动切换按钮：仅当内联行还不是
  // 整条消息时才提供，因此短的单行消息不会得到无用的
  // 折叠披露。与扁平消息比较而不是与
  // 去版本副本比较，还覆盖了消息
  // 只有版本后缀的退化情况：剥离会清空head，上面的回退
  // 会恢复它，此时head===flat正确地报告
  // 没有可揭示的内容。
  return {head:head, full:full, truncated:head!==flat, hasStack:ALERT_STACK_RE.test(full)};
}

// alertDisclosure为一条消息渲染"显示其余内容"切换按钮。
function alertDisclosure(parts){
  if(!parts.truncated) return '';
  const label=parts.hasStack?'完整消息和堆栈跟踪':'完整消息';
  return '<details class="alert-more"><summary>'+label+'</summary>'
        +'<pre class="alert-full">'+esc(parts.full)+'</pre></details>';
}

// alertRowLine渲染一个实例：规则的message模板替换为
// 本行的值。模板是我们的，值由客户控制
// （表名、分区id、原始服务器文本），因此整行
// 都会被转义。
// 客户可在自己的集群上运行以查看所显示告警来源的SQL：
// 规则查询本来的运行原样（占位符已按本bundle的模式
// 展开），然后是规则的后续查询。
// 折叠：打开前卡片中只有一行摘要。
function alertQueries(a){
  const qs=[];
  if(a.query) qs.push({title:'触发此告警的查询',query:a.query});
  (a.investigate||[]).forEach(q=>qs.push(q));
  if(!qs.length) return '';
  return '<details class="alert-more alert-queries"><summary>可在你的集群上运行的查询（'+qs.length+'）</summary>'
    +qs.map(q=>'<div class="alert-q"><div class="alert-q-title">'+esc(q.title)
      +' <button type="button" class="alert-copy">复制</button></div>'
      +'<pre class="alert-full alert-sql">'+esc(q.query)+'</pre></div>').join('')
    +'</details>';
}
// 复制：页面处于安全上下文时用Clipboard API，否则
// 选中文本并回退到execCommand — 从file://打开的
// dashboard并非在每个浏览器中都是安全上下文。
document.addEventListener('click',function(e){
  const b=e.target&&e.target.closest?e.target.closest('.alert-copy'):null;
  if(!b) return;
  const pre=b.closest('.alert-q').querySelector('.alert-sql');
  const done=function(){b.textContent='已复制';setTimeout(function(){b.textContent='复制';},1500);};
  const fallback=function(){
    const r=document.createRange();r.selectNodeContents(pre);
    const sel=window.getSelection();sel.removeAllRanges();sel.addRange(r);
    try{document.execCommand('copy')?done():(b.textContent='已选中 — 按 Ctrl+C');}catch(_){b.textContent='已选中 — 按 Ctrl+C';}
  };
  if(navigator.clipboard&&window.isSecureContext){navigator.clipboard.writeText(pre.textContent).then(done,fallback);}
  else fallback();
});
function alertRowLine(a,row){
  let msg=a.message;
  Object.entries(row).forEach(([k,v])=>{msg=msg.split('{'+k+'}').join(String(v==null?'':v));});
  const parts=alertMessageParts(msg);
  return '<li>\u25B8 '+esc(parts.head)+alertDisclosure(parts)+'</li>';
}

function renderAlerts(){
  const alerts=DATA.alerts||[];
  const fired=alerts.filter(a=>(a.rows&&a.rows.length>0)||a.error);
  const skipped=alerts.filter(a=>a.skipped);
  // 完全镜像alert.Summarize："evaluated"同时排除跳过和
  // 出错的规则，因此此数字绝不会与CLI的不一致。
  // （目前使用它的横幅只在erroredRules为空时渲染，所以
  // 那里的减法是空操作 — 但让同一个数字保留两种不同
  // 定义正是"11 fired"bug的来源。）
  const evaluated=alerts.length-skipped.length-alerts.filter(a=>a.error).length;
  const skipNote=skipped.length
    ? '<div class="alert-skipped">ℹ️ '+skipped.length+' 条规则在此服务器上不适用：'+skipped.map(a=>esc(a.name)+(a.skip_reason?' ('+esc(a.skip_reason)+')':'')).join(', ')+'</div>'
    : '';
  const panel=document.getElementById('alerts-panel');
  const bar=document.getElementById('alerts-summary-bar');
  const navLink=document.getElementById('nav-alerts');

  // 摘要标签。ERRORED的规则与实际finding分开计数 —
  // 按严重级别归类会把例如缺失
  // SELECT grant报告成N个严重/警告生产问题。
  const counts={critical:0,warning:0,info:0};
  const erroredRules=fired.filter(a=>a.error);
  fired.filter(a=>!a.error).forEach(a=>{const s=a.severity||'warning';if(counts[s]!==undefined)counts[s]++;});
  // 徽章只统计真实finding。包括出错规则会
  // 渲染出一个红色的"11"，与十一个严重finding完全相同，而真实
  // 状态只是一个权限问题 — 这正是CLI的
  // fired/errored拆分所消除的混淆。损坏的规则改为显示⚠标签。
  const total=counts.critical+counts.warning+counts.info;

  // 为导航链接加徽章
  if(total>0){
    navLink.innerHTML='告警 <span style="background:var(--status-critical);color:#fff;border-radius:var(--click-radii-full);padding:1px 7px;font-size:var(--click-font-size-0);font-weight:var(--click-font-weight-3);margin-left:4px">'+total+'</span>';
  }

  // 摘要栏
  let barHtml='';
  if(counts.critical) barHtml+='<span class="alert-summary-chip chip-critical">🔴 '+counts.critical+' 严重</span>';
  if(counts.warning)  barHtml+='<span class="alert-summary-chip chip-warning">🟡 '+counts.warning+' 警告</span>';
  if(counts.info)     barHtml+='<span class="alert-summary-chip chip-info">🔵 '+counts.info+' 信息</span>';
  if(erroredRules.length) barHtml+='<span class="alert-summary-chip chip-error">⚠ '+erroredRules.length+' 无法运行</span>';
  if(bar) bar.innerHTML=barHtml?'<div class="alert-summary-bar">'+barHtml+'</div>':'';

  if(!panel) return;

  if(!fired.length){
    panel.innerHTML='<div class="alert-ok">✅ '+evaluated+' 条告警规则已评估 — 未发现问题</div>'+skipNote;
    return;
  }

  const SEV_ICON={critical:'🔴',warning:'🟡',info:'🔵'};
  const ORDER={critical:0,warning:1,info:2};
  const sorted=[...fired].sort((a,b)=>(ORDER[a.severity]||1)-(ORDER[b.severity]||1));

  let html='';
  sorted.forEach(a=>{
    const sev=a.severity||'warning';
    // QUERY出错的规则不是级别N的finding — 显示⚠，使
    // 它不会被误读为列表顶部的红色严重项。
    const icon=a.error?'⚠':(SEV_ICON[sev]||'🟡');
    const cls=a.error?'alert-error':'alert-'+sev;
    const cnt=a.error?'error':(a.rows||[]).length;
    const cntLabel=a.error?'查询错误':cnt+' 个实例';

    html+='<div class="alert-item '+cls+'">';
    html+='<div class="alert-header">';
    html+=icon+' <span class="alert-title">'+esc(a.title||a.name)+'</span>';
    html+='<span class="alert-count badge-'+(a.error?'error':sev)+'">'+cntLabel+'</span>';
    if((a.tags||[]).length) html+='<span class="alert-tags">'+a.tags.map(t=>'<span class="alert-tag">'+esc(t)+'</span>').join('')+'</span>';
    html+='</div>'; // 头部

    if(a.description){
      // 规则描述刻意冗长 — 先是解释，接着
      // 是下一步该检查什么。第一段是解释；
      // 其余是读者只有在告警值得跟进时才需要的指导，
      // 因此折叠。
      const d=a.description.trim();
      const brk=d.indexOf('\n\n');
      const first=(brk>0?d.slice(0,brk):d).trim();
      html+='<div class="alert-desc">'+esc(first).replace(/\n/g,'<br>');
      if(brk>0){
        html+='<details class="alert-more"><summary>关于此规则的更多信息</summary>'
             +'<div class="alert-desc-rest">'+esc(d.slice(brk).trim()).replace(/\n/g,'<br>')+'</div></details>';
      }
      html+='</div>';
    }

    if(a.error){
      // a.error是原始服务器异常文本 — 受客户影响，且它
      // 和行消息一样经常携带堆栈跟踪。
      const ep=alertMessageParts(a.error);
      html+='<div class="alert-err-msg">⚠ '+esc(ep.head)+alertDisclosure(ep)+'</div>';
    } else if(a.message&&(a.rows||[]).length){
      // keeper_health在7天窗口中可能每小时返回一行；只列出
      // 前几行，其余点一下就可见。
      const rows=a.rows||[];
      const shown=rows.slice(0,ALERT_ROWS_SHOWN), hidden=rows.slice(ALERT_ROWS_SHOWN);
      html+='<ul class="alert-messages">'+shown.map(r=>alertRowLine(a,r)).join('')+'</ul>';
      if(hidden.length){
        html+='<details class="alert-more"><summary>'+hidden.length+' 个实例</summary>'
             +'<ul class="alert-messages">'+hidden.map(r=>alertRowLine(a,r)).join('')+'</ul></details>';
      }
    }

    html+=alertQueries(a);
    html+='</div>'; // 条目
  });
  panel.innerHTML=html+skipNote;
}

// ── 主初始化 ──────────────────────────────────────────────────────────────
// 查询分析部分每个耗时轴/提示框都使用的自适应耗时helper。
// 原始需求：值较小时以秒渲染，
// 峰值超过200秒后切换为分钟，
// 使长时间运行的查询不会在提示框中显示为
// "180000 ms"。
function pickDurationUnit(maxMs){
  if(maxMs > 200000){
    return {factor: 1/60000, label: 'min', precision: 2};
  }
  return {factor: 1/1000, label: 'sec', precision: 2};
}
function fmtDuration(ms, unit){
  if(ms==null) return '—';
  return (Number(ms) * unit.factor).toFixed(unit.precision) + ' ' + unit.label;
}

// pickByteUnit根据序列中的最大值选择MiB / GiB / TiB。
// 跳过KiB，因为在ClickHouse报告的
// 规模（read_bytes、memory_usage）下，低于1 MiB的每次执行指标
// 通常是噪声，反正会显示成"0.001 MB"。
function pickByteUnit(maxBytes){
  if(maxBytes >= 1024**4) return {factor: 1/(1024**4), label:'TiB', precision:2};
  if(maxBytes >= 1024**3) return {factor: 1/(1024**3), label:'GiB', precision:2};
  return {factor: 1/(1024**2), label:'MiB', precision:2};
}
function fmtBytes(b, unit){
  if(b==null) return '—';
  return (Number(b) * unit.factor).toFixed(unit.precision) + ' ' + unit.label;
}

// renderQaScatter从qa_executions数组为一个数值指标绘制
// 每次执行的散点图。查询分析部分有五个图表
// 共享此形状 — 成功与失败用颜色区分，提示框
// 显示query_id + hostname + 人类可读单位的指标。
//
// 选项：
//   ySelect(row) → number     从一次执行投影一个指标
//   yUnit(maxVal) → unit obj  选择轴单位（{factor,label,precision}）
//   yLabel(unit)  → string    Y轴标题
//   yFmt(value, unit) → string 提示框格式化器
function renderQaScatter(canvasId, rows, opts){
  const succ=[], fail=[];
  let maxY=0;
  rows.forEach(r=>{
    const t=Date.parse(r.ts);
    const y=opts.ySelect(r);
    if(y>maxY) maxY=y;
    const pt={x:t,y:y,query_id:r.query_id,exception_code:r.exception_code,hostname:r.hostname};
    if(Number(r.exception_code)===0 && r.type==='QueryFinish'){ succ.push(pt); }
    else { fail.push(pt); }
  });
  const u=opts.yUnit(maxY);
  mkChart(document.getElementById(canvasId),{
    type:'scatter',
    data:{datasets:[
      // 结果是状态而非身份，因此使用保留的状态
      // 色阶 — 失败标记还带有独特字形，使含义
      // 从不只靠颜色。
      {label:'成功',data:succ,backgroundColor:STATUS.good,borderColor:STATUS.good,pointRadius:4},
      {label:'失败',data:fail,backgroundColor:STATUS.critical,borderColor:STATUS.critical,pointRadius:5,pointStyle:'crossRot'}
    ]},
    options:{responsive:true,maintainAspectRatio:false,
      plugins:{
        legend:{position:'top'},
        tooltip:{callbacks:{label:c=>{
          const p=c.raw;
          const when=new Date(p.x).toISOString().replace('T',' ').replace(/\..*/,'');
          const lines=[when+' · '+opts.yFmt(p.y,u),'query_id：'+(p.query_id||''),'主机：'+(p.hostname||'')];
          if(Number(p.exception_code)!==0) lines.push('exception_code: '+p.exception_code);
          return lines;
        }}}
      },
      scales:{
        x:{type:'linear',
          title:{display:true,text:'event_time（UTC）'},
          ticks:{callback:v=>new Date(v).toISOString().replace('T',' ').replace(/\..*/,'').slice(5,16)}},
        y:{type:'linear',beginAtZero:true,
          title:{display:true,text:opts.yLabel(u)},
          ticks:{callback:v=>(v*u.factor).toFixed(u.precision)}}
      }}
  });
}

// ── 查询分析渲染器 ────────────────────────────────────────────────────────
function renderQueryAnalysis(){
  if(!DATA.qa_enabled) return;
  document.getElementById('sec-qa').style.display='';
  document.getElementById('nav-qa').style.display='';

  // 焦点查询SQL文本 — 仅在非gov模式下（查询文本
  // 包含gov模式在其他地方哈希的真实数据库/表名）。
  if(DATA.qa_mode !== 'gov'){
    const txt=(((DATA.qa_details||[])[0])||{}).query;
    if(txt){
      document.getElementById('qa-query-text').textContent=txt;
      document.getElementById('qa-query-card').style.display='';
    }
  }

  // 焦点卡片 — 分析的范围，以及单id查询
  // （ProfileEvents、text_log、tables、parts）
  // 过滤所依据的执行。仅hash模式下这是该hash最慢的
  // 执行（在预检中自动推导）；在
  // --query-id模式下则是用户提供的UUID。
  const det=(DATA.qa_details||[])[0]||{};
  const fast=(DATA.qa_fast_slow||[])[0]||{};
  const focus=document.getElementById('qa-focus');
  let h='<div class="alert-header">';
  h+='<span class="alert-title">焦点 query_id：'+esc(DATA.qa_query_id||'（无）')+'</span>';
  h+='<span class="alert-tags"><span class="alert-tag">hash '+esc(DATA.qa_hash||'')+'</span>';
  h+='<span class="alert-tag">窗口 '+esc(DATA.qa_from||'')+' → '+esc(DATA.qa_to||'')+'</span></span>';
  h+='</div>';
  if(det.query_kind){
    h+='<div class="alert-desc">';
    h+='类型：<b>'+esc(det.query_kind)+'</b> · 用户：<b>'+esc(det.user||'?')+'</b> · 耗时：<b>'+fmt(det.query_duration_ms)+' ms</b>';
    h+=' · 读取：<b>'+fmt(det.read_rows)+' 行 / '+esc(det.memory_usage_human||'?')+'</b>';
    if(det.exception_code && Number(det.exception_code)!==0){
      h+=' · <span style="color:var(--status-critical-ink)">异常 '+esc(det.exception_code)+'</span>';
    }
    h+='</div>';
  }
  if(fast.slow_query_id){
    h+='<div class="alert-desc">';
    h+='hash 执行次数：<b>'+fmt(fast.executions)+'</b> · ';
    // query_id可由CLIENT设置：客户可以把查询命名为
    // '<img onerror=...>'，而最慢执行的选择会把它带到这里。
    h+='最慢：<b>'+fmt(fast.slow_duration_ms)+' ms</b> (<code>'+esc(fast.slow_query_id)+'</code>) · ';
    h+='最快：<b>'+fmt(fast.fast_duration_ms)+' ms</b> (<code>'+esc(fast.fast_query_id)+'</code>)';
    if(Number(fast.fast_duration_ms)>0){
      const ratio=(Number(fast.slow_duration_ms)/Number(fast.fast_duration_ms)).toFixed(1);
      h+=' → 慢 <b>'+ratio+'×</b>';
    }
    h+='</div>';
  }
  focus.innerHTML=h;

  // ── 每次执行散点图 ────────────────────────────────────────────────────────
  // 五个图表（耗时、内存、用户CPU、读取行数、读取字节）
  // 共享相同形状：每次执行一个点，x = event_time，
  // y = 该次执行的指标，succ = 绿点，fail = 红色
  // 叉。基于DATA.qa_executions构建，因此这些都不需要单独的
  // SQL聚合 — executions_timeline查询
  // 已携带我们需要的每一列。
  const execs=DATA.qa_executions||[];
  if(execs.length){
    renderQaScatter('chart-qa-scatter', execs, {
      ySelect: r => Number(r.query_duration_ms),
      yUnit:   max => pickDurationUnit(max),
      yLabel:  u => '耗时（' + u.label + '）',
      yFmt:    (v,u) => fmtDuration(v,u),
    });
    renderQaScatter('chart-qa-mem', execs, {
      ySelect: r => Number(r.memory_usage),
      yUnit:   max => pickByteUnit(max),
      yLabel:  u => '内存（' + u.label + '）',
      yFmt:    (v,u) => fmtBytes(v,u),
    });
    renderQaScatter('chart-qa-cpu', execs, {
      ySelect: r => Number(r.user_cpu_us||0) / 1e6,    // 微秒 → 秒
      yUnit:   () => ({factor:1, label:'sec', precision:3}),
      yLabel:  () => '用户 CPU（秒）',
      yFmt:    v => v.toFixed(3) + ' 秒',
    });
    renderQaScatter('chart-qa-rrows', execs, {
      ySelect: r => Number(r.read_rows),
      yUnit:   () => ({factor:1, label:'rows', precision:0}),
      yLabel:  () => '读取行数',
      yFmt:    v => fmt(v) + ' 行',
    });
    renderQaScatter('chart-qa-rbytes', execs, {
      ySelect: r => Number(r.read_bytes),
      yUnit:   max => pickByteUnit(max),
      yLabel:  u => '读取（' + u.label + '）',
      yFmt:    (v,u) => fmtBytes(v,u),
    });
  }

  // ── 按分钟分桶的计数图表 ─────────────────────────────────────────────────
  const sum=DATA.qa_summary||[];
  if(sum.length){
    const labels=sum.map(r=>r.time_bucket);
    mkChart(document.getElementById('chart-qa-execs'),{
      type:'bar',
      data:{labels,datasets:[
        {label:'成功',data:sum.map(r=>Number(r.succeeded)),
          backgroundColor:STATUS.good,borderColor:STATUS.good,borderWidth:1,borderRadius:4,stack:'s'},
        {label:'失败',data:sum.map(r=>Number(r.failed)),
          backgroundColor:STATUS.critical,borderColor:STATUS.critical,borderWidth:1,borderRadius:4,stack:'s'}
      ]},
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true},y:{stacked:true,beginAtZero:true}}}
    });
  }

  const fot=DATA.qa_failed_over_time||[];
  if(fot.length){
    const piv=pivot(fot,'time_bucket','error_type','errors');
    piv.datasets.forEach(ds=>{ds.stack='e';});
    mkChart(document.getElementById('chart-qa-failed'),{
      type:'bar',data:piv,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true},y:{stacked:true,beginAtZero:true}}}
    });
  }

  // 焦点（最慢）执行的Top ProfileEvents。
  const pe=(DATA.qa_profile||[]).slice(0,30);
  if(pe.length){
    mkChart(document.getElementById('chart-qa-profile'),{
      type:'bar',
      data:{labels:pe.map(r=>r.metric),datasets:[{label:'值',data:pe.map(r=>Number(r.value)),
        backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:c=>' '+fmt(c.raw)}}},
        scales:{x:{beginAtZero:true,ticks:{callback:v=>fmt(v)}}}}
    });
  }

  // 快 vs 慢比较 — 按|delta|取前30。
  const cmp=(DATA.qa_pe_compare||[]).slice(0,30);
  if(cmp.length){
    mkChart(document.getElementById('chart-qa-compare'),{
      type:'bar',
      data:{labels:cmp.map(r=>r.metric),datasets:[
        // 哪次执行，而非好与坏：身份，因此用分类槽位。
        {label:'慢',data:cmp.map(r=>Number(r.slow_value)),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
        {label:'快',data:cmp.map(r=>Number(r.fast_value)),
          backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4}
      ]},
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{position:'top'},tooltip:{callbacks:{label:c=>c.dataset.label+': '+fmt(c.raw)}}},
        scales:{x:{beginAtZero:true,ticks:{callback:v=>fmt(v)}}}}
    });
  }

  // 明细表
  renderTable('tbl-qa-host',DATA.qa_by_host||[],
    ['hostname','executions','avg_duration_ms','p95_duration_ms','max_duration_ms','min_duration_ms','avg_memory','max_memory','errors'],
    r=>Number(r.errors||0)>0?'error-row':'');
  renderTable('tbl-qa-failed',DATA.qa_failed||[],
    ['error_type','tables_touched','user','errors','first_seen','last_seen','max_duration_ms','sample_exception']);
  renderTable('tbl-qa-tables',DATA.qa_tables||[],
    ['database','table_name','engine','total_rows','size','size_uncompressed','partition_key','sorting_key','storage_policy']);
  renderTable('tbl-qa-parts',DATA.qa_text_parts||[],
    ['ts','level','logger_name','message']);
  renderTable('tbl-qa-textlog',DATA.qa_text_full||[],
    ['ts','level','logger_name','message']);
}

document.addEventListener('DOMContentLoaded',function(){
  // 主题优先：Chart.defaults必须在构建第一个图表前设置正确。
  applyChartTheme();
  const _tbtn=document.getElementById('theme-toggle');
  if(_tbtn){
    _tbtn.textContent = currentTheme()==='dark' ? '\u2600 浅色' : '\u263e 深色';
    _tbtn.addEventListener('click',()=>setTheme(currentTheme()==='dark'?'light':'dark'));
  }
  // 在查看者未写入显式偏好时跟随操作系统。
  if(window.matchMedia){
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change',()=>{
      let stamped=null; try{ stamped=localStorage.getItem('chdiag-theme'); }catch(e){}
      if(!stamped){ repaintSurfaceGaps(); }
    });
  }

  if(!DATA){document.body.innerHTML='<p style="padding:40px;color:red">没有内嵌数据。</p>';return;}

  renderAlerts();
  renderQueryAnalysis();

  // 发布吸顶条带的真实高度，使锚点偏移和
  // 滚动监听阈值跟随它，而不是硬编码猜测。在
  // 尺寸变化时重新测量，因为header的meta列在窄宽度下会换行。
  // 观察而不是只测量一次：条带高度在此运行之后
  // 仍会变化（hdr-meta的两行落在下方，可选分节的导航链接
  // 取消隐藏，网页字体切换），以及每当视口调整大小、
  // header的meta列重新换行时。只测量一次会读到104px，
  // 而条带最终稳定在约123px，导致锚点跳转把每个
  // 标题塞到nav下方。
  const topbar=document.querySelector('.topbar');
  let topbarH=124;
  function measureTopbar(){
    if(!topbar) return;
    const h=Math.round(topbar.getBoundingClientRect().height);
    if(h && h!==topbarH){
      topbarH=h;
      document.documentElement.style.setProperty('--topbar-h', h+'px');
    }
  }
  measureTopbar();
  if(window.ResizeObserver){
    new ResizeObserver(measureTopbar).observe(topbar);
  } else {
    window.addEventListener('resize', measureTopbar, {passive:true});
  }

  // 滚动时高亮活动nav — 吸顶条带中的"你在这里"。
  //
  // 只有RENDERED的分节才能胜出。每个可选面板初始都是
  // display:none，未渲染的元素报告
  // getBoundingClientRect().top = 0，这在每次滚动时都能通过
  // "其顶部在线上方"的测试。因此文档中最后一个这样的分节
  // 在每一趟中都胜出 — 在典型bundle中那是sec-async-inserts，
  // 其导航链接也隐藏，因此条带完全不高亮。
  // 对任何未渲染内容，getClientRects()为空，而列表每趟
  // 都会重建，因为面板会在数据渲染后取消隐藏。
  const navLinks=[...document.querySelectorAll('nav a')];
  const navFor={};
  navLinks.forEach(a=>{navFor[a.getAttribute('href')]=a;});
  function spySections(){
    return [...document.querySelectorAll('section[id]')]
      .filter(s=>s.getClientRects().length && navFor['#'+s.id]);
  }
  function syncNav(){
    const secs=spySections();
    if(!secs.length) return;
    // 一个分节的顶部到达条带下沿时才算当前，
    // 使高亮与读者实际看到的一致。
    // 容差消除了锚点跳转留下的亚像素间隙，该跳转会把
    // 标题正好落在scroll-margin-top处。
    const line=topbarH+16;
    // 默认使用第一个分节：在页面最顶部还没有任何分节
    // 越过该线，而空白条带正是这里要替换掉的。
    let cur=secs[0].id;
    secs.forEach(s=>{if(s.getBoundingClientRect().top<=line)cur=s.id;});
    // 在页面末尾，较短的最后一个分节永远无法到达该线，
    // 因此当滚动无法继续时由最后一个胜出。
    if(Math.ceil(window.innerHeight+window.scrollY)>=document.documentElement.scrollHeight-2){
      cur=secs[secs.length-1].id;
    }
    navLinks.forEach(a=>{
      const on=a.getAttribute('href')==='#'+cur;
      a.classList.toggle('active',on);
      if(on) a.setAttribute('aria-current','true'); else a.removeAttribute('aria-current');
    });
    // header按钮携带当前分节名称，供侧栏隐藏时使用 —
    // 即侧栏显示时高亮链接提供的"你在这里"。只取第一个
    // 文本节点：Alerts链接还带有计数徽章，
    // 而"Alerts 1"不是分节名称。
    const curLink=navFor['#'+cur];
    const label=document.getElementById('nav-current');
    const name=curLink?(curLink.firstChild&&curLink.firstChild.nodeType===3?curLink.firstChild.textContent:curLink.textContent).trim():'';
    if(label && name && label.textContent!==name) label.textContent=name;
    // 在比列表短的侧栏中保持高亮链接可见。
    // 仅在当前分节变化时：滚动趟每帧运行，
    // 每趟都滚动列表会与读者对抗。
    if(curLink && cur!==lastCur){
      lastCur=cur;
      if(curLink.getClientRects().length) curLink.scrollIntoView({block:'nearest'});
    }
  }
  let lastCur=null;
  // 直接从监听器调用，而不是通过requestAnimationFrame
  // 合并。该趟约20次getBoundingClientRect读取，没有
  // 写入，浏览器已将滚动事件限制在帧率，而
  // rAF会让高亮依赖一次重绘 — 这在无头--virtual-time-budget下
  // 从不发生，因此行为无法
  // 测试。实时读取能在折叠展开
  // 并移动其下每个分节时保持MATH正确（缓存的偏移表则不能）—
  // 但该趟仍必须被触发，而打开<details>既不是
  // 滚动也不是尺寸变化。若不管它，高亮会一直指向展开前的
  // 分节，直到读者滚动。因此观察页面自身高度：
  // 任何移动分节的内容变化 — 折叠、表格过滤、
  // 面板在数据渲染后取消隐藏 — 都会重新运行该趟。
  window.addEventListener('scroll',syncNav,{passive:true});
  window.addEventListener('resize',syncNav,{passive:true});
  const mainEl=document.querySelector('main');
  if(window.ResizeObserver && mainEl){
    new ResizeObserver(syncNav).observe(mainEl);
  }
  // toggle不冒泡，但document上的捕获监听器仍能
  // 看到它 — 这是折叠场景的显式钩子，也是
  // 缺少ResizeObserver时的回退。
  document.addEventListener('toggle',syncNav,true);
  // 现在一次用于初始高亮，加载后一次 — 可选面板
  // 会随数据渲染而取消隐藏，从而改变存在的分节。
  syncNav();
  window.addEventListener('load',syncNav);

  // 侧栏切换。header中的箭头隐藏和显示侧栏；
  // 该选择像主题一样被记住，因此偏好全宽的读者
  // 可跨bundle保持。低于900px时侧栏覆盖
  // 内容，因此在那里选择分节会再次隐藏它。焦点跟随
  // 面板：显示时进入当前分节的链接，隐藏时回到箭头，
  // 使键盘读者永不丢失位置。
  (function(){
    const nav=document.getElementById('main-nav');
    const toggle=document.getElementById('nav-toggle');
    const root=document.documentElement;
    if(!nav||!toggle) return;
    const narrow=()=>window.matchMedia&&window.matchMedia('(max-width:900px)').matches;
    function isShown(){ return !root.classList.contains('nav-collapsed'); }
    function reflect(){
      const shown=isShown();
      toggle.setAttribute('aria-expanded',shown?'true':'false');
      toggle.setAttribute('aria-label',shown?'隐藏分节侧栏':'显示分节侧栏');
    }
    function setNav(shown,remember){
      root.classList.toggle('nav-collapsed',!shown);
      reflect();
      if(remember){ try{ localStorage.setItem('chdiag-nav',shown?'shown':'collapsed'); }catch(e){} }
      if(shown){
        const a=nav.querySelector('a.active')||navLinks.find(l=>l.getClientRects().length);
        if(a){ a.focus({preventScroll:true}); a.scrollIntoView({block:'nearest'}); }
      } else {
        toggle.focus({preventScroll:true});
      }
    }
    reflect(); // 引导脚本可能在此运行前已将其折叠
    toggle.addEventListener('click',()=>setNav(!isShown(),true));
    nav.addEventListener('click',e=>{ if(e.target.closest('a')&&narrow()) setNav(false,false); });
    document.addEventListener('keydown',e=>{ if(e.key==='Escape'&&isShown()&&narrow()) setNav(false,false); });
  })();

  // 头部
  // 部署类型与它所限定的版本放在一起 — "[cloud]
  // Version: 26.6.2191" — 而不是标题旁，因为mode
  // 说明采集了哪些system表，而不是页面是什么。
  document.getElementById('hdr-meta').innerHTML=
    '生成时间：'+esc(DATA.generated_at)+'<br>'
    +'<span class="badge badge-'+esc(DATA.mode)+'">'+esc(DATA.mode)+'</span>'
    +' 版本：'+esc(DATA.version||'N/A')
    // system表描述的是哪个节点，以及它已运行多久：
    // 针对错误主机的bundle，或在采集前刚重启的节点，
    // 必须在任何图表之前可见。
    +(DATA.node?'<br>节点：'+esc(DATA.node)+(DATA.uptime?' &middot; 采集时已运行 '+esc(DATA.uptime):''):'')
    +(DATA.collection?'<br>采集：'+esc(DATA.collection):'');

  // 面板标题说明数据覆盖的窗口。从已采集文件构建时，
  // 那是collector的窗口（-from/-to、essential运行的
  // 1天、3天的part日志）；实时回退保持默认文本。
  document.querySelectorAll('[data-win]').forEach(function(el){
    const w=(DATA.windows||{})[el.getAttribute('data-win')];
    if(w) el.textContent=w;
  });

  // 这是哪种诊断：essential采集（挣扎中的
  // 服务器，或-collect essential）有1天窗口和更少文件，
  // 读者必须在把任何缺失读成健康之前知道这一点。
  (function(){
    const c=DATA.collect||{}; const el=document.getElementById('collect-banner');
    if(!el||!c.kind) return;
    const sc=c.skipped_collectors||[], sp=c.skipped_panels||[];
    let h;
    if(c.kind==='essential'){
      el.classList.add('essential');
      h='<b>⚠ 精简诊断（essential）</b> — '+esc(c.summary||'')
        +'<br><span class="muted">仅采集了开销低且关键的 table，窗口为 1 天。'
        +(sc.length?esc(String(sc.length))+' 个 collector 被跳过（'+sc.slice(0,12).map(esc).join(', ')+(sc.length>12?', …':'')+'）。':'')
        +(sp.length?'未构建的面板：'+sp.map(esc).join(', ')+'。':'')
        +'此处数据缺失并不能证明系统健康。待节点恢复后，用 <code>-collect full</code> 重新运行以获得完整视图。</span>';
    } else {
      h='<b>完整诊断（full）</b> — '+esc(c.summary||'')
        +(sc.length?'<br><span class="muted">'+esc(String(sc.length))+' 个 collector 因时间预算被跳过：'+sc.slice(0,12).map(esc).join(', ')+'。</span>':'')
        +(sp.length?'<br><span class="muted">缺少数据的面板：'+sp.map(esc).join(', ')+' — 此处数据缺失并不能证明系统健康。</span>':'');
    }
    el.innerHTML=h; el.style.display='';
  })();

  // 统计
  // 两个参数都esc()：目前每个调用者传的都是数字、
  // 版本字符串或拼接的负载均值，因此今天渲染没有差异 —
  // 但stat tile是通用helper，默认转义
  // 契约必须在helper处成立，而不是在每个调用点。
  const tile=(v,l)=>'<div class="stat-card"><div class="val'
    +(String(v).length>14?' val-sm':String(v).length>8?' val-md':'')
    +'">'+esc(v)+'</div><div class="lbl">'+esc(l)+'</div></div>';
  const sg=document.getElementById('stats-grid');
  sg.innerHTML=[
    // stat tile是主视觉数字：长字符串（运行时间、长版本号）会
    // 降一档字号，而不是把标题折成三行。
    tile(DATA.version||'N/A','服务器版本'),
    tile(DATA.uptime||'N/A','运行时间'),
    tile(fmt(DATA.total_databases),'数据库'),
    tile(fmt(DATA.total_tables),'表'),
    tile(fmt(DATA.active_parts),'活动 part'),
    tile(DATA.total_size||'N/A','数据总大小'),
  ].join('');

  // ── Host facts（来自host_info.json；跳过host-info时不存在） ──
  (function(){
    const hi=DATA.host_info;
    if(!hi)return;
    document.getElementById('host-block').style.display='';

    const os=hi.os||{}, cpu=hi.cpu||{}, mem=hi.memory||{};
    // null/空表示collector无法读取该字段；0是真实
    // 读数（禁用swap的主机上swap_total_bytes为0），必须
    // 渲染为0.00 GiB，而不是缺失。
    const gib=b=>(b==null||b==='')?'—':(Number(b)/1073741824).toFixed(2)+' GiB';
    const dur=sec=>{
      sec=Number(sec||0); if(!sec)return '—';
      const d=Math.floor(sec/86400),h=Math.floor(sec%86400/3600),m=Math.floor(sec%3600/60);
      return (d?d+'d ':'')+(h?h+'h ':'')+m+'m';
    };

    // 主机自身的概要数字，追加到现有KPI行，
    // 使读者同时看到服务器和机器。
    if(cpu.logical_cpus) sg.innerHTML+=tile(String(cpu.logical_cpus),'逻辑 CPU');
    if(mem.total_bytes)  sg.innerHTML+=tile(gib(mem.total_bytes),'主机内存');
    if(cpu.load_avg_1_5_15 && cpu.load_avg_1_5_15.length)
      sg.innerHTML+=tile(cpu.load_avg_1_5_15.join(' / '),'负载 1/5/15m');

    // 机器信息。用定义风格表格而不是更多tile：这些是
    // 读一次的字符串，而不是用来比较的量级。
    const osRows=[
      ['主机名',os.hostname],
      ['操作系统',[os.distro,os.distro_version].filter(Boolean).join(' ')],
      ['内核',os.kernel_version],
      ['架构',os.arch],
      ['主机运行时间',dur(os.uptime_seconds)],
      ['CPU 型号',cpu.model_name],
      ['向量指令标志',(cpu.notable_flags||[]).join(', ')],
      ['内存总量',gib(mem.total_bytes)],
      ['可用内存',gib(mem.available_bytes)],
      ['页缓存',gib(mem.cached_bytes)],
      ['交换分区总量',gib(mem.swap_total_bytes)],
      ['采集时间',hi.collected_at]
    ].filter(r=>r[1]!==undefined && r[1]!=='' && r[1]!==null)
     .map(r=>({setting:r[0],value:r[1]}));
    renderTable('tbl-host-os',osRows,['setting','value']);

    // 可调参数带有状态，因此除颜色外还有图标+文字。
    const ICON={ok:'\u2713 ok',warning:'\u26a0 warning',info:'\u2013',unknown:'? unknown'};
    const checks=(DATA.host_checks||[]).map(c=>({
      setting:c.setting,
      value:c.value,
      status:ICON[c.status]||c.status,
      note:c.note||''
    }));
    renderTable('tbl-host-tunables',checks,['setting','value','status','note'],
      r=>String(r.status).indexOf('warning')>=0?'alert-row':'');


    const procs=(hi.top_processes_by_rss||[]).slice(0,10).map(pr=>({
      pid:pr.pid, rss:gib(pr.rss_bytes), threads:pr.threads, state:pr.state, command:pr.command
    }));
    renderTable('tbl-host-procs',procs,['pid','rss','threads','state','command']);

    // 无法读取的部分绝不能看起来健康。
    const notes=hi.notes||[];
    if(notes.length){
      // Notes是由OS错误和路径构建的自由文本 — 三种来源中
      // 唯一不是固定枚举的，因此在拼接前逐个转义。
      document.getElementById('host-notes').innerHTML=
        '<div class="alert-skipped">\u2139 host facts 部分不可用：'+notes.map(esc).join('; ')+'</div>';
    }
  })();

  // ── 存储：按数据库的大小（水平柱） ───────────────────────────────────────
  (function(){
    const rows=DATA.storage_by_db||[];
    if(!rows.length)return;
    mkChart(document.getElementById('chart-storage-db'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.database),
        datasets:[{
          label:'压缩后（字节）',
          data:rows.map(r=>r.bytes_total),
          // 名义类别：一个序列，一种颜色。每根柱子一个色相
          // 会用颜色重新编码柱长，白白浪费身份
          // 通道。
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>{
          const r=rows[ctx.dataIndex];
          return[' 大小：'+r.size_human,' 行数：'+fmt(r.rows),' 压缩比：'+r.compression_ratio+'x'];
        }}}},
        scales:{x:{ticks:{callback:v=>v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
      }
    });
  })();

  // ── 存储：引擎分布（环形图） ─────────────────────────────────────────────
  (function(){
    const rows=DATA.engines_dist||[];
    if(!rows.length)return;
    // 用柱而不是环形：读者在这里的任务是比较计数，而
    // 环会让接近的值难以区分。它还避开了
    // 环形图会带来的全配对颜色上限。
    mkChart(document.getElementById('chart-engines'),{
      type:'bar',
      data:{labels:rows.map(r=>r.engine),
        datasets:[{label:'表',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false}},
        scales:{x:{beginAtZero:true,ticks:{precision:0}}}}
    });
  })();

  // 最大表
  renderTable('tbl-top-tables',DATA.top_tables||[],
    ['database','table','parts','total_rows','compressed_size','compression_ratio']);

  // ── 表浏览器 ─────────────────────────────────────────────────────────────
  window._tablesInit(DATA.tables_list||[]);

  // ── 查询：随时间（折线） ─────────────────────────────────────────────────
  (function(){
    const rows=DATA.query_by_time||[];
    if(!rows.length)return;
    const d=pivot(rows,'time','query_kind','count');
    d.datasets.forEach(ds=>{ds.fill=false;ds.tension=0.3;ds.pointRadius=2;});
    mkChart(document.getElementById('chart-query-time'),{
      type:'line',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{ticks:{maxTicksLimit:12,maxRotation:45}},y:{beginAtZero:true}}}
    });
  })();

  // ── 查询：按类型计数（柱） ───────────────────────────────────────────────
  (function(){
    const rows=DATA.query_by_kind||[];
    if(!rows.length)return;
    mkChart(document.getElementById('chart-query-kind'),{
      type:'bar',
      data:{labels:rows.map(r=>r.query_kind||'未知'),
        datasets:[{label:'数量',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},scales:{y:{beginAtZero:true}}}
    });
  })();

  // ── 查询：按类型的平均耗时和平均内存 — 两个单轴图表 ──
  //
  // 这些过去是一个带第二y轴的图表（左边ms，右边
  // MB）。同一图上两个刻度会任意对齐，因此图像会虚构
  // 数据中并不存在的相关性。相同度量，各用一个轴。
  (function(){
    const rows=DATA.query_by_kind||[];
    if(!rows.length)return;
    const labels=rows.map(r=>r.query_kind||'未知');
    const one=(canvas,label,vals,axis)=>mkChart(document.getElementById(canvas),{
      type:'bar',
      data:{labels:labels,datasets:[{label:label,data:vals,
        backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]},
      options:{responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false}},
        scales:{y:{beginAtZero:true,title:{display:true,text:axis}}}}
    });
    one('chart-query-duration','平均耗时',rows.map(r=>r.avg_duration_ms),'ms');
    one('chart-query-memory','平均内存',rows.map(r=>r.avg_memory_mb),'MB');
  })();

  // ── 深入分析：最慢查询（水平柱） ─────────────────────────────────────────
  (function(){
    const rows=DATA.query_slow||[];
    if(!rows.length)return;
    const labels=rows.map(r=>queryLabel(r,34));
    const ch=mkChart(document.getElementById('chart-slow-queries'),{
      type:'bar',
      data:{
        labels:labels,
        datasets:[
          {label:'平均耗时（ms）',data:rows.map(r=>r.avg_duration_ms),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
          {label:'最大耗时（ms）',data:rows.map(r=>r.max_duration_ms),backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4}
        ]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'},tooltip:{callbacks:{
          title:ctx=>{
            const r=rows[ctx[0].dataIndex];
            return r.query_kind+' · '+r.user;
          },
          afterLabel:ctx=>{
            const r=rows[ctx.dataIndex];
            return['执行次数：'+r.executions,'错误：'+r.errors,
                   '平均读取：'+r.avg_read_mb+' MB','平均内存：'+r.avg_memory_mb+' MB',
                   'hash: '+fullHash(r.hash)];
          }
        }}},
        scales:{x:{beginAtZero:true,title:{display:true,text:'毫秒'}}}
      }
    });
    bindQueryPeek(ch,rows,'peek-slow-queries');
  })();

  // ── 深入分析：最大读取量（水平柱） ───────────────────────────────────────
  (function(){
    const rows=DATA.query_heavy||[];
    if(!rows.length)return;
    const labels=rows.map(r=>queryLabel(r,34));
    const ch=mkChart(document.getElementById('chart-heavy-reads'),{
      type:'bar',
      data:{
        labels:labels,
        datasets:[{
          label:'平均读取（MB）',
          data:rows.map(r=>r.avg_read_mb),
          // 名义类别：一个序列，一种颜色。每根柱子一个色相
          // 会用颜色重新编码柱长，白白浪费身份
          // 通道。
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{
          title:ctx=>{
            const r=rows[ctx[0].dataIndex];
            return r.query_kind+' · '+r.user;
          },
          label:ctx=>' 平均读取：'+ctx.raw+' MB',
          afterLabel:ctx=>{
            const r=rows[ctx.dataIndex];
            return['执行次数：'+r.executions,'总读取：'+r.total_read,
                   '平均耗时：'+r.avg_duration_ms+' ms',
                   'hash: '+fullHash(r.hash)];
          }
        }}},
        scales:{x:{beginAtZero:true,title:{display:true,text:'每查询 MB（平均）'}}}
      }
    });
    bindQueryPeek(ch,rows,'peek-heavy-reads');
  })();

  // ── 深入分析：用户活动（分组柱） ─────────────────────────────────────────
  (function(){
    const rows=DATA.query_by_user||[];
    if(!rows.length)return;
    mkChart(document.getElementById('chart-user-activity'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.user),
        datasets:[
          {label:'执行次数',data:rows.map(r=>r.executions),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
          // 错误是状态，因此保留预留的critical token。
          {label:'错误',data:rows.map(r=>r.error_count),backgroundColor:STATUS.critical,borderColor:STATUS.critical,borderWidth:1,borderRadius:4}
        ]
      },
      options:{
        responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'},tooltip:{callbacks:{
          afterLabel:ctx=>{
            if(ctx.datasetIndex!==0)return;
            const r=rows[ctx.dataIndex];
            return['平均耗时：'+r.avg_duration_ms+' ms','总读取：'+r.total_read_gb+' GB','总内存：'+r.total_memory_gb+' GB'];
          }
        }}},
        scales:{y:{beginAtZero:true}}
      }
    });
  })();

  // 慢查询表
  renderTable('tbl-slow-queries',DATA.query_slow||[],
    ['query_kind','user','executions','avg_duration_ms','max_duration_ms','avg_read_mb','avg_memory_mb','errors','hash','sample_query'],
    r=>r.errors>0?'alert-row':'');

  // 用户摘要表
  renderTable('tbl-user-summary',DATA.query_by_user||[],
    ['user','executions','avg_duration_ms','total_read_gb','total_memory_gb','error_count'],
    r=>r.error_count>0?'alert-row':'');

  // ── 异常 ──────────────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.exceptions||[];
    if(!rows.length){
      document.getElementById('chart-exceptions').parentElement.innerHTML='<p class="no-data">最近 7 天没有异常</p>';
      return;
    }
    mkChart(document.getElementById('chart-exceptions'),{
      type:'bar',
      data:{
        labels:rows.map(r=>'错误码 '+r.exception_code),
        datasets:[{label:'数量',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>' 数量：'+ctx.raw}}},
        scales:{x:{beginAtZero:true}}
      }
    });
  })();
  renderTable('tbl-exceptions',DATA.exceptions||[],['exception_code','count','msg']);

  // ── Part日志：随时间（堆叠柱） ───────────────────────────────────────────
  (function(){
    const rows=DATA.part_log_by_time||[];
    if(!rows.length)return;
    const d=pivot(rows,'time','event_type','count');
    stackWithGaps(d);
    mkChart(document.getElementById('chart-partlog-time'),{
      type:'bar',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true,ticks:{maxTicksLimit:14,maxRotation:45}},y:{stacked:true,beginAtZero:true}}}
    });
  })();

  // ── Part日志：按类型（水平柱） ──────────────────────────────────────────
  //
  // 过去是对最多约8种事件类型的环形图。Part事件跨几个数量级
  // （NewPart使其他一切相形见绌），环形图会把它渲染成一个扇区
  // 和一条细线；柱状图保持比较可读，并解除了环形图
  // 施加的三槽位全配对颜色上限。
  (function(){
    const rows=[...(DATA.part_log_by_type||[])].sort((a,b)=>Number(b.count||0)-Number(a.count||0));
    if(!rows.length)return;
    mkChart(document.getElementById('chart-partlog-type'),{
      type:'bar',
      data:{
        labels:rows.map(r=>r.event_type),
        datasets:[{label:'事件数',data:rows.map(r=>r.count),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]
      },
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>
          [' 事件数：'+fmt(Number(rows[ctx.dataIndex].count||0)),
           ' 大小：'+(rows[ctx.dataIndex].total_size||'—')]}}},
        scales:{x:{beginAtZero:true}}}
    });
  })();

  // ── 字典 ──────────────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.dictionaries||[];
    if(!rows.length){
      ['chart-dict-status','chart-dict-bytes','chart-dict-lifetime'].forEach(id=>{
        const el=document.getElementById(id);
        if(el)el.parentElement.innerHTML='<p class="no-data">没有字典</p>';
      });
      document.getElementById('tbl-dicts').innerHTML='<p class="no-data">没有字典</p>';
      return;
    }

    // 现在行是按(dict, pod)的 — cloud模式使用clusterAllReplicas，
    // 因此在3个副本上加载的dict显示3行。对于渲染N份会误导的
    // 图表，聚合到按dict；但为表格保留
    // 原始的按pod行。
    const byDict={};
    rows.forEach(r=>{
      const k=r.database+'.'+r.name;
      const entry=byDict[k]||(byDict[k]={
        key:k, database:r.database, name:r.name, type:r.type,
        bytes_max:0, bytes_max_human:'',
        lifetime_min:Number(r.lifetime_min||0), lifetime_max:Number(r.lifetime_max||0),
        statuses:{}, loaded_pods:0, total_pods:0
      });
      entry.total_pods++;
      entry.statuses[r.status]=(entry.statuses[r.status]||0)+1;
      if(r.status==='LOADED') entry.loaded_pods++;
      const b=Number(r.bytes_allocated||0);
      if(b>entry.bytes_max){ entry.bytes_max=b; entry.bytes_max_human=r.bytes_allocated_human||''; }
    });
    const dicts=Object.values(byDict);

    // 状态饼图 — 每个状态的(dict × pod)槽位计数。一个在3个pod上
    // LOADED、在1个pod上NOT_LOADED的dict贡献3+1个槽位，
    // 使操作者能看到集群范围的加载覆盖。
    const statusMap={};
    rows.forEach(r=>{statusMap[r.status]=(statusMap[r.status]||0)+1;});
    const statLabels=Object.keys(statusMap);
    // 用柱而不是饼图：只有两三种状态时环形只是装饰，而
    // 操作者读的是计数。填充保持保留状态色阶，
    // 状态在轴上明确写出，因此没有任何东西只靠色相表达。
    statLabels.sort((a,b)=>statusMap[b]-statusMap[a]);
    mkChart(document.getElementById('chart-dict-status'),{
      type:'bar',
      data:{
        labels:statLabels,
        datasets:[{label:'pod 槽位',data:statLabels.map(s=>statusMap[s]),
          backgroundColor:statLabels.map(s=>DICT_STATUS_COLOR[s]||STATUS.neutral),
          borderColor:statLabels.map(s=>DICT_STATUS_COLOR[s]||STATUS.neutral),
          borderWidth:1,borderRadius:4}]
      },
      options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{
          label:ctx=>' '+ctx.parsed.x+' 个 pod 槽位'}}},
        scales:{x:{beginAtZero:true,ticks:{precision:0}}}}
    });

    // 已分配字节 — 每个dict跨pod的最大值（同一dict在加载它的每个pod上
    // 字节非常相似；用max可避免在视觉上缩小
    // 在某个副本上NOT_LOADED的dict）。
    const topDicts=[...dicts].sort((a,b)=>b.bytes_max-a.bytes_max).slice(0,15);
    mkChart(document.getElementById('chart-dict-bytes'),{
      type:'bar',
      data:{
        labels:topDicts.map(d=>d.key),
        datasets:[{
          label:'各 pod 最大 bytes_allocated',
          data:topDicts.map(d=>d.bytes_max),
          backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4
        }]
      },
      options:{
        indexAxis:'y',responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false},tooltip:{callbacks:{label:ctx=>{
          const d=topDicts[ctx.dataIndex];
          return [' '+d.bytes_max_human, ' 已加载于 '+d.loaded_pods+' / '+d.total_pods+' 个 pod'];
        }}}},
        scales:{x:{ticks:{callback:v=>v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
      }
    });

    // 生存时间按定义（每个pod值相同），因此使用
    // 去重后的按dict视图。
    const lifeRows=dicts.filter(d=>d.lifetime_max>0||d.lifetime_min>0).slice(0,20);
    if(lifeRows.length){
      mkChart(document.getElementById('chart-dict-lifetime'),{
        type:'bar',
        data:{
          labels:lifeRows.map(d=>d.name),
          datasets:[
            {label:'最小生存时间（s）',data:lifeRows.map(d=>d.lifetime_min),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
            {label:'最大生存时间（s）',data:lifeRows.map(d=>d.lifetime_max),backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4}
          ]
        },
        options:{
          responsive:true,maintainAspectRatio:false,
          interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'}},
          scales:{x:{ticks:{maxRotation:45}},y:{beginAtZero:true,title:{display:true,text:'秒'}}}
        }
      });
    } else {
      document.getElementById('chart-dict-lifetime').parentElement.innerHTML='<p class="no-data">没有生存时间数据</p>';
    }

    // 字典明细表 — 按(dict, pod)的行，使操作者
    // 能看到每个运行时统计来自哪个pod。列顺序：
    // 身份 → 拓扑 → 大小 → 活动 → 模式 → 生命周期 →
    // 错误 → 元数据。长文本列最后；表格包装器
    // 已有overflow-x，因此横向滚动是预期的。
    const el=document.getElementById('tbl-dicts');
    const cols=['hostname','database','name','status','type','source',
                'bytes_allocated_human','element_count','load_factor',
                'query_count','hit_rate_pct','found_rate_pct','error_count',
                'key_names','key_types','attribute_names','attribute_types',
                'lifetime_min','lifetime_max',
                'loading_start_time','last_update','loading_duration_s',
                'last_exception','origin','comment','uuid'];
    let h='<table class="dt"><thead><tr>'+cols.map(k=>'<th>'+esc(COL_LABELS[k]||k)+'</th>').join('')+'</tr></thead><tbody>';
    rows.forEach(r=>{
      const isFailed=r.status==='FAILED'||String(r.last_exception||'').length>1;
      h+='<tr'+(isFailed?' class="error-row"':r.status==='LOADING'?' class="alert-row"':'')+'>';
      cols.forEach(k=>{
        // name / last_exception / origin / comment / source是客户数据。
        if(k==='status') h+='<td>'+dictStatusBadge(r[k])+'</td>';
        else h+='<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>';
      });
      h+='</tr>';
    });
    h+='</tbody></table>';
    el.innerHTML=h;
  })();

  // ── 崩溃日志 ─────────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.crash_log||[];
    if(rows.length>0){
      document.getElementById('sec-crashlog').style.display='';
      document.getElementById('nav-crashlog').style.display='';
      renderTable('tbl-crash-log',rows,
        ['event_time','signal','thread_id','query_id','version'],
        ()=>'error-row');
    }
  })();

  // ── 待处理工作 ───────────────────────────────────────────────────────────
  renderTable('tbl-mutations',DATA.mutations||[],
    ['database','table','mutation_id','parts_to_do','create_time','command']);
  renderTable('tbl-detached',DATA.detached||[],
    ['database','table','count','size','reasons']);
  renderTable('tbl-replication',DATA.replication_queue||[],
    ['table','type','executing','last_exception'],
    r=>r.last_exception&&r.last_exception.length>1?'error-row':'');

  // ── 集群（仅cloud） ───────────────────────────────────────────────────
  if(DATA.mode==='cloud'&&(DATA.clusters||[]).length>0){
    document.getElementById('sec-clusters').style.display='';
    document.getElementById('nav-clusters').style.display='';
    renderTable('tbl-clusters',DATA.clusters,
      ['cluster','shard','replica','host_name','errors_count'],
      r=>r.errors_count&&r.errors_count!=='0'?'alert-row':'');
  }

  // ── 副本健康 ─────────────────────────────────────────────────────────────
  (function(){
    const allRows=DATA.replicas||[];
    if(!allRows.length)return;
    document.getElementById('sec-replicas').style.display='';
    document.getElementById('nav-replicas').style.display='';
    // 需关注者优先：只读，然后落后超过60秒，然后其余 —
    // 每组内保持SQL的延迟顺序（Array.prototype.sort是稳定的）。
    // 只按延迟排序（如SQL所做）可能把延迟很小的只读表
    // 挤出明细表第1页。图表对顺序不敏感，
    // 因此它们使用同一数组。
    const rank=r=>r.is_readonly?2:(Number(r.absolute_delay||0)>60?1:0);
    const rows=[...allRows].sort((a,b)=>rank(b)-rank(a));

    // 延迟分布（柱）
    const delayBuckets={'0s':0,'<10s':0,'<60s':0,'<5m':0,'≥5m':0};
    rows.forEach(r=>{
      const d=Number(r.absolute_delay||0);
      if(d===0)delayBuckets['0s']++;
      else if(d<10)delayBuckets['<10s']++;
      else if(d<60)delayBuckets['<60s']++;
      else if(d<300)delayBuckets['<5m']++;
      else delayBuckets['≥5m']++;
    });
    const bLabels=Object.keys(delayBuckets);
    const bData=bLabels.map(k=>delayBuckets[k]);
    // 延迟区间是ORDERED的，因此采用单色相序数色阶。旧的
    // 绿→黄→红彩虹用了五个色相重新编码轴
    // 已明确写出的顺序，而且读起来像状态色阶，但它并不是。
    const bColors=(ORDINAL[currentTheme()]||ORDINAL.light).slice(0,bLabels.length);
    mkChart(document.getElementById('chart-replica-delay'),{
      type:'bar',
      data:{labels:bLabels,datasets:[{label:'副本',data:bData,
        backgroundColor:bColors,borderColor:bColors,borderWidth:1,borderRadius:4,_ordinal:true}]},
      options:{responsive:true,maintainAspectRatio:false,
        plugins:{legend:{display:false}},scales:{y:{beginAtZero:true,ticks:{stepSize:1}}}}
    });

    // 队列大小水平柱（前15）
    const topQ=[...rows].sort((a,b)=>Number(b.queue_size||0)-Number(a.queue_size||0)).slice(0,15);
    if(topQ.some(r=>Number(r.queue_size||0)>0)){
      mkChart(document.getElementById('chart-replica-queue'),{
        type:'bar',
        data:{
          labels:topQ.map(r=>r.database+'.'+r.table),
          datasets:[
            {label:'队列大小',data:topQ.map(r=>r.queue_size||0),backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4},
            {label:'队列中的插入',data:topQ.map(r=>r.inserts_in_queue||0),backgroundColor:C[1],borderColor:C[1],borderWidth:1,borderRadius:4},
            {label:'队列中的合并',data:topQ.map(r=>r.merges_in_queue||0),backgroundColor:C[2],borderColor:C[2],borderWidth:1,borderRadius:4}
          ]
        },
        options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
          interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'}},scales:{x:{beginAtZero:true}}}
      });
    } else {
      document.getElementById('chart-replica-queue').parentElement.innerHTML='<p class="no-data">没有队列条目</p>';
    }

    // 副本明细每张复制表一行 — 在大型SharedMergeTree集群上
    // 有25 000行。上面的图表使用每一行；
    // 表格一次显示50行。行按absolute_delay DESC、
    // is_readonly DESC到达，因此第1页是需要关注的副本。
    const REPLICA_PAGE=50;
    let rpage=0;
    const rcols=['database','table','is_readonly','is_session_expired','is_leader',
       'queue_size','inserts_in_queue','merges_in_queue','future_parts',
       'parts_to_check','absolute_delay','active_replicas','total_replicas'];
    const rclass=r=>r.is_readonly?'error-row':(Number(r.absolute_delay||0)>60?'alert-row':'');
    const attention=rows.filter(r=>rclass(r)).length;
    document.getElementById('replicas-scope').textContent=
      rows.length+' 张复制表（此服务器）'
      +(attention?' — '+attention+' 个只读或落后超过 60 秒，已排在前面':' — 没有只读或落后的副本')
      +(rows.length>REPLICA_PAGE?'；每页 '+REPLICA_PAGE+' 个，按延迟排序。':'。');
    function renderReplicas(){
      const start=rpage*REPLICA_PAGE;
      renderTable('tbl-replicas',rows.slice(start,start+REPLICA_PAGE),rcols,rclass);
      const pg=document.getElementById('replicas-pagination');
      const total=Math.ceil(rows.length/REPLICA_PAGE);
      if(total<=1){pg.innerHTML='';return;}
      let h='';
      if(rpage>0) h+='<button onclick="window._replicaPg('+(rpage-1)+')">&#9664;</button>';
      h+='<span class="cur">'+(rpage+1)+' / '+total+'</span>';
      if(rpage<total-1) h+='<button onclick="window._replicaPg('+(rpage+1)+')">&#9654;</button>';
      pg.innerHTML=h;
    }
    window._replicaPg=function(pp){rpage=pp;renderReplicas();};
    renderReplicas();
  })();

  // ── Keeper健康 ───────────────────────────────────────────────────────────
  //
  // 与alerts/keeper_health.yaml和skill的HC-3.8相同的判定规则，因此

  // ── 后台池 ───────────────────────────────────────────────────────────────
  // 每个池一个图表，每个都以池大小作为虚线参考：
  // 问题在于"有多少小时接近该大小"，而不是水平
  // 本身。达到或超过大小90 %的小时数会在提示框
  // 和卡片标题中统计；最近24小时内少于3个则不标记。
  {
    const ph=DATA.pools_hourly||[], sizes=DATA.pool_sizes||{};
    const N=v=>Number(v||0), T=t=>Date.parse(String(t).replace(' ','T')+'Z');
    if(!ph.length){
      document.getElementById('pools-charts').innerHTML='<p class="no-data">此服务器上没有 metric_log（池占用来自 CurrentMetric_Background*PoolTask）</p>';
    }else{
      const labels=ph.map(r=>r.time);
      const warn=themeVar('--status-warning')||C[2];
      [['fetch','fetch_tasks',0.9],['schedule','schedule_tasks',0.95],['merge','merge_tasks',0.9]].forEach(([k,col,pct])=>{
        const vals=ph.map(r=>N(r[col])), size=N(sizes[k]);
        // 最近24个CLOCK小时，锚定在最新样本上：metric_log
        // 有间隙（停机、某小时被禁用），因此最近24行可能回溯
        // 数天，并标记实时告警（now() - 24 h）认为干净的池。
        // 两边都按UTC解析，使浏览器时区抵消。
        // 锚点：采集时服务器的now()（pools_now）；没有它的旧
        // payload回退到最新样本。
        const anchor=DATA.pools_now?T(DATA.pools_now):T(labels[labels.length-1]);
        const cut=anchor-24*3600e3;
        const last24=ph.filter(r=>T(r.time)>cut).map(r=>N(r[col])), sat=size?last24.filter(v=>v>=pct*size).length:0;
        const el=document.getElementById('chart-pool-'+k);
        const title=el.parentElement.parentElement.querySelector('h3');
        if(size) title.textContent+=' — 大小 '+size+(sat>=3?' — 最近 '+last24.length+' 小时中有 '+sat+' 小时饱和':'');
        if(sat>=3) el.parentElement.parentElement.classList.add('alert-card');
        const ds=[{label:'每小时平均任务数（各副本最大值）',data:vals,borderColor:sat>=3?warn:C[0],backgroundColor:sat>=3?warn:C[0],tension:0.2,pointRadius:0,borderWidth:2}];
        if(size) ds.push({label:'池大小',data:vals.map(()=>size),borderColor:OTHER,borderDash:[6,4],pointRadius:0,borderWidth:1});
        mkChart(el,{type:'line',data:{labels,datasets:ds},
          options:{responsive:true,maintainAspectRatio:false,interaction:{mode:'index',intersect:false},
            plugins:{legend:{position:'top'}},
            scales:{x:{ticks:{maxTicksLimit:10,maxRotation:45}},y:{beginAtZero:true,suggestedMax:size||undefined}}}});
      });
    }
  }

  // dashboard、告警和预检永远不会对某一小时产生分歧。
  (function(){
    const rows=DATA.keeper_metric_hourly||[];
    if(!rows.length)return;
    document.getElementById('sec-keeper').style.display='';
    document.getElementById('nav-keeper').style.display='';
    document.getElementById('keeper-health-body').style.display='';
    document.getElementById('keeper-connections-block').style.display='';
    const N=v=>Number(v||0);
    const labels=rows.map(r=>r.time);
    const tx=rows.map(r=>N(r.transactions)), hw=rows.map(r=>N(r.hw_exceptions));
    // 中位数 = 偏上的中间样本，与
    // alerts/keeper_health.yaml中的quantileExactHigh(0.5)和
    // inspect_bundle.py中的tx_vals[len//2]使用相同估计器。cloud模式下这里的行
    // 跨副本合池（keeperMetricSQL只按小时分组），而告警按
    // hostName()分组 — 因此判定可能不同，单副本的
    // 故障可能在此面板中被平均掉。已记录为后续按副本
    // 面板；在此之前以告警为准。
    const sorted=[...tx].sort((a,b)=>a-b);
    const med=sorted[Math.floor(sorted.length/2)]||0;
    const verdict=rows.map((r,i)=>{
      const pct=med?100*tx[i]/med:null;
      if(pct===null)return hw[i]>1000?'exceptions, no traffic baseline':'ok';
      if(hw[i]>1000&&pct<50)return 'UNAVAILABLE';
      if(hw[i]>1000)return 'blip';
      if(pct<10&&i>0&&i<rows.length-1)return 'idle/disconnected';
      return 'ok';
    });
    const summary=['UNAVAILABLE','blip','idle/disconnected'].map(v=>{
      const hrs=labels.filter((_,i)=>verdict[i]===v);
      return {verdict:v,hours:hrs.length,
        first:hrs[0]||'—',last:hrs[hrs.length-1]||'—',
        meaning:v==='UNAVAILABLE'?'> 每小时 1000 次以上硬件异常且流量低于中位数 50% — 会话过期，工作无法完成'
               :v==='blip'?'> 每小时 1000 次以上硬件异常且流量 ≥ 中位数 50% — 会话丢失后重新建立'
               :'流量低于中位数 10% 且无异常 — 服务器空闲，或没有 Keeper 会话（检查 ZooKeeperSession）'};
    }).filter(x=>x.hours>0);
    renderTable('tbl-keeper-verdict',
      summary.length?summary:[{verdict:'ok',hours:rows.length,first:labels[0],last:labels[labels.length-1],
        meaning:'没有任何小时出现 > 1000 次硬件异常；中位流量 '+fmt(med)+' 事务/小时'}],
      ['verdict','hours','first','last','meaning'],
      r=>r.verdict==='UNAVAILABLE'?'error-row':(r.verdict==='blip'?'alert-row':''));
    // 两个共享x标签的单轴图表（双轴会虚构
    // 相关性）：流量为折线并画出中位数参考，
    // 异常为按时段判定着色的柱。
    const crit=themeVar('--status-critical')||C[1], warn=themeVar('--status-warning')||C[2];
    const bars=verdict.map(v=>v==='UNAVAILABLE'?crit:(v==='blip'?warn:OTHER));
    const verdictTip={callbacks:{afterBody:items=>{
      const i=items[0].dataIndex;
      return [' 判定：'+verdict[i]+(med?'（中位流量的 '+Math.round(100*tx[i]/med)+'%）':'')];}}};
    mkChart(document.getElementById('chart-keeper-traffic'),{
      type:'line',
      data:{labels,datasets:[
        {label:'事务/小时',data:tx,borderColor:C[0],backgroundColor:C[0],tension:0.2,pointRadius:0,borderWidth:2},
        {label:'7 天中位数',data:tx.map(()=>med),borderColor:OTHER,borderDash:[6,4],pointRadius:0,borderWidth:1}]},
      options:{responsive:true,maintainAspectRatio:false,interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'},tooltip:verdictTip},
        scales:{x:{ticks:{maxTicksLimit:14,maxRotation:45}},y:{beginAtZero:true}}}
    });
    mkChart(document.getElementById('chart-keeper-exceptions'),{
      type:'bar',
      data:{labels,datasets:[{label:'硬件异常/小时',data:hw,backgroundColor:bars,borderColor:bars,borderWidth:1}]},
      options:{responsive:true,maintainAspectRatio:false,interaction:{mode:'index',intersect:false},
        plugins:{legend:{display:false},tooltip:verdictTip},
        scales:{x:{ticks:{maxTicksLimit:14,maxRotation:45}},y:{beginAtZero:true}}}
    });
    // keeper_wait_available是Go侧hasColumn()的判定。key缺失
    // （旧bundle）时回退为将该列视为存在，因此消息
    // 绝不会对缺少该标志的数据声称"没有Keeper请求"。
    const waitCol=DATA.keeper_wait_available!==false;
    if(waitCol&&rows.some(r=>N(r.wait_us)>0)){
      const lat=rows.map((r,i)=>tx[i]?N(r.wait_us)/tx[i]/1000:0);
      mkChart(document.getElementById('chart-keeper-latency'),{
        type:'line',
        data:{labels,datasets:[{label:'平均 ms/请求',data:lat,borderColor:C[1],backgroundColor:C[1],tension:0.2,pointRadius:0,borderWidth:2}]},
        options:{responsive:true,maintainAspectRatio:false,plugins:{legend:{display:false}},
          scales:{x:{ticks:{maxTicksLimit:10,maxRotation:45}},y:{beginAtZero:true}}}
      });
    }else{
      document.getElementById('chart-keeper-latency').parentElement.innerHTML='<p class="no-data">'+(waitCol
        ?'此窗口内没有记录到 Keeper 等待时间 — 此服务器没有完成任何 Keeper 请求（不是复制/共享部署，或从未联系过 Keeper）'
        :'此版本未导出 ZooKeeperWaitMicroseconds')+'</p>';
    }
    const er=DATA.keeper_errors_hourly||[];
    document.getElementById('keeper-errors-source').textContent='来源：'+(DATA.keeper_errors_source||'');
    if(er.length){
      const d=pivot(er,'time','code_name','count');
      stackWithGaps(d);
      mkChart(document.getElementById('chart-keeper-errors'),{
        type:'bar',data:d,
        options:{responsive:true,maintainAspectRatio:false,interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'}},
          scales:{x:{stacked:true,ticks:{maxTicksLimit:10,maxRotation:45}},y:{stacked:true,beginAtZero:true}}}
      });
    }else{
      document.getElementById('chart-keeper-errors').parentElement.innerHTML='<p class="no-data">最近 7 天没有 999 / 242 / 319 / 571 / 252</p>';
    }
    const cn=DATA.keeper_connection||[];
    if(cn.length){
      renderTable('tbl-keeper-connection',cn,null,r=>String(r.is_expired)==='1'?'error-row':'');
    }else{
      document.getElementById('tbl-keeper-connection').innerHTML='<p class="no-data">system.zookeeper_connection 不可用（ClickHouse &lt; 23.8）</p>';
    }
  })();

  // Keeper成员：每个成员自报的信息（keeper/*.txt），
  // 预检的判定由Go中的相同规则计算。它有自己的
  // 可见性：bundle可以携带这些文件而不带每小时指标。
  (function(){
    const ms=DATA.keeper_members||[];
    if(!ms.length)return;
    document.getElementById('sec-keeper').style.display='';
    document.getElementById('nav-keeper').style.display='';
    document.getElementById('keeper-members-block').style.display='';
    const ll=DATA.keeper_leaderless||[];
    if(ll.length){
      document.getElementById('keeper-leaderless').innerHTML='<div class="alert-err-msg">⛔ '
        +ll.map(e=>esc(e==='default'?'Keeper':'集合 '+e)).join(', ')
        +'：此 bundle 中没有任何成员在提供服务 — 成员之间没有 leader；bundle 之外的成员仍可能构成法定人数（HC-3.18）</div>';
    }
    const N=v=>v==null?'—':esc(String(v));
    const B=v=>{if(v==null)return '—';let n=Number(v),u=['B','KiB','MiB','GiB','TiB'],i=0;while(n>=1024&&i<u.length-1){n/=1024;i++;}return esc((i?n.toFixed(2):String(n))+' '+u[i]);};
    const mark=f=>'<span class="'+({critical:'err-badge',warning:'warn-badge',info:'ok-badge'}[f.severity]||'warn-badge')
      +'" title="'+esc(f.check+': '+f.text)+'">'+(f.severity==='critical'?'⛔':f.severity==='warning'?'⚠':'ℹ')+'</span>';
    const pick=(m,fn)=>(m.flags||[]).filter(fn).map(mark).join(' ');
    const rows=ms.map(m=>{
      const behind=(m.flags||[]).some(f=>f.check.startsWith('HC-3.19'));
      return {
        member:esc(m.member), ensemble:esc(m.ensemble||'?'),
        state:esc(m.state)+' '+pick(m,f=>f.check.startsWith('HC-3.18')),
        role:esc(m.role||'—'), version:esc(m.version||'—'),
        'Raft log first → last':(m.first_log_idx==null&&m.last_log_idx==null)?'—'
          :N(m.first_log_idx)+' → '+N(m.last_log_idx)+(behind?' '+pick(m,f=>f.check.startsWith('HC-3.19'))+' <b>needs a full snapshot</b>':''),
        'committed / leader':m.last_committed_log_idx==null?'—':N(m.last_committed_log_idx)+' / '+N(m.leader_committed_log_idx),
        'latest snapshot':B(m.snapshot_size)+' '+pick(m,f=>f.check.startsWith('HC-3.20/')),
        'chunk size':N(m.snapshot_transfer_chunk_size),
        'snapshot format':N(m.write_snapshot_version)+' '+pick(m,f=>f.check==='HC-3.20'),
        'on disk (snapshots / log)':m.snapshot_dir_size==null?'—':B(m.snapshot_dir_size)+' / '+B(m.log_dir_size)
      };
    });
    const cols=['member','ensemble','state','role','version','Raft log first → last','committed / leader','latest snapshot','chunk size','snapshot format','on disk (snapshots / log)'];
    renderTable('tbl-keeper-members',rows,cols,r=>r['Raft log first → last'].indexOf('needs a full snapshot')>=0?'error-row':'',cols);
  })();

  // ── 磁盘使用 ─────────────────────────────────────────────────────────────
  (function(){
    const rows=DATA.disks||[];
    const WARN_PCT=15, CRIT_PCT=5;
    if(rows.length){
      mkChart(document.getElementById('chart-disk-usage'),{
        type:'bar',
        data:{
          // cloud模式下system.disks会扇出，因此同一磁盘
          // 每个副本出现一次（3副本上测得11个磁盘 → 33行）。
          // 用短主机名限定标签，与下面表格现在显示的
          // hostname列一致 — 否则图表就是11个重复标签下的
          // 33根柱子。
          labels:rows.map(r=>r.hostname?r.name+' @ '+String(r.hostname).split('.')[0]:r.name),
          datasets:[
            {label:'已用',data:rows.map(r=>Number(r.total_space||0)-Number(r.free_space||0)),
              backgroundColor:rows.map(r=>Number(r.free_pct||100)<CRIT_PCT?STATUS.critical:Number(r.free_pct||100)<WARN_PCT?STATUS.warning:C[0]),
              borderColor:rows.map(r=>Number(r.free_pct||100)<CRIT_PCT?STATUS.critical:Number(r.free_pct||100)<WARN_PCT?STATUS.warning:C[0]),
              borderWidth:1,borderRadius:4},
            {label:'可用',data:rows.map(r=>Number(r.free_space||0)),
              // 可用空间是剩余量，不是"好"状态 — 弱化灰色。
              backgroundColor:OTHER,borderColor:OTHER,borderWidth:1,borderRadius:4}
          ]
        },
        options:{
          responsive:true,maintainAspectRatio:false,
          interaction:{mode:'index',intersect:false},
          plugins:{legend:{position:'top'},tooltip:{callbacks:{
            label:ctx=>{
              const r=rows[ctx.dataIndex];
              const host=r.hostname?' ('+String(r.hostname).split('.')[0]+')':'';
              if(ctx.datasetIndex===0)return' 已用：'+r.total_space_human+' 总量，'+r.free_pct+'% 可用'+host;
              return' 可用：'+r.free_space_human+host;
            }
          }}},
          scales:{x:{stacked:true},y:{stacked:true,beginAtZero:true,
            ticks:{callback:v=>v>=1e12?(v/1e12).toFixed(1)+'T':v>=1e9?(v/1e9).toFixed(1)+'G':v>=1e6?(v/1e6).toFixed(1)+'M':v>=1e3?(v/1e3).toFixed(0)+'K':v}}}
        }
      });
    }
    renderTable('tbl-disks',rows,
      ['hostname','name','type','free_space_human','total_space_human','free_pct','path'],
      r=>Number(r.free_pct||100)<CRIT_PCT?'error-row':Number(r.free_pct||100)<WARN_PCT?'alert-row':'');
  })();

  // ── 服务器错误 ───────────────────────────────────────────────────────────
  (function(){
    const rows=(DATA.server_errors||[]).slice(0,20);
    if(rows.length){
      mkChart(document.getElementById('chart-server-errors'),{
        type:'bar',
        data:{
          labels:rows.map(r=>r.name||'错误码 '+r.code),
          datasets:[{label:'总次数（自重启以来）',
            data:rows.map(r=>r.value),
            backgroundColor:C[0],borderColor:C[0],borderWidth:1,borderRadius:4}]
        },
        options:{indexAxis:'y',responsive:true,maintainAspectRatio:false,
          plugins:{legend:{display:false},tooltip:{callbacks:{
            afterLabel:ctx=>{
              const r=rows[ctx.dataIndex];
              return['错误码：'+r.code,'最后出现：'+r.last_error_time,'消息：'+(r.last_error_message||'').slice(0,120)];
            }
          }}},
          scales:{x:{beginAtZero:true}}}
      });
    } else {
      document.getElementById('chart-server-errors').parentElement.innerHTML='<p class="no-data">没有记录到服务器错误</p>';
    }

    renderTable('tbl-high-parts',DATA.high_part_count||[],
      ['database','table','partition_id','parts_count'],
      r=>Number(r.parts_count||0)>=500?'error-row':Number(r.parts_count||0)>=300?'alert-row':'');

    renderTable('tbl-ttl-activity',DATA.ttl_activity||[],
      ['day','event_type','merge_reason','events','total_size']);
  })();

  // ── 日志 ──────────────────────────────────────────────────────────────────
  //
  // 可在页面内搜索的有界结构化行；完整原始文件
  // 链接在“已采集文件”下。分页是因为行数上限约束的是
  // payload而不是DOM — 1000行<tr>正是让页面爬行的原因。
  (function(){
    const rows=DATA.text_log||[];
    if(!rows.length) return;
    document.getElementById('sec-logs').style.display='';
    document.getElementById('nav-logs').style.display='';

    const cap=DATA.text_log_row_cap||rows.length;
    document.getElementById('logs-scope').textContent=
      '来自 system.text_log 的 Warning 及更严重级别（Warning、Error、Critical、Fatal），最近 24 小时，最新在前'
      +(rows.length>=cap?' — 上限为最近 '+cap+' 行；完整原始文件见“已采集文件”。':'。');

    const sel=document.getElementById('log-level-filter');
    [...new Set(rows.map(r=>r.level))].sort().forEach(l=>{
      sel.innerHTML+='<option value="'+esc(l)+'">'+esc(l)+'</option>';
    });

    const PAGE=100;
    let filtered=rows.slice(), page=0;
    const BADGE={Fatal:'err-badge',Critical:'err-badge',Error:'err-badge',Warning:'warn-badge'};
    // 级别是状态：状态色阶加上文字本身，绝不只靠颜色。
    const cell=r=>({
      event_time:r.event_time,
      level:'<span class="'+(BADGE[r.level]||'warn-badge')+'">'+esc(r.level)+'</span>',
      logger_name:r.logger_name,
      message:r.message
    });

    function render(){
      const cnt=document.getElementById('log-count');
      const pg=document.getElementById('log-pagination');
      cnt.textContent=filtered.length+' / '+rows.length+' 条';
      if(!filtered.length){
        document.getElementById('tbl-logs').innerHTML='<p class="no-data">没有匹配的条目</p>';
        pg.innerHTML=''; return;
      }
      const start=page*PAGE;
      renderTable('tbl-logs',filtered.slice(start,start+PAGE).map(cell),
        ['event_time','level','logger_name','message'],null,['level']);
      const total=Math.ceil(filtered.length/PAGE);
      if(total<=1){pg.innerHTML='';return;}
      let h='';
      if(page>0) h+='<button onclick="window._logPg('+(page-1)+')">&#9664;</button>';
      h+='<span class="cur">'+(page+1)+' / '+total+'</span>';
      if(page<total-1) h+='<button onclick="window._logPg('+(page+1)+')">&#9654;</button>';
      pg.innerHTML=h;
    }
    window._logPg=function(pp){page=pp;render();};
    window.logsFilter=function(){
      const q=(document.getElementById('log-search').value||'').toLowerCase();
      const lvl=document.getElementById('log-level-filter').value;
      filtered=rows.filter(r=>{
        if(lvl && r.level!==lvl) return false;
        if(q) return String(r.message||'').toLowerCase().includes(q)
                 || String(r.logger_name||'').toLowerCase().includes(q);
        return true;
      });
      page=0; render();
    };
    render();
  })();

  // ── 模式关系图 ───────────────────────────────────────────────────────────
  //
  // 仅当生成器写入了schema_graph.html时存在。框架的src
  // 在首次点击时（来自导航链接或按钮）赋值，绝不在
  // 加载时，因此不打开标签页的读者不会额外下载任何内容。
  (function(){
    const sg=DATA.schema_graph;
    if(!sg||!sg.file) return;
    document.getElementById('sec-schema').style.display='';
    document.getElementById('nav-schema').style.display='';
    const bits=[sg.tables+' 张表', sg.databases+' 个数据库'];
    if(sg.mvs) bits.push(sg.mvs+' 个 materialized view');
    if(sg.refreshable) bits.push(sg.refreshable+' 个可刷新视图');
    if(sg.dictionaries) bits.push(sg.dictionaries+' 个字典');
    document.getElementById('schema-count').textContent=bits.join(' · ');
    document.getElementById('schema-note').textContent='以交互方式展示表之间数据流向的关系图，采集时由 system.tables、system.columns、system.dictionaries 和 system.view_refreshes 构建（不含 system 数据库）。';
    document.getElementById('schema-open').href=sg.file;
    const frame=document.getElementById('schema-frame');
    const wrap=document.getElementById('schema-frame-wrap');
    const btn=document.getElementById('schema-load');
    let loaded=false;
    function load(){
      if(loaded) return;
      loaded=true;
      frame.src=sg.file;
      wrap.style.display='';
      btn.textContent='已加载';
      btn.disabled=true;
    }
    btn.addEventListener('click', load);
    document.getElementById('nav-schema').addEventListener('click', load);
  })();

  // ── 已采集文件 ───────────────────────────────────────────────────────────
  //
  // 是bundle其余部分的索引，而不是副本。本轮写入的每个
  // 产物都得到一行和一个相对链接；由浏览器打开文件。
  (function(){
    const files=DATA.bundle_files||[];
    if(!files.length) return;
    document.getElementById('sec-files').style.display='';
    document.getElementById('nav-files').style.display='';

    const total=files.reduce((a,f)=>a+Number(f.bytes||0),0);
    const human=b=>{
      const u=['B','KiB','MiB','GiB','TiB']; let i=0; b=Number(b)||0;
      while(b>=1024 && i<u.length-1){b/=1024;i++;}
      return (i?b.toFixed(1):b)+' '+u[i];
    };
    document.getElementById('files-scope').textContent=
      files.length+' 个文件，共 '+human(total)+'。';

    const sel=document.getElementById('file-group-filter');
    // ''是'All folders'哨兵，因此根组以'/'作为
    // option值，否则选择'(bundle root)'会什么都不过滤。
    [...new Set(files.map(f=>f.group))].sort().forEach(g=>{
      sel.innerHTML+='<option value="'+esc(g||'/')+'">'+esc(g||'（bundle 根目录）')+'</option>';
    });

    let filtered=files.slice();
    // 链接是本文件构建的标记；其中的文件名已转义，
    // 每个路径段都经过百分号编码，因此名称中的空格、'#'或'?'
    // 不会破坏href（encodeURI不处理'#'/'?' — 它们会
    // 变成虚假的fragment/query，并在解压的bundle上404）。
    const row=f=>({
      file:'<a href="'+f.href.split('/').map(encodeURIComponent).join('/')+'" target="_blank" rel="noopener">'+esc(f.file)+'</a>',
      kind:f.kind||'\u2014',
      size:f.size
    });

    function render(){
      document.getElementById('file-count').textContent=
        filtered.length+' / '+files.length+' 个文件';
      renderTable('tbl-files',filtered.map(row),['file','kind','size'],null,['file']);
    }
    window.filesFilter=function(){
      const q=(document.getElementById('file-search').value||'').toLowerCase();
      const g=document.getElementById('file-group-filter').value;
      filtered=files.filter(f=>{
        if(g!=='' && f.group!==(g==='/'?'':g)) return false;
        return !q || String(f.file).toLowerCase().includes(q);
      });
      render();
    };
    render();
  })();

  // ── 异步插入（仅非gov） ──────────────────────────────────────────────────
  (function(){
    const rows=DATA.async_inserts||[];
    if(!rows.length)return;
    document.getElementById('sec-async-inserts').style.display='';
    document.getElementById('nav-async-inserts').style.display='';
    const d=pivot(rows,'hour','status','flushes');
    stackWithGaps(d);
    mkChart(document.getElementById('chart-async-inserts'),{
      type:'bar',data:d,
      options:{responsive:true,maintainAspectRatio:false,
        interaction:{mode:'index',intersect:false},
        plugins:{legend:{position:'top'}},
        scales:{x:{stacked:true,ticks:{maxRotation:45}},y:{stacked:true,beginAtZero:true}}}
    });
    renderTable('tbl-async-inserts',rows,
      ['hour','status','flushes','total_rows','total_bytes','avg_flush_ms']);
  })();
});
</script>
</body>
</html>`

// nonNil使空列表在模板中保持为JSON []而不是null。
func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}
