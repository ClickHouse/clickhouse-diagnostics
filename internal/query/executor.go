package query

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/collection"
	"clickhouse-diagnostic/internal/collectmode"
	"clickhouse-diagnostic/internal/runlog"
	"clickhouse-diagnostic/pkg"
)

// Executor 负责查询执行和结果管理
type Executor struct {
	client *pkg.ClickHouseClient
	// salt 非空时，会在执行前替换 .sql 查询体中的字面量 '%salt%' 占位符。
	// 仅在 gov 模式下使用，使 database/table 名称的哈希针对客户的本次运行
	// 唯一，而不是一个公开已知的常量。
	salt string
	// format 是结果序列化所用的 ClickHouse FORMAT，也是写出文件时使用的扩展名。
	// 零值表示使用默认格式。
	format OutputFormat
	// from / to 非零时，覆盖每个查询自身的 {from:…} / {to:…} 默认值。
	// 零值表示“每个查询保留它声明的窗口”。
	from time.Time
	to   time.Time
	// mode 用于 {sys.<table>} 展开（cloud 会把 per-replica 表包裹在
	// clusterAllReplicas 中）。为空则让这类占位符保持未展开，
	// 随后由下方的未绑定检查拒绝。
	mode string
	// rec 设置后，每个查询向 execution_log.txt 写入一条记录
	// （结果、实际耗时、结果大小）。nil 表示无操作。
	rec *runlog.Recorder
	// watch 逐个 collector 决定本次运行是否仍为 full：一旦它被触发
	//（一开始就选用 essential 采集、超时、连续两次慢查询），就跳过 full tier
	// 的 collector；超过时间预算后，剩余的一切都跳过。nil = 始终 full。
	watch *collectmode.Watchdog
	// maxLookback 为每个 {from:<default>} 窗口设上限（essential：1 天）。
	maxLookback time.Duration
	// detachedScope / detachedScopeP 填充 {detached_scope(_p)}。
	detachedScope, detachedScopeP string
	// keeperDown：以该原因跳过需要活动 Keeper 会话的 collector。
	keeperDown string
}

// WithKeeperDown 让 executor 跳过 `-- needs: keeper` 的 collector
// （reason 非空：服务器没有 Keeper 会话）。
func (e *Executor) WithKeeperDown(reason string) *Executor {
	e.keeperDown = reason
	return e
}

// WithDetachedScope 设置 detached part 的查询范围（见 DetachedScope）。
func (e *Executor) WithDetachedScope(scope, qualified string) *Executor {
	e.detachedScope, e.detachedScopeP = scope, qualified
	return e
}

// WithWatchdog 挂接采集模式 watchdog。
func (e *Executor) WithWatchdog(w *collectmode.Watchdog) *Executor {
	e.watch = w
	return e
}

// WithMaxLookback 为每个查询的默认窗口设上限（0 = 不设限）。
func (e *Executor) WithMaxLookback(d time.Duration) *Executor {
	e.maxLookback = d
	return e
}

// WithRecorder 挂接执行日志记录器。
func (e *Executor) WithRecorder(r *runlog.Recorder) *Executor {
	e.rec = r
	return e
}

// WithMode 设置用于 {sys.<table>} 展开的拓扑模式。
func (e *Executor) WithMode(mode string) *Executor {
	e.mode = mode
	return e
}

// WithWindow 覆盖每个声明了时间窗口的查询的窗口。
// 零值会让每个查询自身的默认值保持不变。
func (e *Executor) WithWindow(from, to time.Time) *Executor {
	e.from, e.to = from, to
	return e
}

// NewExecutor 创建一个新的查询执行器
func NewExecutor(client *pkg.ClickHouseClient) *Executor {
	return &Executor{
		client: client,
		format: DefaultOutputFormat,
	}
}

// WithOutputFormat 设置查询结果的序列化格式。
func (e *Executor) WithOutputFormat(f OutputFormat) *Executor {
	e.format = f
	return e
}

