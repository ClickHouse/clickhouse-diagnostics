package query

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SharedSystemTables 列出内容在每个 replica 上都相同的 system.* 表 ——
// 既包括 ClickHouse Cloud（SharedMergeTree、共享存储），也包括自管理
// ReplicatedMergeTree 集群。对这类表套用 clusterAllReplicas 会让每一行
// 按每个 replica 重复一次，产生虚高的计数 / 重复的告警行。
//
// 这是告警评估器、dashboard 生成器以及新的查询分析采集器共用的唯一事实来源。
// 如果你把某张 system 表加入其中某条代码路径，请同步更新这个映射。
//
// 一个显著的例外：system.dictionaries 不在这个列表中，尽管在 CH Cloud 中
// 字典的 DEFINITION 是通过 Keeper 共享的。原因是运行时状态列（status、
// bytes_allocated、query_count、hit_rate）是 per-replica 的 —— 一个在
// replica A 上查询过、但尚未在 replica B 上查询过的字典，在各副本上会显示
// 不同的状态。我们想看到每个 pod 的加载状态，因此让 cloud 模式用
// clusterAllReplicas 包裹；调用方在 SELECT 中加入 hostname()，
// 为每一行标出它所属的 pod。
//
// 这里的 "Shared" 指的是行内容，而不是表是否存在。它只关乎读取一张表时
// 如何避免 clusterAllReplicas 造成的行重复。它与一张表是否存在于每个 replica
// 上无关 —— 按节点记录的 system 日志表（例如 crash_log）在某些 replica 上
// 可能不存在，即使 `tables` 列在这里。dashboard 的 hasTable() 探测
// 会单独处理这种情况。
var SharedSystemTables = map[string]bool{
	"columns":           true,
	"databases":         true,
	"detached_parts":    true,
	"mutations":         true,
	"parts":             true,
	"replicas":          true,
	"replication_queue": true,
	"tables":            true,
}

// SysTable 返回模式对应的 system 表引用。
// 在 cloud 模式下，per-replica 的表（query_log、part_log、text_log、
// errors 等）会用 clusterAllReplicas 包裹，使结果覆盖每个 replica。
// SharedSystemTables 中的表则直接查询。
func SysTable(mode, table string) string {
	if strings.ToLower(mode) == "cloud" && !SharedSystemTables[table] {
		return fmt.Sprintf("clusterAllReplicas(default, system.%s)", table)
	}
	return "system." + table
}

// Vars 保存查询模板的替换值。所有字段都是可选的；
// Apply 只替换对应字段非零的占位符。
//
// 校验由调用方负责 —— Vars 的值会直接拼接进 SQL 字符串。在填充 Vars 之前，
// 请使用包级的 Validate* 辅助函数（或 cmd/main.go 中的等价实现）。
type Vars struct {
	// Mode 选择 ClickHouse 拓扑 —— "cloud" / "onprem" / "gov"。
	// {sys.<table>} 替换需要它来选取正确的引用。
	Mode string

	// QueryID 非空时，把每个 {query_id} 占位符替换为带引号的 UUID
	// 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'。
	QueryID string

	// NormalizedQueryHash 非空时，把每个 {normalized_query_hash}
	// 占位符替换为不带引号的数值。
	NormalizedQueryHash string

	// From / To 非零时，把 {from} / {to} 占位符替换为 UTC 的
	// 带引号 ClickHouse DateTime 字面量。
	From time.Time
	To   time.Time

	// GovSalt 非空时，把 SQL 中的每个 '%salt%' 字面量替换为
	// 客户提供的、用于 gov 模式哈希的 salt。
	GovSalt string

	// MaxLookback 非零且未给出 From 时，为每个长于它的
	// {from:<default>} 设上限（essential 采集：1 天而不是 3 天或 7 天）。
	// 显式给出的 From 始终优先。
	MaxLookback time.Duration

	// DetachedScope / DetachedScopeP 填充 {detached_scope} 和
	// {detached_scope_p}（gov 文件，相关列带 p. 前缀）。为空表示
	// 所有表（"1"）；见 DetachedScope。
	DetachedScope  string
	DetachedScopeP string
}

