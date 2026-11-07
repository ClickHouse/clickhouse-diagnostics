package alert

import (
	"fmt"
	"math"
	"sort"
	"time"

	"clickhouse-diagnostic/internal/bundle"
)

// Go check 共用的辅助函数：它们在已采集的文件上，
// 重新实现规则曾用到的那几个 SQL 函数。

// serverClock 返回日志窗口结束的时刻 —— 采集时服务器的 now()，
// 若显式 -to 更早则为该 -to（日志文件到那里为止）—— 以及服务器时区。
func serverClock(b *bundle.Bundle) (time.Time, *time.Location, error) {
	now, loc, err := collectedAt(b)
	if err != nil {
		return now, loc, err
	}
	if end := b.WindowEnd(); !end.IsZero() && end.Before(now) {
		now = end.In(loc)
	}
	return now, loc, nil
}

// collectedAt 是采集时服务器的 now()（system.version 的 collected_at），
// 用于从实时快照（mutations、parts）读取的时长 —— -to 不会移动它 ——
// 以及文件中每个 DateTime 写入所用的服务器时区。
func collectedAt(b *bundle.Bundle) (time.Time, *time.Location, error) {
	rows, err := b.Rows("system.version")
	if err != nil {
		return time.Time{}, nil, err
	}
	if len(rows) == 0 || rows[0].Str("collected_at") == "" {
		return time.Time{}, nil, fmt.Errorf("system.version 没有 collected_at（由旧版工具采集）")
	}
	loc := time.UTC
	if tz := rows[0].Str("timezone"); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05", rows[0].Str("collected_at"), loc)
	if err != nil {
		return time.Time{}, nil, fmt.Errorf("system.version collected_at 解析失败: %w", err)
	}
	return now, loc, nil
}

// serverHost 是 onprem/gov 文件所描述的主机（gov 中为哈希值）。
func serverHost(b *bundle.Bundle) string {
	if rows, err := b.Rows("system.version"); err == nil && len(rows) > 0 {
		return rows[0].Str("hostname")
	}
	return ""
}

// serverAtLeast 报告 system.version 是否至少为 major.minor
// （未知时返回 false）。
func serverAtLeast(b *bundle.Bundle, major, minor int) bool {
	rows, err := b.Rows("system.version")
	if err != nil || len(rows) == 0 {
		return false
	}
	var ma, mi int
	if _, err := fmt.Sscanf(rows[0].Str("version"), "%d.%d", &ma, &mi); err != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}

// parseTime 按服务器时区解析采集行中的 DateTime 字符串；
// 为空或格式错误时返回 ok=false。
func parseTime(s string, loc *time.Location) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04:05.000000", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// dateDiffHours 等价于 ClickHouse dateDiff('hour', a, b)：跨越的小时边界数，
// 而不是四舍五入后的经过小时数。
func dateDiffHours(a, b time.Time) int64 {
	ta := a.Truncate(time.Hour)
	tb := b.Truncate(time.Hour)
	return int64(tb.Sub(ta) / time.Hour)
}

// formatReadableSize 等价于 ClickHouse formatReadableSize：以 1024 为底，
// 保留两位小数（"100.00 B"、"1.50 GiB"）。
func formatReadableSize(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	i := 0
	for math.Abs(n) >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", n, units[i])
}

// roundTo 等价于 Float64 上的 ClickHouse round(x, d)：就近舍入，平局取偶。
func roundTo(x float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.RoundToEven(x*p) / p
}

// medianHigh 等价于 quantileExactHigh(0.5)：上中位数。
func medianHigh(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// row 按规则消息使用的形态构建结果行（列 → 值）。
type row = map[string]interface{}
