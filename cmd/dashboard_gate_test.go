package main

import (
	"strings"
	"testing"
)

// gov 模式绝不能生成 dashboard.html。dashboard 面板会选择原始标识符，
// 而 queries.gov/*.sql 的存在正是为了对这些标识符做哈希，且 dashboard 与它们
// 打包在同一个归档里——因此这是一条披露规则，而不是偏好设置。
func TestDashboardDecision_GovNeverGenerates(t *testing.T) {
	for _, skip := range []bool{false, true} {
		generate, reason := dashboardDecision(skip, "gov")
		if generate {
			t.Fatalf("gov 模式不得生成 dashboard（skipDashboard=%v）", skip)
		}
		if reason == "" {
			t.Errorf("gov 拒绝必须说明原因（skipDashboard=%v）", skip)
		}
	}
	// 未设置 --skip-dashboard 时，给出的原因必须是 gov 相关的那条，这样
	// 运维人员能知道这是策略限制，而不是某个标志或故障。
	_, reason := dashboardDecision(false, "gov")
	if !strings.Contains(reason, "gov 模式") {
		t.Errorf("gov 拒绝应指明 gov 模式，实际得到 %q", reason)
	}
}

// gov 拒绝不得依赖调用方是否已规范化 mode。
// getUserInput 在今天到达这里之前会转成小写，因此这些形式从 main() 不可达——
// 这正是重点：守卫放在这里，是为了将来新增的调用点无法重新引入泄露，
// 而且 "GOV" 已经在这个文件里绕过一次裸的 == "gov" 比较。
func TestDashboardDecision_GovIsNormalised(t *testing.T) {
	for _, mode := range []string{"GOV", "Gov", " gov", "gov ", "\tGOV\n"} {
		if generate, _ := dashboardDecision(false, mode); generate {
			t.Errorf("模式 %q 生成了 dashboard——无论大小写或空白，gov 都必须被拒绝", mode)
		}
	}
}

func TestDashboardDecision_NonGov(t *testing.T) {
	for _, mode := range []string{"onprem", "cloud"} {
		if generate, reason := dashboardDecision(false, mode); !generate {
			t.Errorf("模式 %q 应生成 dashboard，却被拒绝，原因 %q", mode, reason)
		}
		// 显式的 --skip-dashboard 会被遵守，并被报告为该标志所致，
		// 而不是策略拒绝。
		generate, reason := dashboardDecision(true, mode)
		if generate {
			t.Errorf("模式 %q 忽略了 --skip-dashboard", mode)
		}
		if !strings.Contains(reason, "--skip-dashboard") {
			t.Errorf("模式 %q 应把跳过归因于该标志，实际得到 %q", mode, reason)
		}
	}
}
