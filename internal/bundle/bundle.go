// Package bundle 读取采集器保存在运行目录中的内容，使告警检查和 dashboard
// 基于已采集的数据工作，而不是再次查询服务器。
//
// 采集器的文件名是 <collector>_<YYYYMMDD_HHMMSS>.jsonl。文件按
// 精确的采集器名加上运行时间戳匹配：前缀匹配
// 会把 system.replicas 和 system.replicas_problem_tables 混在一起。
package bundle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status 说明某个采集器为何没有文件：skipped（essential 采集、
// 时间预算、无 Keeper 会话）或 failed，并附带原因。
type Status struct {
	State  string // "skipped" 或 "failed"
	Reason string
}

// NotCollected 在一个采集器没有结果文件时返回。收到它的检查
// 会把其规则报告为不适用，而绝不是正常。
type NotCollected struct {
	Collector string
	State     string // "skipped"、"failed" 或 "not collected"
	Reason    string
}

func (e *NotCollected) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("%s %s", e.Collector, e.State)
	}
	return fmt.Sprintf("%s %s: %s", e.Collector, e.State, e.Reason)
}

// Bundle 是一个运行目录。
type Bundle struct {
	dir    string
	status map[string]Status
	mu     sync.Mutex
	cache  map[string][]Row
	end    time.Time
}

// Open 索引 dir。status 来自执行日志，用于说明那些没有文件的
// 采集器；它可以为 nil。
func Open(dir string, status map[string]Status) *Bundle {
	return &Bundle{dir: dir, status: status, cache: map[string][]Row{}}
}

// WithWindowEnd 记录采集窗口的显式结束时间（-to）：
// 日志文件到此为止，因此“最近 N 小时”以此为判定上限。
func (b *Bundle) WithWindowEnd(t time.Time) *Bundle {
	b.end = t
	return b
}

// WindowEnd 是显式的窗口结束时间；当本次运行一直采集到运行时刻时为零值。
func (b *Bundle) WindowEnd() time.Time { return b.end }

// Dir 是运行目录。
func (b *Bundle) Dir() string { return b.dir }

var reRunFile = regexp.MustCompile(`^(.+)_(\d{8}_\d{6})\.jsonl$`)

// path 返回该采集器的文件，没有时返回 ""。
func (b *Bundle) path(collector string) string {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if m := reRunFile.FindStringSubmatch(e.Name()); m != nil && m[1] == collector {
			return filepath.Join(b.dir, e.Name())
		}
	}
	return ""
}

// Has 报告该采集器是否产出了文件。
func (b *Bundle) Has(collector string) bool { return b.path(collector) != "" }

// Rows 返回该采集器的行，文件只读取一次。
func (b *Bundle) Rows(collector string) ([]Row, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rows, ok := b.cache[collector]; ok {
		return rows, nil
	}
	// 失败的采集器即使留下了文件，也可能是部分写入
	// （磁盘写满）：以失败状态为准。
	if st, ok := b.status[collector]; ok && st.State == "failed" {
		return nil, &NotCollected{Collector: collector, State: st.State, Reason: st.Reason}
	}
	p := b.path(collector)
	if p == "" {
		if st, ok := b.status[collector]; ok {
			return nil, &NotCollected{Collector: collector, State: st.State, Reason: st.Reason}
		}
		return nil, &NotCollected{Collector: collector, State: "not collected"}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []Row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r map[string]any
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		rows = append(rows, Row(r))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	b.cache[collector] = rows
	return rows, nil
}

// Row 是一行采集结果。64 位整数以带引号的字符串到达
// （output_format_json_quote_64bit_integers=1）；各访问器两种形式都能读取。
type Row map[string]any

// Str 以文本形式返回该列（缺失时为 ""）。
func (r Row) Str(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// Int 以 int64 形式返回该列（缺失或不是数字时为 0）。
func (r Row) Int(k string) int64 {
	s := r.Str(k)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	if b, ok := r[k].(bool); ok && b {
		return 1
	}
	return 0
}

// Uint 以 uint64 形式返回该列，对满宽度 UInt64 值保持精确。
func (r Row) Uint(k string) uint64 {
	if n, err := strconv.ParseUint(r.Str(k), 10, 64); err == nil {
		return n
	}
	if n := r.Int(k); n > 0 {
		return uint64(n)
	}
	return 0
}

// Float 以 float64 形式返回该列。
func (r Row) Float(k string) float64 {
	f, _ := strconv.ParseFloat(r.Str(k), 64)
	return f
}

// Map 返回 Map(String, number) 列（例如错误映射）。
func (r Row) Map(k string) map[string]int64 {
	out := map[string]int64{}
	if m, ok := r[k].(map[string]any); ok {
		for key, v := range m {
			out[key] = Row{"v": v}.Int("v")
		}
	}
	return out
}