// Apply 替换 sql 中所有在 vars 里提供了值的占位符。
// 支持的占位符（区分大小写）：
//
//	{sys.<table>}             — 模式对应的 system 表引用
//	{query_id}                — 'uuid'（带引号）
//	{normalized_query_hash}   — 12345（不带引号的数值）
//	{from}, {to}              — 'YYYY-MM-DD HH:MM:SS' UTC（带引号）
//	{from:15d}, {to:now}      — 若给出了该 flag 则用其值，否则用
//	                            查询自身的默认窗口
//	'%salt%'                  — '<gov-salt>'
//
// 对应 Vars 字段为空的占位符会保持原样 —— 随后 executor 的空占位符检查
// 会拒绝发送该查询，防止未绑定的模板泄漏到服务器。
func Apply(sql string, vars Vars) string {
	sql = expandSysPlaceholders(sql, vars.Mode)
	sql = expandWindowPlaceholders(sql, vars.From, vars.To, vars.MaxLookback)
	sql = strings.ReplaceAll(sql, "{detached_scope_p}", scopeOr1(vars.DetachedScopeP))
	sql = strings.ReplaceAll(sql, "{detached_scope}", scopeOr1(vars.DetachedScope))

	if vars.QueryID != "" {
		sql = strings.ReplaceAll(sql, "{query_id}", "'"+vars.QueryID+"'")
	}
	if vars.NormalizedQueryHash != "" {
		sql = strings.ReplaceAll(sql, "{normalized_query_hash}", vars.NormalizedQueryHash)
	}
	if !vars.From.IsZero() {
		sql = strings.ReplaceAll(sql, "{from}", utcLiteral(vars.From))
	}
	if !vars.To.IsZero() {
		sql = strings.ReplaceAll(sql, "{to}", utcLiteral(vars.To))
	}
	if vars.GovSalt != "" {
		sql = strings.ReplaceAll(sql, "'%salt%'", "'"+vars.GovSalt+"'")
	}
	return sql
}

// expandSysPlaceholders 遍历 SQL，把每个 {sys.<table>} 出现处
// 替换为 SysTable(mode, table)。手工实现（不用正则）是因为占位符形态
// 固定且简单 —— 这让本包依赖面保持很小。
func expandSysPlaceholders(sql, mode string) string {
	// 没有 mode 就不展开。用 "" 展开会悄悄表现得像 onprem ——
	// 写进 queries.cloud/ 的第一个 {sys.query_log} 将只采集一个 replica
	// 而不是扇出，而且不会有任何报错。保留占位符则会让 UnboundPlaceholders
	// 拒绝该查询，从而指向接线错误，而不是产出一个错误的 bundle。
	if strings.TrimSpace(mode) == "" {
		return sql
	}
	out := sql
	for {
		start := strings.Index(out, "{sys.")
		if start == -1 {
			return out
		}
		end := strings.Index(out[start:], "}")
		if end == -1 {
			return out
		}
		table := out[start+5 : start+end]
		out = strings.ReplaceAll(out, out[start:start+end+1], SysTable(mode, table))
	}
}

// reBoundPlaceholder 匹配我们已知的占位符形态 —— 由 ASCII 字母、数字、点号和
// 下划线组成的名称，位于花括号之间。它刻意不匹配空的 `{}` 形式，因此像
//
//	'Selected {}/{} parts by partition key, …'
//
// 这样的 ClickHouse 日志格式字符串 —— 它们在某些查询中作为字面值出现 ——
// 会被原样保留。
var rePlaceholder = regexp.MustCompile(`\{[A-Za-z0-9_.]+\}`)

// UnboundPlaceholders 返回 Apply 之后仍留在 sql 中的具名 {…} 占位符。
// 由 executor 用作安全检查：如果某个必需的值（例如 {query_id}）从未绑定，
// SQL 会带着字面量 '{query_id}' 到达服务器，要么解析报错，要么 —— 更糟 ——
// 悄悄返回零行。拒绝该查询更好。
//
// 空的 `{}` 花括号（ClickHouse 日志格式占位符）会被忽略。
// '%salt%' 在此刻意不被标记：queries.gov/ 下的任何 .sql 文件都把它作为标记
// 包含在内，而 executor 只在设置了 GovSalt（gov 模式）时才做替换，
// 因此它在 Apply 之后仍存在是预期行为，在非 gov 模式下不是错误。
func UnboundPlaceholders(sql string) []string {
	// 去掉注释后再扫描。查询文件会在说明文字中合法地提到
	// ClickHouse 宏 —— queries.gov/system.replication_queue.sql 解释说
	// replica_name“习惯上是 {replica} 宏” —— 标记这些会仅因一条注释
	// 就拒绝查询。去掉注释后的副本只用于检测；发送的 SQL 不受影响。
	scan := reLineComment.ReplaceAllString(reBlockComment.ReplaceAllString(sql, " "), " ")

	matches := rePlaceholder.FindAllString(scan, -1)
	// 默认规格格式错误的窗口占位符（例如
	// {from:7dd}）会被 expandWindowPlaceholders 原样保留。由于含有冒号，
	// 它不匹配 rePlaceholder，因此在这里报告它 —— 否则一个拼写错误
	// 就会以字面花括号的形式到达服务器。
	matches = append(matches, reWindowLeftover.FindAllString(scan, -1)...)
	if len(matches) == 0 {
		return nil
	}
	// 稳定去重，使相同的未绑定占位符不会在面向运维人员的警告中重复出现。
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}

