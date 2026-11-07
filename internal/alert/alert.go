// Package alert 提供用于 ClickHouse 诊断的 YAML 驱动告警系统。
//
// alert rule（告警规则）位于 .yaml 文件中（参见仓库根目录的 alerts/ 目录）。
// 每条规则定义一个只读 SELECT 查询；如果查询返回任何行，告警就会触发，
// 这些行会呈现在 HTML dashboard 中。
//
// YAML 规则格式
// ──────────────
//
//	name:        unique_snake_case_id
//	title:       "人类可读的标题"
//	severity:    critical | warning | info        （默认：warning）
//	description: |
//	  多行说明，解释该告警的含义
//	  以及应当如何处理。
//	tags:
//	  - mutations
//	  - performance
//	modes: [cloud]                                 （可选；默认所有模式）
//	query: |
//	  SELECT database, table, mutation_id,
//	         dateDiff('hour', create_time, now()) AS hours_running,
//	         parts_to_do
//	  FROM {sys.mutations}                 -- 占位符，见下文
//	  WHERE parts_to_do > 0
//	    AND dateDiff('hour', create_time, now()) > 3
//	  ORDER BY hours_running DESC
//	message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h"
//
//	modes 把规则限制在其查询有意义的运行模式中 ——
//	`[cloud]` 用于需要 clusterAllReplicas 扇出的 per-replica 比较，
//	`[onprem, gov]` 用于 SharedMergeTree 所没有的 ReplicatedMergeTree
//	replication queue（复制队列）。在其他模式中，规则会被报告为不适用（skipped），
//	绝不算作出错，也绝不计为已检查。
//
//	根规则只应使用最旧受支持服务器（22.8）上可用的列/语法；更新的内容
//	请放在版本子目录中做门控。例如 system.mutations.is_killed（24.1+）
//	位于 alerts/24.1.1.0/mutation_running_too_long.yaml，而不是根规则中。
//
// system 表占位符
// ───────────────
//
//	在查询中使用 {sys.<table>}，而不是硬编码表路径。
//	评估器会根据运行模式替换为正确的引用：
//
//	  {sys.query_log}  →  system.query_log                                  （onprem / gov）
//	                   →  clusterAllReplicas(default, system.query_log)     （cloud，per-replica）
//	  {sys.parts}      →  system.parts                                      （所有模式）
//
//	内容在各 replica 间共享的表（parts、tables、replicas、replication_queue、
//	mutations、detached_parts、columns、databases）即使在 cloud 模式下也直接查询：
//	每个 replica 看到相同的行，因此 clusterAllReplicas 会造成重复。
//	（system.dictionaries 故意不在该列表中 —— 它的运行时状态是 per-replica 的；
//	见 internal/query/template.go。）
//
// 消息模板
// ────────
//
//	在 message 字符串中使用 {column_name}；评估器会用每个结果行中的
//	列值进行替换。每一行创建一个告警实例。
//
// 检查
// ────
//
//	check: go:<name> 指定一个 Go 函数（internal/alert/checks_*.go、
//	RegisterCheck），在 collector 写出的文件上评估 —— 该规则不发送任何查询。
//	source: 列出这些文件。query: 保留同样用 SQL 表达的测试，展示给客户现场运行。
//	未采集到的文件会让规则变为不适用，并指明采集器及原因。
//	没有 check: 的规则会向服务器运行它的查询。
//
// 安全性
// ──────
//
//	所有告警查询在执行前都会由 ValidateQueryContent 校验。
//	只接受 SELECT / WITH 查询；INSERT、ALTER、DROP 等会被拒绝并报错，
//	该告警会被跳过。
package alert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/bundle"
	"clickhouse-diagnostic/internal/query"
	"clickhouse-diagnostic/pkg"
)

