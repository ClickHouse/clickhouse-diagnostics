package query

import (
	"fmt"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/collectmode"
	"clickhouse-diagnostic/internal/runlog"
	"clickhouse-diagnostic/pkg"
)

// Manager 负责编排查询的发现、选择和执行
type Manager struct {
	finder   *Finder
	selector *Selector
	format   OutputFormat
	from     time.Time
	to       time.Time
	mode     string
	rec      *runlog.Recorder
	watch    *collectmode.Watchdog
	lookback time.Duration
	scope    [2]string
	keeperDn string
}

// WithKeeperDown 把无 Keeper 会话的原因传给 executor。
func (m *Manager) WithKeeperDown(reason string) *Manager {
	m.keeperDn = reason
	return m
}

// WithDetachedScope 把 detached part 的查询范围传给 executor。
func (m *Manager) WithDetachedScope(scope, qualified string) *Manager {
	m.scope = [2]string{scope, qualified}
	return m
}

// WithWatchdog 把采集模式 watchdog 传给 executor。
func (m *Manager) WithWatchdog(w *collectmode.Watchdog) *Manager {
	m.watch = w
	return m
}

// WithMaxLookback 为每个查询的默认窗口设上限（essential：1 天）。
func (m *Manager) WithMaxLookback(d time.Duration) *Manager {
	m.lookback = d
	return m
}

// WithMode 设置用于 {sys.<table>} 展开的拓扑模式。
func (m *Manager) WithMode(mode string) *Manager {
	m.mode = mode
	return m
}

// WithWindow 覆盖每个声明了时间窗口的查询的窗口。
func (m *Manager) WithWindow(from, to time.Time) *Manager {
	m.from, m.to = from, to
	return m
}

// WithOutputFormat 设置查询结果的序列化格式。
// WithRecorder 挂接执行日志记录器，并传给 executor。
func (m *Manager) WithRecorder(r *runlog.Recorder) *Manager {
	m.rec = r
	return m
}

func (m *Manager) WithOutputFormat(f OutputFormat) *Manager {
	m.format = f
	return m
}

// NewManager 创建一个新的查询管理器
func NewManager() *Manager {
	return &Manager{
		finder:   NewFinder(),
		selector: NewSelector(),
		format:   DefaultOutputFormat,
	}
}

// ExecuteQueries 为给定的 ClickHouse 实例发现、选择并执行查询。
// 当 salt 非空（gov 模式）时，它会在 .sql 文件发送给 ClickHouse 之前
// 替换其中的 '%salt%' 占位符。
// 返回保存结果的具体目录路径。
func (m *Manager) ExecuteQueries(client *pkg.ClickHouseClient, queriesDir string, serverVersion internal.Version, outputDir, salt string) (string, error) {
	// 校验查询目录
	if err := m.finder.ValidateQueryDirectory(queriesDir); err != nil {
		return "", fmt.Errorf("查询目录校验失败：%w", err)
	}

	// 查找所有兼容的查询
	allQueries, err := m.finder.FindCompatibleQueries(queriesDir, serverVersion)
	if err != nil {
		return "", fmt.Errorf("查找查询文件失败：%w", err)
	}

	if len(allQueries) == 0 {
		return "", fmt.Errorf("在 '%s' 或其子目录中未找到合适的查询文件", queriesDir)
	}

	// 选择优先级最高的查询
	selectedQueries := m.selector.SelectHighestPriorityQueries(allQueries)

	fmt.Printf("找到 %d 个待执行的唯一查询文件\n", len(selectedQueries))

	// 执行选中的查询，并获得具体的输出目录
	executor := NewExecutor(client).WithSalt(salt).WithOutputFormat(m.format).
		WithWindow(m.from, m.to).WithMode(m.mode).WithRecorder(m.rec).
		WithWatchdog(m.watch).WithMaxLookback(m.lookback).WithDetachedScope(m.scope[0], m.scope[1]).WithKeeperDown(m.keeperDn)
	finalOutputDir, err := executor.ExecuteQueries(selectedQueries, outputDir)
	if err != nil {
		return "", fmt.Errorf("执行查询失败：%w", err)
	}

	return finalOutputDir, nil
}

// GetQuerySummary 返回可用查询的汇总
func (m *Manager) GetQuerySummary(queriesDir string, serverVersion internal.Version) (map[string]int, error) {
	allQueries, err := m.finder.FindCompatibleQueries(queriesDir, serverVersion)
	if err != nil {
		return nil, err
	}

	selectedQueries := m.selector.SelectHighestPriorityQueries(allQueries)
	groups := m.selector.GroupQueriesByType(selectedQueries)

	summary := make(map[string]int)
	for group, queries := range groups {
		summary[group] = len(queries)
	}

	return summary, nil
}
