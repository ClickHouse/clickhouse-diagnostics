package dashboard

import (
	"strings"
	"testing"
)

// 每个dashboard都说明自己属于哪种诊断。精简诊断——
// 服务器吃紧，或使用-collect essential——只有1天窗口和更少的
// 文件，因此banner必须说明这一点，以免有人把数据缺失当作健康，
// 列出被跳过的内容，并说明如何获得完整视图。
func TestTemplate_CollectBanner(t *testing.T) {
	for _, want := range []string{
		`<div id="collect-banner" class="collect-banner" style="display:none"></div>`,
		"精简诊断（essential）", "完整诊断（full）", "<code>-collect full</code>",
		"此处数据缺失并不能证明系统健康",
		"esc(c.summary", "sc.slice(0,12).map(esc)", "sp.map(esc)",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("模板缺少%q", want)
		}
	}
	for _, banned := range []string{"'+c.summary+'", "+sc.join(", "+sp.join("} {
		if strings.Contains(htmlTemplate, banned) {
			t.Errorf("banner未转义地插值：%q", banned)
		}
	}
	// banner是告警部分的第一项。
	if i, j := strings.Index(htmlTemplate, `id="collect-banner"`), strings.Index(htmlTemplate, "🚨 告警摘要"); i < 0 || i > j {
		t.Error("banner不在告警摘要上方")
	}
}

// essential generator不会构建任何读取全表的面板。
func TestGenerator_EssentialSkipsHeavyPanels(t *testing.T) {
	g := NewGenerator(nil, "onprem").WithCollectMode("essential", "memory at 94 %", []string{"system.parts"})
	for label := range heavyPanels {
		if rows := g.safeQuery(label, "SELECT 1"); len(rows) != 0 {
			t.Errorf("%s在essential运行中被构建", label)
		}
	}
	if len(g.skippedPanels) != len(heavyPanels) {
		t.Errorf("跳过的面板%v", g.skippedPanels)
	}
}

// 超过运行的时间预算后，每个实时面板查询都会被跳过并记录名称；
// 没有Keeper会话时，Keeper面板也一样。
func TestGenerator_BudgetAndKeeperDownSkip(t *testing.T) {
	g := NewGenerator(nil, "onprem").WithStop(func() bool { return true })
	if rows := g.safeQuery("query_by_time", "SELECT 1"); len(rows) != 0 || len(g.skippedPanels) != 1 || !strings.Contains(g.skippedPanels[0], "时间预算") {
		t.Fatalf("预算：rows=%v skipped=%v", rows, g.skippedPanels)
	}
	k := NewGenerator(nil, "onprem").WithKeeperDown(true)
	if rows := k.safeQuery("keeper_connection", "SELECT 1"); len(rows) != 0 || len(k.skippedPanels) != 1 || !strings.Contains(k.skippedPanels[0], "无 Keeper 会话") {
		t.Fatalf("Keeper不可用：rows=%v skipped=%v", rows, k.skippedPanels)
	}
}