// outputFormat 返回配置的格式，并兼容较早调用点或测试构建的
// 零值 Executor。
func (e *Executor) outputFormat() OutputFormat {
	if e.format.Clause == "" {
		return DefaultOutputFormat
	}
	return e.format
}

// WithSalt 设置 gov 模式的 salt，用于在运行时替换 .sql 文件中的
// '%salt%' 占位符。salt 必须已经过 sanitisation（仅字母数字）；
// 校验由调用方负责。
func (e *Executor) WithSalt(salt string) *Executor {
	e.salt = salt
	return e
}

// RunFolderName 是本次运行结果文件夹的名称 —— 加上 .tar.gz 后也是由它构建的
// 归档名称：clickhouse_diagnostic_<YYYYMMDD_HHMMSS>。
// 重命名之前的发行版本写的是 clickhouse_backup_<ts>；预检脚本两者都能读取。
func RunFolderName(t time.Time) string {
	return "clickhouse_diagnostic_" + t.Format("20060102_150405")
}

// ExecuteQueries 执行一组选中的查询并保存结果
// 返回保存结果的具体目录路径
func (e *Executor) ExecuteQueries(queries map[string]internal.QueryFile, outputDir string) (string, error) {
	now := time.Now()
	timestamp := now.Format("20060102_150405")
	finalOutputDir := filepath.Join(outputDir, RunFolderName(now))

	// 创建输出目录
	if err := os.MkdirAll(finalOutputDir, 0750); err != nil {
		return "", fmt.Errorf("创建输出目录失败：%w", err)
	}

	if e.client.IsDryRun() {
		fmt.Printf("将执行 %d 条唯一查询（dry-run）：\n", len(queries))
	} else {
		fmt.Printf("正在执行 %d 条唯一查询...\n\n", len(queries))
	}

	successCount := 0
	errorCount := 0
	skippedCount := 0

	// 先 essential 采集器，再其余采集器，每组内部按名称排序：即使运行被提前
	// 截断，开销低且具决定性的表也已进入 bundle，而且顺序不再依赖 map 迭代。
	ordered := orderByTier(queries)
	for _, oq := range ordered {
		query := oq.file
		if e.watch.OverBudget() {
			e.skip(query, fmt.Sprintf("时间预算（%v）在其运行前已用完", e.watch.Budget()))
			skippedCount++
			continue
		}
		if e.keeperDown != "" && oq.needsKeeper {
			e.skip(query, "需要活动的 Keeper 会话——"+e.keeperDown)
			skippedCount++
			continue
		}
		if tripped, why := e.watch.Tripped(); tripped && oq.tier != TierEssential {
			e.skip(query, "essential collection（精简采集）："+why)
			skippedCount++
			continue
		}
		started := time.Now()
		err := e.executeQuery(query, finalOutputDir, timestamp)
		e.watch.Observe(query.Name, time.Since(started), err)
		if err != nil {
			fmt.Printf("执行查询 '%s' 失败：%v\n", query.Name, err)
			errorCount++
		} else {
			successCount++
		}
	}
	if skippedCount > 0 {
		fmt.Printf("\n%d 个 collector 被跳过（essential 采集或时间预算）——已列在 execution_log.txt 中\n", skippedCount)
	}

	if e.client.IsDryRun() {
		fmt.Printf("\nDry-run 摘要：本应执行 %d 个查询文件。\n", successCount)
	} else {
		fmt.Printf("\n查询执行完成：%d 成功，%d 失败\n", successCount, errorCount)
		fmt.Printf("结果已保存到：%s\n", finalOutputDir)
	}

	return finalOutputDir, nil
}