// 严重级别常量。
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Definition 是单个 .yaml 告警规则文件的 schema。
type Definition struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Severity    string   `yaml:"severity"`
	Description string   `yaml:"description"`
	Query       string   `yaml:"query"`
	Message     string   `yaml:"message"`
	Tags        []string `yaml:"tags"`
	// Modes 把规则限制在这些运行模式（cloud / onprem / gov）。
	// 为空 = 所有模式。如果规则的查询只有在扇出时才有意义
	//（基于 clusterAllReplicas 的 per-replica 比较），就写 `modes: [cloud]`，
	// 在其他模式下报告为 skipped 而不是 errored —— 否则该规则会在每次
	// onprem 运行时以 CLUSTER_DOESNT_EXIST 失败。
	Modes []string `yaml:"modes"`
	// Investigate 列出跟进查询，供客户在规则触发时在自己的集群上运行 ——
	// 即一个 finding（发现）引出的“是哪一个？”（哪个池、哪张表）。
	// 只在 dashboard 中展示，工具从不运行它们；每条都必须像规则查询一样
	// 是只读 SELECT。
	Investigate []InvestigateQuery `yaml:"investigate"`
	// Tier 为 "full" 用于扫描逐表数据的规则（system.parts、detached_parts 等）：
	// essential 采集不会运行它们，并报告为未运行。为空或 "essential"
	// 则在每次采集中都运行 —— 规则很小，漏掉一条的代价比运行一次更大。
	Tier string `yaml:"tier"`
	// Needs 为 "keeper" 用于等待活动 Keeper 会话的规则
	//（system.zookeeper_connection）：在没有 Keeper 会话的服务器上会被跳过，
	// 并给出原因。
	Needs string `yaml:"needs"`
	// Source 和 Check：规则由注册的 Go 函数在已采集的文件上评估
	//（go:<name>，见 checks.go），而不是在服务器上运行它的查询。
	// 此时 Query 只是给客户在自己集群上运行、以看到同样结果的内容。
	Source []string `yaml:"source"`
	Check  string   `yaml:"check"`
}

// InvestigateQuery 是规则文件中的一条跟进查询。
type InvestigateQuery struct {
	Title string `yaml:"title"`
	Query string `yaml:"query"`
}

// Investigation 是 dashboard 所展示的跟进查询：已按模式展开，
// 可直接粘贴到 clickhouse-client 中。
type Investigation struct {
	Title string `json:"title"`
	Query string `json:"query"`
}