// reWindowPlaceholder 匹配自带默认值的时间窗口占位符：
// {from:15d}、{from:12h}、{to:now}。
//
// 过去每个采集查询都把窗口硬编码为字面量。现在默认值随查询本身走，而不是
// 被压平成单一的全局值，因为宽范围扫描的代价因表而异 —— text_log 保留
// 1 天的默认值，而其他日志表用 7 天，未来需要更长或更短范围的查询可以
// 自行声明，而不影响其他查询。--from / --to 会一次性覆盖所有这些默认值。
var reWindowPlaceholder = regexp.MustCompile(`\{(from|to):([A-Za-z0-9]+)\}`)

// reWindowLeftover 查找 expandWindowPlaceholders 拒绝替换的窗口占位符，
// 即默认规格格式错误的那些。由 UnboundPlaceholders 报告，使 executor
// 拒绝该查询，而不是把字面花括号发送到服务器。
var reWindowLeftover = regexp.MustCompile(`\{(?:from|to):[^{}]*\}`)

// windowDefaultSQL 把占位符的默认规格转换为 SQL。
//
//	now  -> now()
//	15d  -> now() - INTERVAL 15 DAY
//	12h  -> now() - INTERVAL 12 HOUR
//	30m  -> now() - INTERVAL 30 MINUTE
//
// 其他情况返回 ok=false，从而让占位符保持原样，随后被当作未绑定捕获，
// 而不是悄悄放宽时间窗口。
func windowDefaultSQL(spec string) (string, bool) {
	if spec == "now" {
		return "now()", true
	}
	if len(spec) < 2 {
		return "", false
	}
	n, unit := spec[:len(spec)-1], spec[len(spec)-1]
	for _, c := range n {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	if n == "" || strings.TrimLeft(n, "0") == "" {
		return "", false // 从未有意使用零长度窗口
	}
	var interval string
	switch unit {
	case 'd':
		interval = "DAY"
	case 'h':
		interval = "HOUR"
	case 'm':
		interval = "MINUTE"
	default:
		return "", false
	}
	return fmt.Sprintf("now() - INTERVAL %s %s", n, interval), true
}

// utcLiteral 把 flag 提供的时间戳渲染为 SQL。这里显式写出时区，是因为
// ClickHouse 会按 DateTime 列所在时区（服务器的时区，而不是 UTC）解析裸字符串
// 字面量。在 <timezone>America/New_York</timezone> 的服务器上，裸的
// '2026-03-01 10:30:00' 会与运维人员想要的 UTC 时刻相差五个小时，而且
// 不会有任何报错：窗口被悄悄平移了。已在线验证：裸写与显式声明
// 恰好相差一个时区偏移。
func utcLiteral(t time.Time) string {
	return "toDateTime('" + t.UTC().Format("2006-01-02 15:04:05") + "', 'UTC')"
}

// expandWindowPlaceholders 在提供了 flag 值时把 {from:<default>} /
// {to:<default>} 替换为 flag 值，否则替换为查询自身的默认值。
// 替换后的 flag 值是带引号的 UTC 字面量，与查询分析所用的裸
// {from} / {to} 形式一致。
func expandWindowPlaceholders(sql string, from, to time.Time, maxLookback time.Duration) string {
	return reWindowPlaceholder.ReplaceAllStringFunc(sql, func(match string) string {
		g := reWindowPlaceholder.FindStringSubmatch(match)
		name, spec := g[1], g[2]

		bound := from
		if name == "to" {
			bound = to
		}
		if !bound.IsZero() {
			return utcLiteral(bound)
		}
		if name == "from" && maxLookback > 0 {
			if d, ok := windowSpecDuration(spec); ok && d > maxLookback {
				return fmt.Sprintf("now() - INTERVAL %d HOUR", int64((maxLookback+time.Hour-1)/time.Hour))
			}
		}
		if def, ok := windowDefaultSQL(spec); ok {
			return def
		}
		return match // 格式错误 —— 留给 UnboundPlaceholders
	})
}

// windowSpecDuration 是 {from:<spec>} 默认值的长度（15d、12h、30m）；
// 对于 "now" 或格式错误的规格返回 ok=false。
func windowSpecDuration(spec string) (time.Duration, bool) {
	if _, ok := windowDefaultSQL(spec); !ok || spec == "now" {
		return 0, false
	}
	n, err := strconv.ParseInt(spec[:len(spec)-1], 10, 64)
	if err != nil {
		return 0, false
	}
	switch spec[len(spec)-1] {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'm':
		return time.Duration(n) * time.Minute, true
	}
	return 0, false
}

func scopeOr1(s string) string {
	if s == "" || s == "1" {
		return "1"
	}
	return "(" + s + ")"
}
