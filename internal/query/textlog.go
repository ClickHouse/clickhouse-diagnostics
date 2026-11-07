package query

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/pkg"
)

// TextLogOpts 配置对 system.text_log 的有界导出。
//
// 该采集器是可选启用的，并且要求显式时间窗口。text_log 是 ClickHouse 写入量
// 最大的表 —— 无界导出可能达到数 GB，本身就会给服务器带来负载 —— 因此这里
// 刻意不提供默认范围：运维人员必须说明自己在查看哪次故障。
type TextLogOpts struct {
	From     time.Time
	To       time.Time
	Level    string // 可选的最低严重级别过滤，例如 "Warning"
	RowLimit int
}

// DefaultTextLogRowLimit 限定单次采集的行数。它足够宽裕以覆盖一次故障窗口，
// 又足够小以让归档保持可分发。
const DefaultTextLogRowLimit = 500000

// Validate 强制要求时间窗口，并规范化行数上限。
func (o *TextLogOpts) Validate() error {
	if o.From.IsZero() || o.To.IsZero() {
		return fmt.Errorf("--collect-text-log 需要同时提供 --from 和 --to " +
			"（text_log 数据量很大；无界导出会非常庞大，并给服务器带来负载）")
	}
	if !o.To.After(o.From) {
		return fmt.Errorf("--to（%s）必须晚于 --from（%s）",
			o.To.Format(time.RFC3339), o.From.Format(time.RFC3339))
	}
	if o.RowLimit <= 0 {
		o.RowLimit = DefaultTextLogRowLimit
	}
	if o.Level != "" {
		canonical, ok := canonicalLevel(o.Level)
		if !ok {
			return fmt.Errorf("--text-log-level %q 不是 ClickHouse 日志级别（%s）",
				o.Level, strings.Join(levelNames, ", "))
		}
		o.Level = canonical
	}
	return nil
}

var levelNames = []string{"Fatal", "Critical", "Error", "Warning", "Notice", "Information", "Debug", "Trace"}

// canonicalLevel 以不区分大小写的方式，把用户输入的级别（"error"、"ERROR"）
// 解析为 text_log 使用的确切枚举拼写。返回值只可能来自 levelNames，这正是
// 将其拼接进 SQL 之所以安全的原因。（取代了 strings.Title 往返 —— 它已被废弃，
// 而且它是推导拼写而非证明成员关系。）
func canonicalLevel(s string) (string, bool) {
	for _, l := range levelNames {
		if strings.EqualFold(s, l) {
			return l, true
		}
	}
	return "", false
}

// TextLogCollector 导出 system.text_log 中一段有界的时间切片。
type TextLogCollector struct {
	client *pkg.ClickHouseClient
	mode   string
	format OutputFormat
}

// WithOutputFormat 设置 text_log 导出的序列化格式。
func (c *TextLogCollector) WithOutputFormat(f OutputFormat) *TextLogCollector {
	c.format = f
	return c
}

// outputFormat 兼容零值 collector（较早的调用点和测试）。
func (c *TextLogCollector) outputFormat() OutputFormat {
	if c.format.Clause == "" {
		return DefaultOutputFormat
	}
	return c.format
}

func NewTextLogCollector(client *pkg.ClickHouseClient, mode string) *TextLogCollector {
	return &TextLogCollector{client: client, mode: strings.ToLower(mode)}
}

// Collect 把 text_log_<timestamp>.<ext> 写入 outDir 并返回路径。
// text_log 表不存在时报告为“未采集”而不是错误：该表在 ClickHouse 中默认禁用，
// 因此它的缺失是一个配置事实，而不是失败 —— 这与告警评估器区分的
// skipped 与 errored 是同一个道理。
func (c *TextLogCollector) Collect(opts TextLogOpts, outDir string, serverVersion internal.Version) (string, error) {
	if err := opts.Validate(); err != nil {
		return "", err
	}

	sql := c.buildSQL(opts, serverVersion)
	if err := ValidateQueryContent(sql); err != nil {
		return "", fmt.Errorf("text_log 查询的安全校验失败：%w", err)
	}

	fmt.Printf("正在采集 %s 到 %s 之间的 system.text_log（上限 %d 行）...\n",
		opts.From.Format(time.RFC3339), opts.To.Format(time.RFC3339), opts.RowLimit)

	result, err := c.client.ExecuteQuery(sql)
	if err != nil {
		if strings.Contains(err.Error(), "UNKNOWN_TABLE") {
			return "", fmt.Errorf("此服务器上不存在 system.text_log——"+
				"该表默认禁用；请在服务器配置中启用 <text_log> 段以采集它（原始错误：%w）", err)
		}
		return "", err
	}

	if c.client.IsDryRun() {
		return "", nil // client 已经打印了 SQL；无需写入任何内容
	}

	path := filepath.Join(outDir, fmt.Sprintf("text_log_%s%s",
		time.Now().UTC().Format("20060102_150405"), c.outputFormat().Ext))
	if err := os.WriteFile(path, []byte(result), 0600); err != nil {
		return "", fmt.Errorf("写入 text_log 输出失败：%w", err)
	}
	return path, nil
}

// buildSQL 组装查询。它与 Collect 分开，以便无需服务器即可对 SQL 形态做单元测试。
func (c *TextLogCollector) buildSQL(opts TextLogOpts, serverVersion internal.Version) string {
	// message_format_string 在 23.1 中加入；在更旧的服务器上查询它会报
	// UNKNOWN_IDENTIFIER，因此像版本目录中的 .sql 文件那样对它做版本判断。
	formatCol := ""
	if isAtLeast(serverVersion, 23, 1) {
		formatCol = "    message_format_string,\n"
	}

	var levelFilter string
	if opts.Level != "" {
		// text_log.level 是 Enum8，按最严重在前排序，因此“至少这么严重”
		// 就是 <=。与字符串比较可以让 ClickHouse 解析该枚举，而无需我们硬编码
		// 数值。Validate() 已经规范化了 opts.Level，但 buildSQL 也可以被
		// 单独调用（测试、未来的调用方）—— 因此再次解析，并且只拼接
		// 已被证明属于成员的拼写，绝不拼接原始输入。
		if canonical, ok := canonicalLevel(opts.Level); ok {
			levelFilter = fmt.Sprintf("  AND level <= '%s'\n", canonical)
		}
	}

	return fmt.Sprintf(`SELECT
    event_time_microseconds                                AS ts,
    hostName()                                             AS hostname,
    level,
    thread_id,
    logger_name,
    message,
%s    source_file,
    source_line,
    query_id
FROM %s
WHERE event_time >= toDateTime('%s', 'UTC')
  AND event_time <= toDateTime('%s', 'UTC')
%sORDER BY event_time_microseconds ASC
LIMIT %d
FORMAT %s`,
		formatCol,
		SysTable(c.mode, "text_log"),
		opts.From.UTC().Format("2006-01-02 15:04:05"),
		opts.To.UTC().Format("2006-01-02 15:04:05"),
		levelFilter,
		opts.RowLimit,
		c.outputFormat().Clause)
}

// isAtLeast 报告 v >= major.minor 是否成立。
func isAtLeast(v internal.Version, major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}
