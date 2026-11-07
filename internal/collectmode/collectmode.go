// Package collectmode 决定诊断采集多少内容：全部
// （"full"），还是当服务器明显吃力时只采集决定大多数故障的
// 廉价表（"essential"）。采集器之前的短暂探测
// 决定起始模式（Decide）；运行期间的 watchdog 在查询开始超时或
// 变得极慢时切换到 essential（Watchdog）。运维人员可以用 -collect
// 强制指定任一种。
package collectmode

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Mode 是 -collect 所要求的值，或最终决定的模式。
type Mode string

const (
	Auto      Mode = "auto"
	Essential Mode = "essential"
	Full      Mode = "full"
)

// Parse 读取 -collect flag 的值；空值表示 auto。
func Parse(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", Auto:
		return Auto, nil
	case Essential, Full:
		return m, nil
	}
	return "", fmt.Errorf("-collect 必须是 auto、essential 或 full（实际为 %q）", s)
}

// Signals 是探测读取到的内容。零值限制表示“未知”：
// 与未知限制相比的比率绝不构成理由。
type Signals struct {
	Ping                 time.Duration // SELECT 1
	MetricsRead          time.Duration // 探测对 system.metrics 的读取
	Queries              int64         // system.metrics Query
	MaxConcurrentQueries int64         // 服务器设置 max_concurrent_queries
	MemoryTracking       int64         // system.metrics MemoryTracking，字节
	MemoryLimit          int64         // 实际生效的服务器内存限制，字节
	Load1                float64       // asynchronous_metrics LoadAverage1
	CPUs                 float64       // 服务器使用的核心数（max_threads auto）
	ReadonlyReplicas     int64         // system.metrics ReadonlyReplica
	KeeperSessions       int64         // system.metrics ZooKeeperSession
	// KeeperLossSince 是 ZooKeeperConnectionLossStartedTimestampSeconds：
	// 与 Keeper 的连接丢失期间非零（已过期的会话
	// 仍计入 ZooKeeperSession）。没有该指标的版本为 0。
	KeeperLossSince int64
	Tables          int64 // asynchronous_metrics NumberOfTables
}

// Thresholds 来自现场：它们以升级案例中节点明显陷入困境的
// 位置为起点，意在调整，而不是盲信。
const (
	slowPing        = 1 * time.Second
	slowMetrics     = 2 * time.Second
	concurrentRatio = 0.8
	memoryRatio     = 0.9
	loadPerCPU      = 2.0
	// LargeTables 标记一个即使健康时逐表读取也很昂贵的服务器
	// （每张表一次列举 / Keeper 读取）。
	LargeTables = 10000
)

// Decision 是起始模式及其原因。
type Decision struct {
	Requested Mode
	Mode      Mode     // Essential 或 Full
	Reasons   []string // 为何选择 essential（full 时为空）
	Large     bool     // 表数 ≥ LargeTables
	// KeeperDown：复制表只读且没有 Keeper 会话。
	// 需要活动会话的查询（system.zookeeper_connection、
	// database_replicas、由 Keeper 支撑的 replicas 列）随后会
	// 等待超时而不是返回结果，因此本次运行跳过它们。
	KeeperDown bool
	Signals    Signals
}

// Decide 将探测信号转换为起始模式。probeErr 是
// 探测自身的失败：无法回答三次内存读取的服务器
// 会被视为处于困境。
func Decide(requested Mode, s Signals, probeErr error) Decision {
	d := Decision{Requested: requested, Signals: s, Large: s.Tables >= LargeTables,
		KeeperDown: probeErr == nil && s.ReadonlyReplicas > 0 && (s.KeeperSessions == 0 || s.KeeperLossSince > 0)}
	var why []string
	if probeErr != nil {
		why = append(why, "负载探测本身失败（"+firstLine(probeErr.Error())+"）")
	} else {
		if s.Ping >= slowPing {
			why = append(why, fmt.Sprintf("服务器响应 SELECT 1 用了 %.1f 秒", s.Ping.Seconds()))
		}
		if s.MetricsRead >= slowMetrics {
			why = append(why, fmt.Sprintf("读取 system.metrics 用了 %.1f 秒", s.MetricsRead.Seconds()))
		}
		if s.MaxConcurrentQueries > 0 && float64(s.Queries) >= concurrentRatio*float64(s.MaxConcurrentQueries) {
			why = append(why, fmt.Sprintf("有 %d 条查询正在运行，max_concurrent_queries = %d", s.Queries, s.MaxConcurrentQueries))
		}
		if s.MemoryLimit > 0 && float64(s.MemoryTracking) >= memoryRatio*float64(s.MemoryLimit) {
			why = append(why, fmt.Sprintf("内存已达服务器上限的 %.0f%%", 100*float64(s.MemoryTracking)/float64(s.MemoryLimit)))
		}
		if s.CPUs > 0 && s.Load1 >= loadPerCPU*s.CPUs {
			why = append(why, fmt.Sprintf("%.0f 核上的 load average 为 %.1f", s.CPUs, s.Load1))
		}
		if s.ReadonlyReplicas > 0 {
			cause := "Keeper 会话或磁盘故障"
			if d.KeeperDown {
				cause = "没有 Keeper 会话"
				if s.KeeperLossSince > 0 {
					cause = "Keeper 连接自 " + time.Unix(s.KeeperLossSince, 0).UTC().Format("2006-01-02 15:04:05 UTC") + " 起断开"
				}
			}
			why = append(why, fmt.Sprintf("%d 张复制表处于只读状态（%s）", s.ReadonlyReplicas, cause))
		}
	}
	switch requested {
	case Full:
		d.Mode = Full
	case Essential:
		d.Mode = Essential
	default:
		d.Mode = Full
		if len(why) > 0 {
			d.Mode = Essential
		}
	}
	d.Reasons = why
	return d
}