// executeQuery 校验并执行单个查询，然后保存结果。
func (e *Executor) executeQuery(query internal.QueryFile, outputDir, timestamp string) error {
	// 从文件读取查询
	queryContent, err := os.ReadFile(query.FullPath)
	if err != nil {
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed", Rows: -1, Error: "error reading query file: " + err.Error()})
		return fmt.Errorf("读取查询文件失败：%w", err)
	}

	if len(queryContent) == 0 {
		fmt.Printf("查询文件 '%s' 为空，跳过\n", query.FullPath)
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "empty", Rows: -1, Extra: "query file is empty"})
		return nil
	}

	// 绑定模板。这会解析 {from:<default>} / {to:<default>}
	// 时间窗口 —— 若给出了 --from / --to flag 则用它们，否则用每个查询
	// 自行声明的默认值 —— 并且在 gov 模式下，用客户 salt 替换
	// 公开的 '%salt%' 标记。salt 格式已在上游校验（仅字母数字），
	// 因此它无法逃出 SQL 字符串字面量。
	sqlText := Apply(string(queryContent), Vars{
		Mode:           e.mode,
		From:           e.from,
		To:             e.to,
		GovSalt:        e.salt,
		MaxLookback:    e.lookback(),
		DetachedScope:  e.detachedScope,
		DetachedScopeP: e.detachedScopeP,
	})

	// 未绑定的占位符会以字面花括号的形式到达服务器：
	// 要么解析报错，要么 —— 更糟 —— 得到一个悄悄出错的窗口。
	if unbound := UnboundPlaceholders(sqlText); len(unbound) > 0 {
		err := fmt.Errorf("查询 '%s' 含有未绑定的占位符 %v", query.Name, unbound)
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed", Rows: -1, Error: err.Error()})
		return err
	}

	// 按配置的格式序列化，替换文件自带的任何 FORMAT。
	// 在校验之前完成，这样校验器看到的正是将要发送给服务器的文本。
	sqlText = e.outputFormat().Apply(sqlText)

	// 安全性：执行前强制只读 SELECT。
	if err := ValidateQueryContent(sqlText); err != nil {
		err = fmt.Errorf("'%s' 的安全校验失败：%w", query.Name, err)
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed", Rows: -1, Error: err.Error()})
		return err
	}

	// 显示来源信息。dry-run 时前缀会变化，以明确表示实际上什么都没运行。
	verb := "正在执行"
	if e.client.IsDryRun() {
		verb = "将执行"
	}
	if query.DirName != "" {
		fmt.Printf("%s查询 '%s'（来自目录 '%s'）...\n", verb, query.Name, query.DirName)
	} else {
		fmt.Printf("%s查询 '%s'（来自根目录）...\n", verb, query.Name)
	}

	// 执行查询并计时往返：这个数字能说明在给定服务器上哪些 collector 开销大。
	started := time.Now()
	result, err := e.client.ExecuteQuery(sqlText)
	elapsed := time.Since(started)
	if err != nil {
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed",
			Duration: elapsed, Rows: -1, Error: err.Error()})
		return fmt.Errorf("执行查询失败：%w", err)
	}

	// DDL 中内嵌的凭据。system.tables.create_table_query / engine_full
	// / as_select 以及 system.dictionaries.source 在 23.x 以下的服务器上会原样
	// 携带引擎参数（S3 密钥、MySQL 密码）；较新的服务器会自行把它们
	// 掩码为 '[HIDDEN]'。配置 sanitizer 从未看到这些文件 —— 它只接入了
	// XML/YAML 采集器 —— 因此结果文本在写入磁盘之前在这里清洗。
	result, redactedNote := redactResult(query.Name, e.outputFormat().Ext, result)

	// 生成输出文件名
	baseFileName := strings.TrimSuffix(query.Name, filepath.Ext(query.Name))
	outputFileName := fmt.Sprintf("%s_%s%s", baseFileName, timestamp, e.outputFormat().Ext)
	outputPath := filepath.Join(outputDir, outputFileName)

	// 把结果保存到文件
	if err := os.WriteFile(outputPath, []byte(result), 0600); err != nil {
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed",
			Duration: elapsed, Bytes: int64(len(result)), Rows: -1, Error: "error saving result: " + err.Error()})
		return fmt.Errorf("保存结果失败：%w", err)
	}

	// 只有在面向行的格式中才能统计行数；Native 是不透明的，
	// 而 TSVWithNamesAndTypes 带有两行并非数据的表头。
	rows := int64(-1)
	switch e.outputFormat().Ext {
	case ".jsonl":
		rows = int64(strings.Count(result, "\n"))
	case ".tsv":
		rows = int64(strings.Count(result, "\n")) - 2
		if rows < 0 {
			rows = 0
		}
	}
	extra := outputFileName
	if redactedNote != "" {
		extra += "; " + redactedNote
	}
	e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "ok",
		Duration: elapsed, Bytes: int64(len(result)), Rows: rows, Extra: extra})

	if !e.client.IsDryRun() {
		fmt.Printf("查询 '%s' 执行成功。结果已保存到 %s\n\n", query.Name, outputPath)
	}
	return nil
}

