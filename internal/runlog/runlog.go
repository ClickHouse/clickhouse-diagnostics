// Package runlog 记录一次诊断运行做了什么——每条采集器查询及其结果与耗时、
// 每条告警规则，以及每个阶段的墙钟时间——并将其作为 execution_log.txt 写入 bundle。
//
// 该文件回答结果文件无法回答的两个问题：“哪些采集器没有运行，为什么？”
// （否则缺失的文件与空表无法区分）以及“哪些采集器在这台服务器上开销大？”，
// 以便依据真实数据调整 queries.<mode>/ 中的窗口和形态。
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileName 是写入运行目录的产物文件名。
const FileName = "execution_log.txt"

// errorCap 限制每条记录保留的错误文本长度：足以容纳
// ClickHouse 错误码和第一句话，而不是堆栈跟踪。
const errorCap = 300

// Entry 是一个工作单元：一次采集器查询、一条告警规则、一段 text_log
// 切片或一个分析文件。
type Entry struct {
	Stage    string // collector | alert | text_log | analysis
	Name     string // 文件名（system.parts.sql、too_many_parts.yaml……）
	Source   string // 来源版本目录，"" 表示根目录
	Status   string // ok | failed | empty | skipped | fired | clean
	Duration time.Duration
	Bytes    int64  // 写入的结果字节数（采集器）
	Rows     int64  // 面向行格式时的结果行数；-1 表示未知
	Extra    string // 阶段相关的备注（告警实例数、跳过原因）
	Error    string
}

// Phase 是运行中某一步的墙钟时间。
type Phase struct {
	Name     string
	Duration time.Duration
	Note     string
}

// Recorder 累积记录项和阶段。可安全地并发调用 Record。
type Recorder struct {
	mu      sync.Mutex
	started time.Time
	meta    []kv
	entries []Entry
	phases  []Phase
	// gov 在存储前剥离自由文本：服务器错误消息会点名数据库、
	// 表、part、路径和主机，而该日志与其他位置一样，
	// 会进入对标识符做了哈希的同一个归档。只有
	// ClickHouse 错误码及其常量名会保留。
	gov bool
}

// WithGov 启用对每个自由文本字段的 gov 模式脱敏。
func (r *Recorder) WithGov(on bool) *Recorder {
	if r != nil {
		r.gov = on
	}
	return r
}

var reErrCode = regexp.MustCompile(`Code: (\d+)`)

// redactError 只保留数字形式的 ClickHouse 错误码，例如 "Code: 60"。
// 常量名特意不提取：服务器消息中带括号的全大写
// token 在没有 ClickHouse 常量列表的情况下，无法与
// "(CUSTOMER_TABLE)" 这类客户文本区分，而读者
// 可以用 errorCodeToName() 或 skill 的 error-codes.md 解析错误码。
// 没有错误码的消息会变成固定标记。
func redactError(msg string) string {
	if msg == "" {
		return ""
	}
	if code := reErrCode.FindStringSubmatch(msg); code != nil {
		return fmt.Sprintf("Code: %s — gov 模式下已隐去消息", code[1])
	}
	return "gov 模式下已隐去错误文本"
}

type kv struct{ k, v string }

// New 启动一个记录器；起始时间即本次运行的开始时间。
func New() *Recorder { return &Recorder{started: time.Now()} }

// SetMeta 添加一行头部信息（mode、服务器版本、目标、窗口……）。
func (r *Recorder) SetMeta(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.meta {
		if r.meta[i].k == key {
			r.meta[i].v = value
			return
		}
	}
	r.meta = append(r.meta, kv{key, value})
}

// Meta 返回之前设置的头部值，没有时返回 ""——dashboard 头部
// 会重复 node / uptime / collection 行，使两个产物保持一致。
func (r *Recorder) Meta(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.meta {
		if m.k == key {
			return m.v
		}
	}
	return ""
}

// Record 追加一条记录。错误文本在此处截断，确保文件
// 永远不会携带堆栈跟踪。
func (r *Recorder) Record(e Entry) {
	if r == nil {
		return
	}
	if r.gov {
		e.Error = redactError(e.Error)
		// Extra 携带输出文件名（安全）或告警实例数
		// （安全）；阶段式的自由文本永远不会进入 Extra。无需其他处理。
	}
	if len(e.Error) > errorCap {
		e.Error = e.Error[:errorCap] + "…"
	}
	e.Error = strings.ReplaceAll(strings.ReplaceAll(e.Error, "\n", " "), "|", "/")
	r.mu.Lock()
	r.entries = append(r.entries, e)
	r.mu.Unlock()
}

// Phase 追加一个阶段耗时。
func (r *Recorder) Phase(name string, d time.Duration, note string) {
	if r == nil {
		return
	}
	if r.gov && note != "" {
		note = redactError(note)
	}
	r.mu.Lock()
	r.phases = append(r.phases, Phase{name, d, note})
	r.mu.Unlock()
}

// Entries 返回已记录条目的副本（供测试和调用方使用）。
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Entry(nil), r.entries...)
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%d µs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2f s", d.Seconds())
	default:
		return fmt.Sprintf("%.1f min", d.Minutes())
	}
}