// Summary 是供控制台、execution_log.txt
// 和 dashboard 横幅使用的一行解释。
func (d Decision) Summary() string {
	switch {
	case d.Requested == Essential:
		return "essential collection（精简采集），由 -collect essential 指定"
	case d.Requested == Full:
		s := "full collection（完整采集），由 -collect full 指定"
		if len(d.Reasons) > 0 {
			s += "（探测看到：" + strings.Join(d.Reasons, "；") + "）"
		}
		return s
	case d.Mode == Essential:
		return "essential collection（精简采集），自动选择：" + strings.Join(d.Reasons, "；")
	default:
		return "full collection（完整采集，服务器正常响应了负载探测）"
	}
}

// essential 采集在哪些方面有所不同。
const (
	EssentialQueryTimeout = 30 * time.Second // 每条查询，除非指定了 -query-timeout
	EssentialBudget       = 3 * time.Minute  // 整个采集，除非指定了 -time-budget
	EssentialLookback     = 24 * time.Hour   // 每个 3 天和 7 天的默认窗口都变为 1 天
)

// slowStreak：连续两条查询至少这么慢，就把本次运行的
// 其余部分切换到 essential。
const slowQuery = 30 * time.Second

// Watchdog 监视采集器，并在服务器开始超时或变得极慢时
// 将本次运行的其余部分切换为 essential，同时执行
// 总体时间预算。
type Watchdog struct {
	budget     time.Duration
	tripBudget time.Duration
	started    time.Time
	tripped    bool
	reason     string
	lastSlow   string
	onTrip     func(reason string)
	locked     bool
	now        func() time.Time
}

// Locked 让 watchdog 绝不把运行切换到 essential（-collect
// full）；其预算（如果有）仍然生效。
func (w *Watchdog) Locked() *Watchdog {
	w.locked = true
	return w
}

// WithTripBudget 设置运行中途切换到 essential 时开始计时的
// 预算（仅在未给出总体预算时使用）。
func (w *Watchdog) WithTripBudget(d time.Duration) *Watchdog {
	w.tripBudget = d
	return w
}

// OnTrip 注册一个回调，在运行变为 essential 时执行一次。
func (w *Watchdog) OnTrip(f func(reason string)) {
	if w != nil {
		w.onTrip = f
	}
}

// NewWatchdog 返回一个 watchdog；budget 为 0 表示无上限。
func NewWatchdog(budget time.Duration) *Watchdog {
	return &Watchdog{budget: budget, now: time.Now}
}

// Start 启动预算时钟（在第一个采集器之前调用一次）。
func (w *Watchdog) Start() {
	if w != nil {
		w.started = w.now()
	}
}

var reTimeout = regexp.MustCompile(`(?i)TIMEOUT_EXCEEDED|Code: 159\b|deadline exceeded|Client\.Timeout|timeout awaiting|i/o timeout`)

// Observe 记录一条查询的结果。
func (w *Watchdog) Observe(name string, d time.Duration, err error) {
	if w == nil || w.tripped {
		return
	}
	if err != nil && reTimeout.MatchString(err.Error()) {
		w.trip(fmt.Sprintf("%s timed out after %.0f s", name, d.Seconds()))
		return
	}
	if d >= slowQuery {
		if w.lastSlow != "" {
			w.trip(fmt.Sprintf("%s 和 %s 各用了超过 %.0f 秒", w.lastSlow, name, slowQuery.Seconds()))
			return
		}
		w.lastSlow = name
		return
	}
	w.lastSlow = ""
}

func (w *Watchdog) trip(reason string) {
	if w.locked {
		return
	}
	w.tripped, w.reason = true, reason
	if w.budget <= 0 && w.tripBudget > 0 {
		w.budget, w.started = w.tripBudget, w.now()
	}
	if w.onTrip != nil {
		w.onTrip(reason)
	}
}

// Trip 因外部原因切换到 essential（例如预先选择了
// essential 模式），并保留给出的第一个原因。
func (w *Watchdog) Trip(reason string) {
	if w != nil && !w.tripped {
		w.trip(reason)
	}
}

// Tripped 报告运行现在是否为 essential，以及原因。
func (w *Watchdog) Tripped() (bool, string) {
	if w == nil {
		return false, ""
	}
	return w.tripped, w.reason
}

// OverBudget 报告总体时间预算是否已用完。
func (w *Watchdog) OverBudget() bool {
	if w == nil || w.budget <= 0 || w.started.IsZero() {
		return false
	}
	return w.now().Sub(w.started) > w.budget
}

// Budget 返回配置的预算（0 = 无上限）。
func (w *Watchdog) Budget() time.Duration {
	if w == nil {
		return 0
	}
	return w.budget
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