// Result 保存评估一条告警规则的结果。
// 它可 JSON 序列化，因此能嵌入 dashboard 的载荷中。
type Result struct {
	Name        string                   `json:"name"`
	Title       string                   `json:"title"`
	Severity    string                   `json:"severity"`
	Description string                   `json:"description"`
	Message     string                   `json:"message"`
	Tags        []string                 `json:"tags"`
	Rows        []map[string]interface{} `json:"rows"`
	FiredAt     string                   `json:"fired_at"`
	Error       string                   `json:"error,omitempty"`
	File        string                   `json:"file"`
	// Skipped 标记一条未运行的规则，因为它在这台服务器上不适用 ——
	// 例如它查询的 system 表不存在（没有崩溃时的 crash_log，
	// 或配置禁用的 text_log/query_log）。被跳过的规则既未触发也未出错；
	// 它绝不能计入 "evaluated"，否则 dashboard 会在检查从未运行时
	// 声称检查通过。
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"skip_reason,omitempty"`
	// DurationMs 是规则查询的实际耗时，用于 execution_log.txt。
	DurationMs int64 `json:"duration_ms,omitempty"`
	// Query 和 Investigate 只在展示结果时设置（触发，或其查询失败）：
	// 规则实际运行的 SQL，以及跟进查询，两者都已按模式展开 ——
	// 也就是客户可以在自己集群上运行以查看证据的内容。
	// 干净的规则两者都不带，因此 dashboard 不会因没有发现任何东西的规则而膨胀；
	// gov 的 alerts_summary.json 从不携带它们（它只包含计数）。
	Query       string          `json:"query,omitempty"`
	Investigate []Investigation `json:"investigate,omitempty"`
}

// Notable 报告该结果是否值得读者注意 ——
// 它匹配了行，或者运行失败。仅用于决定要展示什么。
//
// 刻意不命名为 "Fired"：把错误当作触发，正是那台服务器上十一条规则都因权限
// 失败时却输出 "11 fired" 这一行的原因。计数请使用 Summarize，
// 它会区分 fired / errored / skipped。任何向人类报告数字的地方
// 都必须使用 Summarize，而不是这个。
func (r Result) Notable() bool { return len(r.Rows) > 0 || r.Error != "" }

// FormattedMessages 为每个结果行返回一条 message 字符串，
// 其中的 {column_name} 占位符用该行的值替换。
func (r Result) FormattedMessages() []string {
	out := make([]string, 0, len(r.Rows))
	for _, row := range r.Rows {
		msg := r.Message
		for k, v := range row {
			msg = strings.ReplaceAll(msg, "{"+k+"}", fmt.Sprintf("%v", v))
		}
		out = append(out, msg)
	}
	return out
}

// Evaluator 针对一个 ClickHouse 实例加载并运行告警规则。
type Evaluator struct {
	client *pkg.ClickHouseClient
	mode   string
	// queryFn 设置后会替代 client 来执行辅助探测查询。
	// 它的存在是为了让 cloud 的不适用逻辑 —— 即决定一个危急崩溃告警
	// 是被隐藏还是被暴露 —— 无需实时集群即可做单元测试。
	// 生产环境中为 nil；见 query()。
	queryFn func(string) (string, error)
	// essential：跳过 full tier 的规则（essential 采集）。
	essential bool
	// detachedScope 填充 {detached_scope}（见 query.DetachedScope）。
	detachedScope string
	// keeperDown：以该原因跳过 `needs: keeper` 的规则。
	keeperDown string
	// stop 返回 true 时，跳过所有尚未运行的规则
	//（本次运行的时间预算已用完）。
	stop func() bool
	// bundle 是本次运行采集的数据，供 Go-check 规则读取。
	bundle *bundle.Bundle
}

// WithBundle 把本次运行采集的数据交给 Go-check 规则。
func (ev *Evaluator) WithBundle(b *bundle.Bundle) *Evaluator {
	ev.bundle = b
	return ev
}

// WithKeeperDown 跳过需要活动 Keeper 会话的规则。
func (ev *Evaluator) WithKeeperDown(reason string) *Evaluator {
	ev.keeperDown = reason
	return ev
}

// WithStop 在 stop() 返回 true 后跳过剩余规则。
func (ev *Evaluator) WithStop(stop func() bool) *Evaluator {
	ev.stop = stop
	return ev
}

// WithDetachedScope 像 collector 一样限制 detached part 相关规则。
func (ev *Evaluator) WithDetachedScope(scope string) *Evaluator {
	ev.detachedScope = scope
	return ev
}

// WithEssential 让评估器跳过 full tier 的规则。
func (ev *Evaluator) WithEssential(on bool) *Evaluator {
	ev.essential = on
	return ev
}

// query 在存在注入接缝时通过它运行辅助探测查询，
// 否则使用真实的 client。两者都不可用时返回错误，
// 使调用方倾向于暴露问题，而不是悄悄跳过。
func (ev *Evaluator) query(sql string) (string, error) {
	if ev.queryFn != nil {
		return ev.queryFn(sql)
	}
	if ev.client == nil {
		return "", fmt.Errorf("没有可用的 ClickHouse 客户端")
	}
	return ev.client.ExecuteQueryWithFormat(sql)
}

// NewEvaluator 为给定的连接和模式创建一个新的 Evaluator。
func NewEvaluator(client *pkg.ClickHouseClient, mode string) *Evaluator {
	return &Evaluator{client: client, mode: strings.ToLower(mode)}
}

// expandQuery 用评估器模式对应的正确表路径替换 {sys.<name>} 占位符。
// 委托给共享的模板辅助函数（cloud 共享表列表的唯一事实来源）。
func (ev *Evaluator) expandQuery(sql string) string {
	return query.Apply(sql, query.Vars{Mode: ev.mode, DetachedScope: ev.detachedScope})
}

// RunAll 从 dir 加载每个 .yaml 文件并评估每条规则。
// 如果目录不存在，该调用是空操作（返回 nil）。
//
// dir 遵循与查询目录相同的版本目录约定：名为 ClickHouse 版本
// （例如 "25.4.1.0"）的子目录，在所连接的服务器（serverVersion）至少为该版本时
// 覆盖同名的根规则。
func (ev *Evaluator) RunAll(dir string, serverVersion internal.Version) []Result {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	files, err := query.FindVersionedFiles(dir, serverVersion, ".yaml")
	if err != nil {
		fmt.Printf("[alerts] 无法读取 %s: %v\n", dir, err)
		return nil
	}

	var results []Result
	for _, f := range files {
		started := time.Now()
		r := ev.evalFile(f.FullPath)
		r.DurationMs = time.Since(started).Milliseconds()
		// 在报告的文件名中保留版本覆盖上下文（与 analysis.go 一致），
		// 以便支持工程师分辨是哪个变体触发的 ——
		// 例如 "22.11.1.0/detached_parts_exist.yaml"。
		if f.DirName != "" {
			r.File = f.DirName + "/" + f.Name
		}
		// 在这里用能感知版本的 File 把任何真实失败（读取 / yaml / 查询 / 解析）
		// 暴露一次 —— evalFile 只把它存进 Result.Error，否则永远不会到达 stdout，
		// 这样一条损坏的规则看起来会和健康规则一模一样。安全相关的情况
		// 被跳过：evalFile 已经打印了 "[alert] BLOCKED"。缺表的跳过
		// 不携带 Error，因此会被自动排除。
		if r.Error != "" && !strings.HasPrefix(r.Error, "security:") {
			fmt.Printf("  [alert] ERROR %s: %s\n", r.File, r.Error)
		}
		results = append(results, r)
	}

	evaluated, fired, errored, skipped := Summarize(results)
	fmt.Printf("[alerts] 已评估 %d 条规则：%d fired，%d errored，%d skipped（不适用）\n",
		evaluated, fired, errored, skipped)
	return results
}

// WriteSummaryJSON 把 alerts_summary.json 写入 dir：每条规则一个条目，
// 包含其身份、结果和实例计数 —— 刻意不包含匹配的行。
//
// 在未生成 HTML dashboard 时（gov 模式、--skip-dashboard，或生成失败）写出，
// 因为 dashboard 是告警结果的唯一另一个消费者 —— 没有它，归档就不会记录
// 任何关于已运行、可能已触发规则的信息。
//
// 行内容始终省略：告警的列由规则定义，因此它们经常携带原始的 database/table
// 名称，而 gov 模式在其他所有地方都会对这些名称做哈希。仅凭计数加上规则自身的
// 消息模板，就能在不含标识符的情况下给支持工程师提供信号。
func WriteSummaryJSON(dir string, results []Result, mode string) error {
	type entry struct {
		Name     string `json:"name"`
		Title    string `json:"title"`
		Severity string `json:"severity"`
		File     string `json:"file"`
		State    string `json:"state"` // fired | clean | skipped | error
		Count    int    `json:"instance_count"`
		Reason   string `json:"skip_reason,omitempty"`
		Error    string `json:"error,omitempty"`
		FiredAt  string `json:"checked_at"`
	}
	evaluated, fired, errored, skipped := Summarize(results)
	out := struct {
		Evaluated     int     `json:"evaluated"`
		Fired         int     `json:"fired"`
		Errored       int     `json:"errored"`
		NotApplicable int     `json:"not_applicable"`
		Note          string  `json:"note"`
		Rules         []entry `json:"rules"`
	}{
		Evaluated:     evaluated,
		Fired:         fired,
		Errored:       errored,
		NotApplicable: skipped,
		Note:          summaryNote(mode),
	}
	for _, r := range results {
		e := entry{Name: r.Name, Title: r.Title, Severity: r.Severity, File: r.File,
			Count: len(r.Rows), Reason: r.Reason, Error: r.Error, FiredAt: r.FiredAt}
		switch {
		case r.Skipped:
			e.State = "skipped"
		case r.Error != "":
			e.State = "error"
		case len(r.Rows) > 0:
			e.State = "fired"
		default:
			e.State = "clean"
		}
		out.Rules = append(out.Rules, e)
	}
	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "alerts_summary.json"), blob, 0600)
}

// summaryNote 向打开归档的人说明该文件的范围。在 gov 模式下，
// 省略行是隐私要求；在其他模式下，它只是替代 dashboard 的范围说明。
func summaryNote(mode string) string {
	if strings.ToLower(mode) == "gov" {
		return "gov 模式：仅包含规则结果与实例计数；匹配的行已省略，" +
			"因为这些列可能包含未脱敏的标识符"
	}
	// 不要让读者去看 dashboard.html：这个文件之所以存在，正是因为
	// 该产物不在本归档中，所以“见 dashboard”会读成缺失的附件。
	// 应给出一个可执行的建议。
	return "仅包含规则结果与实例计数；本次运行未生成 HTML dashboard，因此写出此文件。匹配的行已省略——" +
		"如需行级详情，请去掉 --skip-dashboard 重新运行（或解决上方的 dashboard 生成错误）"
}

// Summarize 统计结果以供报告。它是 RunAll 的汇总和 cmd/main.go 完成行
// 的唯一事实来源。
//
// 有三种区分很重要，因为对阅读归档的人来说，它们各自意味着不同的东西：
//   - "evaluated" 只统计真正给出了答案的规则，因此同时排除 skipped 和
//     errored 的规则。一条从未运行的规则 —— 无论是因为它的表在这里不存在，
//     还是因为查询失败 —— 都不能被读作检查通过。
//   - "fired" 只统计匹配到行的规则，即真正的 findings。
//   - "errored" 与 fired 分开计数。把两者合并会严重误导：一个缺少 SELECT
//     授权的用户会让所有规则失败，汇总就会读成 "11 fired" —— 十一个表面上的
//     生产问题，实际上是一个权限问题（这是针对受限的 ClickHouse Cloud 用户
//     观察到的，也正是 "11 checked" 被证明高估了实际验证内容的地方）。
func Summarize(results []Result) (evaluated, fired, errored, skipped int) {
	for _, r := range results {
		switch {
		case r.Skipped:
			skipped++
		case r.Error != "":
			errored++
		case len(r.Rows) > 0:
			fired++
		}
	}
	return len(results) - skipped - errored, fired, errored, skipped
}

// knownModes 是规则可在 modes: 中指定的封闭集合。它放在这里、紧挨着门控，
// 而不是从 cmd（它负责 CLI 的规范化）导入，这样 alert 包就能被测试和
// dashboard 导入而不产生循环依赖。
var knownModes = map[string]bool{"cloud": true, "onprem": true, "gov": true}

// validateModes 拒绝指定了不存在模式的 modes: 列表。
// 规则来自用户可选的 -alerts-dir，因此像 `modes: [on-prem]` 这样的拼写错误
// 必须作为损坏的定义暴露出来，而不是在每次运行中都悄悄跳过该规则 ——
// 否则门控会永远把它读作“某个其他模式”。
func (d Definition) validateModes() error {
	for _, m := range d.Modes {
		if !knownModes[strings.ToLower(strings.TrimSpace(m))] {
			return fmt.Errorf("modes: 未知模式 %q（已知：cloud, onprem, gov）", m)
		}
	}
	return nil
}

// appliesTo 报告规则是否在 mode 下运行：Modes 为空时适用于所有模式，
// 否则仅适用于列出的模式（不区分大小写，容忍空白）。
func (d Definition) appliesTo(mode string) bool {
	if len(d.Modes) == 0 {
		return true
	}
	for _, m := range d.Modes {
		if strings.EqualFold(strings.TrimSpace(m), strings.TrimSpace(mode)) {
			return true
		}
	}
	return false
}

// isMissingTable 报告 err 是否为 ClickHouse 的“表不存在”错误
// （code 60 / UNKNOWN_TABLE）。它刻意不匹配 UNKNOWN_IDENTIFIER（缺列）——
// 后者必须保持为真正的错误，这样版本门控检测器仍能捕获 is_killed 那类 bug。
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNKNOWN_TABLE")
}

// reMissingTable 从 UNKNOWN_TABLE 消息中提取表名，以便让集群范围的存在性检查
// 针对该表进行。
//
// 措辞在不同版本间并不稳定 —— 22.8 输出 "Table
// system.crash_log doesn't exist."，而 26.2 输出 "... does not
// exist." —— 因此两种拼写都接受。（这是吃了苦头才发现的：只匹配旧形式，
// 会让 cloud 探测在 26.2 上悄悄无法到达。）
var reMissingTable = regexp.MustCompile(`Table ([A-Za-z_][\w.]*) (?:doesn'?t|does not) exist`)

// notApplicable 报告 err 是否表示“该规则不适用于这台服务器”
// （相对于“该规则损坏了”）。
//
// 单节点模式：本地 UNKNOWN_TABLE 即为定论。
//
// Cloud：per-replica 表通过 clusterAllReplicas 读取，因此 UNKNOWN_TABLE
// 是有歧义的 —— 它可能来自一张到处都不存在的表（确实不适用），
// 也可能来自一张仅在某些 replica 上存在的表，此时把它当作不适用
// 会掩盖一个真实的 finding（system.crash_log 会在发生崩溃的节点上生成）。
// 与其猜测，不如询问：统计拥有该表的 replica 数。为零表示整个集群都不存在，
// 因此跳过是正确的；其他任何答案（包括探测失败）都意味着我们无法排除
// 部分存在，于是错误会被暴露出来。
//
// 在 3 副本的 Cloud 26.2 服务上实测：crash_log 和 text_log 在
// 每个 replica 上都会预先创建，因此扇出返回 0 行而不是 UNKNOWN_TABLE，
// 在那里根本不会走到这条路径。
func (ev *Evaluator) notApplicable(err error) bool {
	if !isMissingTable(err) {
		return false
	}
	if ev.mode != "cloud" {
		return true
	}
	m := reMissingTable.FindStringSubmatch(err.Error())
	if m == nil {
		return false // 无法识别表 → 暴露该错误
	}
	// 保留错误中给出的数据库，而不是假定为 "system"，这样针对
	// mydb.foo 的规则不会去检查 system.foo。
	db, name := "system", m[1]
	if i := strings.LastIndex(name, "."); i >= 0 {
		db, name = name[:i], name[i+1:]
	}
	count := func(where string) (int64, bool) {
		raw, qErr := ev.query(fmt.Sprintf(
			"SELECT count() AS c FROM clusterAllReplicas(default, system.tables) WHERE %s", where))
		if qErr != nil {
			return 0, false
		}
		rows, parseErr := parseJSONCompact(raw)
		if parseErr != nil || len(rows) == 0 {
			return 0, false
		}
		n, convErr := strconv.ParseInt(strings.TrimSpace(fmt.Sprintf("%v", rows[0]["c"])), 10, 64)
		return n, convErr == nil
	}
	// 与 dashboard 探测所用的防权限盲区保护相同：system.tables
	// 会按授权过滤而不报错，因此裸的零可能意味着“没有权限”而不是“不存在”。
	// 我们能查询的数据库总会有一些可见的表，因此在信任那个具体的零之前
	// 先要求这一点。
	if n, ok := count(fmt.Sprintf("database='%s'", db)); !ok || n == 0 {
		return false
	}
	n, ok := count(fmt.Sprintf("database='%s' AND name='%s'", db, name))
	return ok && n == 0
}

// evalFile 加载、校验并执行一个告警规则文件。
func (ev *Evaluator) evalFile(path string) Result {
	now := time.Now().UTC().Format(time.RFC3339)

	// 读取/yaml 失败时没有解析出的定义，因此从文件名推导身份字段 ——
	// 否则 dashboard 会渲染一张无标题的错误卡片（(a.title||a.name) 都为空），
	// 运维人员无法分辨是哪条规则坏了。
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{Name: base, Title: base, Severity: SeverityWarning,
			File: filepath.Base(path), Error: fmt.Sprintf("读取失败: %v", err), FiredAt: now}
	}

	var def Definition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		return Result{Name: base, Title: base, Severity: SeverityWarning,
			File: filepath.Base(path), Error: fmt.Sprintf("yaml 解析失败: %v", err), FiredAt: now}
	}

	r := Result{
		Name:        def.Name,
		Title:       def.Title,
		Severity:    def.Severity,
		Description: def.Description,
		Message:     def.Message,
		Tags:        def.Tags,
		File:        filepath.Base(path),
		FiredAt:     now,
	}
	if r.Severity == "" {
		r.Severity = SeverityWarning
	}

	// 格式错误的 modes: 列表是一条损坏的规则，会像 YAML 错误一样报告 ——
	// 下面的门控绝不能把拼写错误变成静默跳过。
	if def.Tier != "" && def.Tier != "essential" && def.Tier != "full" {
		r.Error = fmt.Sprintf("未知 tier %q（应为 essential 或 full）", def.Tier)
		fmt.Printf("  [alert] ERROR %q: %v\n", def.Name, r.Error)
		return r
	}
	if err := def.validateModes(); err != nil {
		r.Error = err.Error()
		fmt.Printf("  [alert] ERROR %q: %v\n", def.Name, err)
		return r
	}

	// 在接触服务器之前先做模式门控：声明给其他模式的规则在这里就是“不适用”，
	// 与缺表的结果相同 —— 从不运行、从不出错、从不计为已检查。
	if !def.appliesTo(ev.mode) {
		r.Skipped = true
		r.Reason = "在 " + ev.mode + " 模式下不适用（规则模式：" + strings.Join(def.Modes, ", ") + "）"
		fmt.Printf("  [alert] skipped %q (%s)\n", def.Name, r.Reason)
		return r
	}

	// 下面三个门控保护服务器免受实时规则查询的影响。Go check 只读取已采集的
	// 文件：由其数据源是否被采集（在 essential 运行、无 Keeper、超出预算时
	// 会被跳过）来决定。
	live := def.Check == "" || ev.bundle == nil
	if live && ev.stop != nil && ev.stop() {
		r.Skipped = true
		r.Reason = "未运行：本次采集的时间预算已用尽"
		fmt.Printf("  [alert] skipped %q (%s)\n", def.Name, r.Reason)
		return r
	}
	if live && ev.keeperDown != "" && def.Needs == "keeper" {
		r.Skipped = true
		r.Reason = "未运行：需要活动的 Keeper 会话 — " + ev.keeperDown
		fmt.Printf("  [alert] skipped %q (%s)\n", def.Name, r.Reason)
		return r
	}
	if live && ev.essential && def.Tier == "full" {
		r.Skipped = true
		r.Reason = "在 essential 采集模式下未运行（该规则会扫描逐表数据）"
		fmt.Printf("  [alert] skipped %q (%s)\n", def.Name, r.Reason)
		return r
	}

	sql := ev.expandQuery(def.Query)

	// 安全性：告警查询必须是只读 SELECT —— 交给客户运行的跟进查询
	// 也必须如此。带有不安全跟进查询的规则是损坏的：它不会运行，
	// 也不展示任何 SQL。
	if err := query.ValidateQueryContent(sql); err != nil {
		r.Error = fmt.Sprintf("security: %v", err)
		fmt.Printf("  [alert] BLOCKED %q: %v\n", def.Name, err)
		return r
	}
	var followUps []Investigation
	for i, inv := range def.Investigate {
		q := ev.expandQuery(inv.Query)
		if err := query.ValidateQueryContent(q); err != nil {
			r.Error = fmt.Sprintf("security: investigate[%d]: %v", i, err)
			fmt.Printf("  [alert] BLOCKED %q: investigate[%d]: %v\n", def.Name, i, err)
			return r
		}
		followUps = append(followUps, Investigation{Title: inv.Title, Query: strings.TrimSpace(q)})
	}

	// 没有 bundle（check 无法读取的 -output-format）：规则的查询会像
	// check 出现之前那样实时运行。
	if def.Check != "" && ev.bundle != nil {
		return ev.evalCheck(def, r, sql, followUps)
	}

	resp, err := ev.query(sql)
	if err != nil {
		// 缺表意味着该规则在这台服务器上不适用
		//（例如 system.crash_log 只在崩溃后存在；system.text_log
		// / system.query_log 可能被禁用）—— 拼错的表名也是如此，
		// 后者本应由 *_FilesAreValid 测试和评审捕获。
		// 无论哪种情况，这都不是损坏的规则：返回一个 Skipped 结果（未触发、
		// 未出错），这样它既不会触发 "[alert] ERROR" —— 该输出必须保留给
		// 缺列（UNKNOWN_IDENTIFIER）之类的真正损坏 —— 也不会被计为一次
		// 没有发现任何东西的已评估检查。RunAll 会打印真正的错误。
		//
		// 在 cloud 模式下，clusterAllReplicas 扇出会让 UNKNOWN_TABLE
		// 产生歧义，因此 notApplicable() 通过统计真正拥有该表的 replica
		// 来判定 —— 见其文档注释。
		if ev.notApplicable(err) {
			r.Skipped = true
			r.Reason = "表不存在"
			fmt.Printf("  [alert] skipped %q（表不存在）\n", def.Name)
			return r
		}
		r.Error = fmt.Sprintf("查询失败: %v", err)
		r.Query, r.Investigate = sql, followUps // 展示：显示失败的内容
		return r
	}

	rows, err := parseJSONCompact(resp)
	if err != nil {
		r.Error = fmt.Sprintf("解析失败: %v", err)
		return r
	}

	r.Rows = rows
	if len(rows) > 0 {
		fmt.Printf("  [alert] FIRED  %q — %d 行\n", def.Name, len(rows))
		r.Query, r.Investigate = sql, followUps
	}
	return r
}

// parseJSONCompact 把 ClickHouse JSONCompact 响应转换为
// “列名 → 值”映射的切片。
func parseJSONCompact(raw string) ([]map[string]interface{}, error) {
	var res struct {
		Meta []struct {
			Name string `json:"name"`
		} `json:"meta"`
		Data [][]json.RawMessage `json:"data"`
		Rows int                 `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w (%.120s)", err, raw)
	}
	out := make([]map[string]interface{}, 0, res.Rows)
	for _, row := range res.Data {
		rec := make(map[string]interface{}, len(res.Meta))
		for i, col := range res.Meta {
			if i < len(row) {
				var v interface{}
				_ = json.Unmarshal(row[i], &v)
				rec[col.Name] = v
			}
		}
		out = append(out, rec)
	}
	return out, nil
}