func fmtBytes(b int64) string {
	const k = 1024.0
	f := float64(b)
	for _, u := range []string{"B", "KiB", "MiB", "GiB"} {
		if f < k || u == "GiB" {
			if u == "B" {
				return fmt.Sprintf("%d B", b)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= k
	}
	return fmt.Sprintf("%d B", b)
}

func src(e Entry) string {
	if e.Source == "" {
		return "root"
	}
	return e.Source
}

// Render 生成 execution_log.txt 的文本。各节依次为：头部、摘要、
// 最耗时的采集器、失败的采集器、按执行顺序排列的所有条目
// （管道表，以便 skill 的预检脚本解析）、告警规则、
// 阶段。
func (r *Recorder) Render(now time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	w := func(format string, a ...interface{}) { fmt.Fprintf(&b, format, a...) }

	w("clickhouse-diagnostic 执行日志\n")
	w("===================================\n")
	w("started:  %s\n", r.started.Format("2006-01-02 15:04:05 -0700"))
	w("finished: %s   (total %s)\n", now.Format("2006-01-02 15:04:05 -0700"), fmtDur(now.Sub(r.started)))
	for _, m := range r.meta {
		w("%-9s %s\n", m.k+":", m.v)
	}

	var collectors, alerts, others []Entry
	for _, e := range r.entries {
		switch e.Stage {
		case "collector":
			collectors = append(collectors, e)
		case "alert":
			alerts = append(alerts, e)
		default:
			others = append(others, e)
		}
	}
	count := func(es []Entry, status string) int {
		n := 0
		for _, e := range es {
			if e.Status == status {
				n++
			}
		}
		return n
	}
	var collTotal time.Duration
	for _, e := range collectors {
		collTotal += e.Duration
	}

	w("\nSummary\n-------\n")
	w("collectors：%d 成功，%d 失败，%d 为空 — 查询耗时 %s\n",
		count(collectors, "ok"), count(collectors, "failed"), count(collectors, "empty"), fmtDur(collTotal))
	if len(alerts) > 0 {
		w("alerts：    %d 条规则 — %d 触发，%d 正常，%d 失败，%d 跳过\n",
			len(alerts), count(alerts, "fired"), count(alerts, "clean"), count(alerts, "failed"), count(alerts, "skipped"))
	}
	if len(others) > 0 {
		w("other：     %d 条记录（%d 失败）\n", len(others), count(others, "failed"))
	}
	if len(r.phases) > 0 {
		parts := make([]string, 0, len(r.phases))
		for _, p := range r.phases {
			parts = append(parts, fmt.Sprintf("%s %s", p.Name, fmtDur(p.Duration)))
		}
		w("phases：    %s\n", strings.Join(parts, " | "))
	}

	if len(collectors) > 0 {
		sorted := append([]Entry(nil), collectors...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Duration > sorted[j].Duration })
		w("\n最耗时的 collector（按实际耗时；据此调整 queries.<mode>/ 中的窗口）\n")
		w("--------------------------------------------------------------------------------\n")
		for i, e := range sorted {
			if i >= 10 {
				break
			}
			rows := ""
			if e.Rows >= 0 {
				rows = fmt.Sprintf("  %d 行", e.Rows)
			}
			w("%3d. %10s  %-48s (%s)  %s%s  %s\n", i+1, fmtDur(e.Duration), e.Name, src(e), fmtBytes(e.Bytes), rows, e.Status)
		}
	}

	if n := count(collectors, "failed"); n > 0 {
		w("\n失败的 collector（%d）——缺失的结果文件属于这一类，而不是空表\n", n)
		w("-------------------------------------------------------------------------------\n")
		for _, e := range collectors {
			if e.Status == "failed" {
				w("  %-48s (%s)  %s — %s\n", e.Name, src(e), fmtDur(e.Duration), e.Error)
			}
		}
	}

	w("\n全部记录（按执行顺序）\n-----------------------------\n")
	w("| # | stage | name | source | status | duration_ms | bytes | rows | note | error |\n")
	w("|---|---|---|---|---|---|---|---|---|---|\n")
	for i, e := range r.entries {
		rows := ""
		if e.Rows >= 0 {
			rows = fmt.Sprintf("%d", e.Rows)
		}
		w("| %d | %s | %s | %s | %s | %d | %d | %s | %s | %s |\n",
			i+1, e.Stage, e.Name, src(e), e.Status, e.Duration.Milliseconds(), e.Bytes, rows,
			strings.ReplaceAll(e.Extra, "|", "/"), e.Error)
	}

	if len(r.phases) > 0 {
		w("\n阶段\n------\n")
		for _, p := range r.phases {
			note := ""
			if p.Note != "" {
				note = "  — " + p.Note
			}
			w("  %-16s %10s%s\n", p.Name, fmtDur(p.Duration), note)
		}
	}
	w("\n归档步骤在本文件写出之后才运行，因此其耗时未记录在这里。\n")
	return b.String()
}

// Write 将日志渲染到 dir/execution_log.txt 并返回路径。
func (r *Recorder) Write(dir string) (string, error) {
	if r == nil {
		return "", nil
	}
	dst := filepath.Join(dir, FileName)
	// 与 bundle 中其他数据文件一样使用 0600：在 gov 之外，该文件
	// 会携带目标主机和原始服务器错误文本。
	if err := os.WriteFile(dst, []byte(r.Render(time.Now())), 0600); err != nil {
		return "", fmt.Errorf("write %s: %w", FileName, err)
	}
	return dst, nil
}
