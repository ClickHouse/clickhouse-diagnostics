package main

import (
	"fmt"
	"strings"
	"time"

	"clickhouse-diagnostic/internal/collectmode"
	"clickhouse-diagnostic/internal/runlog"
	"clickhouse-diagnostic/pkg"
)

// gentleSettings 避免让本就吃力的服务器雪上加霜：减少线程数、内存上限，
// 以及最低调度优先级（在 ClickHouse 中 priority 值越大越晚执行）。
var gentleSettings = []struct{ name, value string }{
	{"max_threads", "2"},
	{"max_memory_usage", "1073741824"},
	{"priority", "10"},
}

// setupCollection 执行负载探测（dry-run 下不执行，因为 dry-run 只允许发送
// 元数据查询），决定起始模式，并返回各 collector 运行时所受的 watchdog。
func setupCollection(client *pkg.ClickHouseClient, requested collectmode.Mode, budget time.Duration, dryRun bool) (collectmode.Decision, *collectmode.Watchdog) {
	var d collectmode.Decision
	if dryRun {
		d = collectmode.Decide(requested, collectmode.Signals{}, nil)
	} else {
		s, err := collectmode.Probe(client)
		d = collectmode.Decide(requested, s, err)
	}
	if requested == collectmode.Full {
		// -collect full 是一种承诺：不会切换到 essential，只在指定了
		// 预算时受预算限制。
		return d, collectmode.NewWatchdog(budget).Locked()
	}
	return d, collectmode.NewWatchdog(budget).WithTripBudget(collectmode.EssentialBudget)
}

// applyEssential 让后续运行更温和：较短的每查询超时（除非运维人员已自行指定）
// 以及 gentle 设置。每个设置都会用 SELECT 1 验证，否则带约束的 settings
// profile 会让后续所有查询失败；被拒绝的设置会被省略并报告。
func applyEssential(client *pkg.ClickHouseClient, timeoutExplicit bool) []string {
	var notes []string
	client.SetClientMargin(5 * time.Second)
	if !timeoutExplicit {
		client.SetQueryTimeout(collectmode.EssentialQueryTimeout)
		notes = append(notes, fmt.Sprintf("查询超时 %v", collectmode.EssentialQueryTimeout))
	}
	var kept, refused []string
	for _, s := range gentleSettings {
		client.SetSetting(s.name, s.value)
		if _, err := client.ExecuteQueryReal("SELECT 1"); err != nil {
			client.RemoveSetting(s.name)
			refused = append(refused, s.name)
			continue
		}
		kept = append(kept, s.name+"="+s.value)
	}
	if len(kept) > 0 {
		notes = append(notes, strings.Join(kept, ", "))
	}
	if len(refused) > 0 {
		notes = append(notes, "被设置配置拒绝，已省略："+strings.Join(refused, ", "))
	}
	return notes
}

// collectKind 在运行期间任一时刻为 essential（一开始就选择或中途切换）时
// 返回 "essential"，否则返回 "full"。
func collectKind(w *collectmode.Watchdog) string {
	if tripped, _ := w.Tripped(); tripped {
		return string(collectmode.Essential)
	}
	return string(collectmode.Full)
}

// skippedCollectors 列出本次运行未执行的 collector。
func skippedCollectors(rec *runlog.Recorder) []string {
	var out []string
	for _, e := range rec.Entries() {
		if e.Stage == "collector" && e.Status == "skipped" {
			out = append(out, strings.TrimSuffix(e.Name, ".sql"))
		}
	}
	return out
}