// ValidateQuery 读取查询文件并用 ValidateQueryContent 校验它。
func (e *Executor) ValidateQuery(queryPath string) error {
	content, err := os.ReadFile(queryPath)
	if err != nil {
		return fmt.Errorf("无法读取查询文件：%w", err)
	}
	return ValidateQueryContent(string(content))
}

// redactResult 在写出之前，从 collector 结果中带 DDL 的列里清除凭据。
// 只会触碰 collection.SensitiveCollectorFields 中列出的 collector，并且只在
// JSONL 格式下进行 —— 在该格式中，字段可以被定位并重写而不扰动任何其他字节。
// Native 和 TSV 对字段级处理是不透明的，因此会原样写出，并在执行日志中
// 加一条说明 —— 这也是默认格式才值得随包发布的又一个原因。
func redactResult(queryName, ext, result string) (string, string) {
	fields, ok := collection.SensitiveCollectorFields[queryName]
	if !ok {
		return result, ""
	}
	if ext != ".jsonl" {
		fmt.Printf("  警告：%s 携带 DDL 文本，而写出格式为 %s，无法逐字段脱敏；"+
			"引擎参数中的凭据可能会保留——如需可共享的 bundle，请使用默认的 jsonl 格式\n",
			queryName, strings.TrimPrefix(ext, "."))
		return result, "未脱敏（非 JSONL 格式）"
	}
	out, n := collection.RedactJSONLFields(result, fields)
	if n == 0 {
		return out, ""
	}
	return out, fmt.Sprintf("%d 个凭据已替换为 [HIDDEN]", n)
}

type tieredQuery struct {
	file        internal.QueryFile
	tier        string
	needsKeeper bool
}

// orderByTier 把 collector 按 essential 优先排序，然后按名称排序。
// 无法读取 tier 的文件按 full 计；executeQuery 会报告读取错误。
func orderByTier(queries map[string]internal.QueryFile) []tieredQuery {
	out := make([]tieredQuery, 0, len(queries))
	for _, q := range queries {
		tier, needs := TierFull, false
		if b, err := os.ReadFile(q.FullPath); err == nil {
			if t, err := ParseTier(string(b)); err == nil {
				tier = t
			}
			needs = NeedsKeeper(string(b))
		}
		out = append(out, tieredQuery{q, tier, needs})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].tier == TierEssential) != (out[j].tier == TierEssential) {
			return out[i].tier == TierEssential
		}
		return out[i].file.Name < out[j].file.Name
	})
	return out
}

// skip 记录一个未运行的 collector 及其原因。
func (e *Executor) skip(q internal.QueryFile, why string) {
	fmt.Printf("跳过 '%s'：%s\n", q.Name, why)
	e.rec.Record(runlog.Entry{Stage: "collector", Name: q.Name, Source: q.DirName, Status: "skipped", Rows: -1, Extra: why})
}

// lookback 是下一个查询的窗口上限：配置值，或者一旦本次运行变为 essential
// （一开始选定或运行中途切换）则为 1 天。
func (e *Executor) lookback() time.Duration {
	if e.maxLookback > 0 {
		return e.maxLookback
	}
	if tripped, _ := e.watch.Tripped(); tripped {
		return collectmode.EssentialLookback
	}
	return 0
}
