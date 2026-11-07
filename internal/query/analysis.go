package query

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/pkg"
)

// reQueryID 匹配规范 UUID —— 8-4-4-4-12 位十六进制数字。
// 使用锚定，使部分匹配无法夹带额外的 SQL。
var reQueryID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// reNormalizedHash 匹配 ClickHouse 的 normalized_query_hash ——
// 字符串化的 UInt64。最多 20 位数字（最大值为 18446744073709551615）。
var reNormalizedHash = regexp.MustCompile(`^[0-9]{1,20}$`)

// ValidateQueryID 在 id 不是规范 UUID 时返回错误。
// CLI flag 解析器和分析 collector 都会在把该值拼接进 SQL 字符串字面量
// 之前调用它。
func ValidateQueryID(id string) error {
	if id == "" {
		return fmt.Errorf("query-id 为必填项")
	}
	if !reQueryID.MatchString(id) {
		return fmt.Errorf("query-id 必须是 UUID（xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx）；实际为 %q", id)
	}
	return nil
}

// ValidateNormalizedQueryHash 在 h 不是十进制 uint64 时返回错误。
// 空值会被拒绝 —— 只有在调用方已经要求提供值时才调用它。
func ValidateNormalizedQueryHash(h string) error {
	if h == "" {
		return fmt.Errorf("normalized-query-hash 为必填项")
	}
	if !reNormalizedHash.MatchString(h) {
		return fmt.Errorf("normalized-query-hash 必须是正整数（UInt64）；实际为 %q", h)
	}
	return nil
}

// ParseTimeFlag 接受 RFC3339（`2026-05-28T10:00:00Z`）或仅日期
// （`2026-05-28`，按 UTC 午夜解释）。--from 和 --to 都会使用它。
func ParseTimeFlag(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("时间 %q 无效——请使用 RFC3339（2026-05-28T10:00:00Z）或 YYYY-MM-DD", s)
}

// AnalysisOpts 保存查询分析采集阶段的运行时参数。由 cmd/main.go 从 CLI flag
// 构建，并传入 AnalysisCollector.Collect。
//
// QueryID 和 NormalizedQueryHash 彼此兼容 —— 允许同时提供，
// 并且会跳过预检中的 hash 推导。两者至少要有一个非空，collector 才会运行。
type AnalysisOpts struct {
	QueryID             string
	NormalizedQueryHash string
	From                time.Time // 窗口起点（UTC）
	To                  time.Time // 窗口终点（UTC）
}

// Enabled 报告是否设置了任一焦点参数 —— 即分析 bundle 是否应当运行。
func (o AnalysisOpts) Enabled() bool {
	return o.QueryID != "" || o.NormalizedQueryHash != ""
}

// Validate 校验已填充的字段，并应用合理的默认时间窗口。`now` 是用于
// 默认值的实际时钟 —— 调用方在解析 flag 时传入 time.Now()，测试则传入
// 固定时间戳。
//
// 默认窗口规则：
//   - --from 和 --to 都设置：        原样使用
//   - 只设置了其中一个：             以 now 为基准补齐另一个
//   - 都没设置，只有 --query-id：    最近 3 天（以 event_time 为中心的
//     窗口处理发生在预检之后；见 PreflightForQueryID）
//   - 都没设置，只有 --hash：        最近 7 天（更多历史）
func (o *AnalysisOpts) Validate(now time.Time) error {
	if !o.Enabled() {
		return fmt.Errorf("查询分析需要 --query-id 或 --normalized-query-hash")
	}
	if o.QueryID != "" {
		if err := ValidateQueryID(o.QueryID); err != nil {
			return err
		}
	}
	if o.NormalizedQueryHash != "" {
		if err := ValidateNormalizedQueryHash(o.NormalizedQueryHash); err != nil {
			return err
		}
	}
	switch {
	case !o.From.IsZero() && !o.To.IsZero():
		// 两者都已设置；信任调用方
	case !o.From.IsZero():
		o.To = now
	case !o.To.IsZero():
		o.From = o.To.AddDate(0, 0, -7)
	default:
		// 都没设置 —— 默认值因使用哪个焦点参数而不同
		days := 7
		if o.QueryID != "" {
			days = 3
		}
		o.To = now
		o.From = now.AddDate(0, 0, -days)
	}
	if !o.To.After(o.From) {
		return fmt.Errorf("--to（%s）必须晚于 --from（%s）",
			o.To.Format(time.RFC3339), o.From.Format(time.RFC3339))
	}
	return nil
}

// PreflightForHash 为给定的 normalized_query_hash 选出一个代表性
// query_id —— 时间窗口内最慢的一次已完成执行。仅 hash 模式就是这样填充
// 只接受单个 id 的查询（ProfileEvents、text_log 切片、tables-referenced 等）：
// 没有 query_id，每个 `query_id = …` 的 WHERE 过滤都会返回零行。
//
// 我们刻意选择最慢的执行，因为它对“为什么这次这么慢？”的分析最有用。
// 快慢对比会通过 fast_slow_query_ids.sql 单独呈现最快的执行，
// 因此两个极端在 dashboard 中都能看到。
//
// 返回选中的 query_id 及其 event_time。如果没有找到执行记录，两个值都为零 ——
// 调用方应将其视为“没有可用的代表”，并跳过只接受单个 id 的文件。
func PreflightForHash(client *pkg.ClickHouseClient, mode, hash string, from, to time.Time) (queryID string, eventTime time.Time, err error) {
	if err := ValidateNormalizedQueryHash(hash); err != nil {
		return "", time.Time{}, err
	}
	if from.IsZero() || to.IsZero() {
		return "", time.Time{}, fmt.Errorf("预检：从 hash 推导 query_id 前必须先设置窗口")
	}
	sql := fmt.Sprintf(`SELECT
		argMax(query_id, query_duration_ms) AS qid,
		toString(argMax(event_time, query_duration_ms)) AS et
	FROM %s
	WHERE normalized_query_hash = %s
	  AND event_time >= '%s'
	  AND event_time <= '%s'
	  AND type = 'QueryFinish'
	  AND NOT has(databases, 'system')
	FORMAT JSONCompact`,
		SysTable(mode, "query_log"),
		hash,
		from.UTC().Format("2006-01-02 15:04:05"),
		to.UTC().Format("2006-01-02 15:04:05"))

	// ExecuteQueryReal 绕过 dry-run 拦截。否则，dry-run 在这里会返回空结果，
	// 推导出的 query_id 会保持未设置 —— 分析 bundle 随后会原样打印
	// `{query_id}`，而不是慢执行的真正 UUID，这就违背了 dry-run 的目的。
	raw, err := client.ExecuteQueryReal(sql)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("预检（hash → 最慢的 query_id）失败：%w", err)
	}
	var res struct {
		Data [][]string `json:"data"`
		Rows int        `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return "", time.Time{}, fmt.Errorf("预检解析失败：%w（%.200s）", err, raw)
	}
	if res.Rows == 0 || len(res.Data) == 0 || len(res.Data[0]) < 2 || res.Data[0][0] == "" {
		// 空结果不是错误 —— 调用方自行决定是否在没有代表
		// query_id 的情况下继续。
		return "", time.Time{}, nil
	}
	queryID = res.Data[0][0]
	if t, parseErr := time.Parse("2006-01-02 15:04:05", res.Data[0][1]); parseErr == nil {
		eventTime = t.UTC()
	}
	return queryID, eventTime, nil
}

// PreflightForQueryID 为给定的 --query-id 查询 normalized_query_hash
// 和 event_time。它在分析 bundle 之前只运行一次，这样 bundle 的分组查询
// 就能用推导出的 hash 参数化，而无需用户同时提供两个 flag。
//
// 返回推导出的 hash 和该查询的 event_time。如果 event_time 非零，
// 调用方应把默认时间窗口重新对准它（这样一条 3 天前的慢查询就不会被
// “从 now 往前 3 天”的默认值排除）。
//
// 如果在调用方给定的搜索窗口（默认回溯 30 天）内未能在 system.query_log
// 中找到该 query_id，则返回错误 —— 与其产出空的分析文件，
// 不如以清晰的错误信息退出。
func PreflightForQueryID(client *pkg.ClickHouseClient, mode, queryID string) (hash string, eventTime time.Time, err error) {
	if err := ValidateQueryID(queryID); err != nil {
		return "", time.Time{}, err
	}
	sql := fmt.Sprintf(`SELECT
		toString(normalized_query_hash) AS h,
		toString(event_time) AS et
	FROM %s
	WHERE query_id = '%s'
	  AND event_time > now() - INTERVAL 30 DAY
	  AND type != 'QueryStart'
	ORDER BY event_time DESC
	LIMIT 1
	FORMAT JSONCompact`, SysTable(mode, "query_log"), queryID)

	// ExecuteQueryReal —— 见 PreflightForHash 中的注释。必须绕过
	// dry-run，这样推导出的 hash 才能流入分析 SQL。
	raw, err := client.ExecuteQueryReal(sql)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("预检查询 query_id %s 失败：%w", queryID, err)
	}
	var res struct {
		Data [][]string `json:"data"`
		Rows int        `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return "", time.Time{}, fmt.Errorf("预检解析失败：%w（%.200s）", err, raw)
	}
	if res.Rows == 0 || len(res.Data) == 0 {
		return "", time.Time{}, fmt.Errorf("最近 30 天内未在 system.query_log 中找到 query_id %s", queryID)
	}
	row := res.Data[0]
	if len(row) < 2 {
		return "", time.Time{}, fmt.Errorf("预检返回了意外的结构：%v", row)
	}
	hash = row[0]
	if t, parseErr := time.Parse("2006-01-02 15:04:05", row[1]); parseErr == nil {
		eventTime = t.UTC()
	}
	return hash, eventTime, nil
}

// AnalysisCollector 运行查询分析 SQL bundle，并把结果写入每次运行备份
// 文件夹下的 `query_analysis/` 子目录。
type AnalysisCollector struct {
	client *pkg.ClickHouseClient
	mode   string
	format OutputFormat
}

// WithOutputFormat 设置分析结果的序列化格式。
func (c *AnalysisCollector) WithOutputFormat(f OutputFormat) *AnalysisCollector {
	c.format = f
	return c
}

// outputFormat 兼容零值 collector（较早的调用点和测试）。
func (c *AnalysisCollector) outputFormat() OutputFormat {
	if c.format.Clause == "" {
		return DefaultOutputFormat
	}
	return c.format
}

// NewAnalysisCollector 返回一个绑定到已连接 client 的 collector。
func NewAnalysisCollector(client *pkg.ClickHouseClient, mode string) *AnalysisCollector {
	return &AnalysisCollector{client: client, mode: strings.ToLower(mode)}
}

// Collect 遍历 queriesDir 中的 .sql 文件，应用模板
// （sys/query_id/normalized_query_hash/from/to 占位符），校验每个查询，
// 执行它，并把结果按每个查询一个文件的形式写入
// <backupDir>/query_analysis/。
//
// queriesDir 遵循与各模式查询目录相同的版本目录约定：名为 ClickHouse 版本
// （例如 "25.4.1.0"）的子目录，在所连接的服务器（serverVersion）至少为该版本时
// 覆盖同名的根文件。
//
// 模板替换后仍包含未绑定占位符的文件会被跳过 —— 当设置了
// --normalized-query-hash 而未设置 --query-id 时（反之亦然），
// 只接受单个 query-id 的文件就是这样表现的。
//
// 返回写入的文件数，以及因未绑定占位符而跳过的文件数
// （仅供参考 —— 不是错误）。
func (c *AnalysisCollector) Collect(opts AnalysisOpts, queriesDir, backupDir string, serverVersion internal.Version) (written, skipped int, err error) {
	if !opts.Enabled() {
		return 0, 0, fmt.Errorf("未设置焦点参数时调用了分析 collector")
	}

	files, err := FindVersionedFiles(queriesDir, serverVersion, ".sql")
	if err != nil {
		return 0, 0, fmt.Errorf("读取 query-analysis 目录 %s 失败：%w", queriesDir, err)
	}

	if len(files) == 0 {
		return 0, 0, fmt.Errorf("在 %s 中未找到 .sql 文件", queriesDir)
	}

	outDir := filepath.Join(backupDir, "query_analysis")
	if err := os.MkdirAll(outDir, 0750); err != nil {
		return 0, 0, fmt.Errorf("创建 query_analysis 输出目录失败：%w", err)
	}

	vars := Vars{
		Mode:                c.mode,
		QueryID:             opts.QueryID,
		NormalizedQueryHash: opts.NormalizedQueryHash,
		From:                opts.From,
		To:                  opts.To,
	}
	timestamp := time.Now().UTC().Format("20060102_150405")

	verb := "正在运行"
	if c.client.IsDryRun() {
		verb = "将运行"
	}
	fmt.Printf("\n查询分析：%s %d 个文件（窗口 %s → %s）\n",
		verb, len(files), opts.From.Format(time.RFC3339), opts.To.Format(time.RFC3339))

	for _, qf := range files {
		fname := qf.Name
		if qf.DirName != "" {
			// 为这台服务器选中的版本特定覆盖文件。
			fname = qf.DirName + "/" + qf.Name
		}
		raw, readErr := os.ReadFile(qf.FullPath)
		if readErr != nil {
			fmt.Printf("  [analysis] 读取 %s 失败：%v\n", fname, readErr)
			continue
		}
		applied := Apply(string(raw), vars)
		if unbound := UnboundPlaceholders(applied); len(unbound) > 0 {
			fmt.Printf("  [analysis] 跳过 %s——需要 %v\n", fname, unbound)
			skipped++
			continue
		}
		applied = c.outputFormat().Apply(applied)
		if validateErr := ValidateQueryContent(applied); validateErr != nil {
			fmt.Printf("  [analysis] 跳过 %s——校验：%v\n", fname, validateErr)
			skipped++
			continue
		}
		result, qErr := c.client.ExecuteQuery(applied)
		if qErr != nil {
			fmt.Printf("  [analysis] %s：%v\n", fname, qErr)
			continue
		}
		base := strings.TrimSuffix(qf.Name, ".sql")
		out := filepath.Join(outDir, fmt.Sprintf("%s_%s%s", base, timestamp, c.outputFormat().Ext))
		// 在 dry-run 中结果为空，且 client 已经把 SQL 和表打印到 stdout；
		// 再写一个空结果文件只会弄乱临时目录。
		if !c.client.IsDryRun() {
			if writeErr := os.WriteFile(out, []byte(result), 0600); writeErr != nil {
				fmt.Printf("  [analysis] 写入 %s 失败：%v\n", out, writeErr)
				continue
			}
			fmt.Printf("  [analysis] %s → %s\n", fname, out)
		}
		written++
	}
	if c.client.IsDryRun() {
		fmt.Printf("查询分析：%d 个将运行，%d 个跳过\n", written, skipped)
	} else {
		fmt.Printf("查询分析：%d 个已写入，%d 个跳过\n", written, skipped)
	}
	return written, skipped, nil
}
