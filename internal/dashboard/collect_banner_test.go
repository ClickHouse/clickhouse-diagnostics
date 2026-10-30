package dashboard

import (
	"strings"
	"testing"
)

// Every dashboard says what kind of diagnostic it is. An essential one —
// a struggling server, or -collect essential — has 1-day windows and fewer
// files, so the banner has to say so before anyone reads an absence as
// health, list what was skipped, and say how to get the full picture.
func TestTemplate_CollectBanner(t *testing.T) {
	for _, want := range []string{
		`<div id="collect-banner" class="collect-banner" style="display:none"></div>`,
		"Essential diagnostic", "Full diagnostic", "<code>-collect full</code>",
		"Missing data here is not evidence of health",
		"esc(c.summary", "sc.slice(0,12).map(esc)", "sp.map(esc)",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("template lacks %q", want)
		}
	}
	for _, banned := range []string{"'+c.summary+'", "+sc.join(", "+sp.join("} {
		if strings.Contains(htmlTemplate, banned) {
			t.Errorf("banner interpolates unescaped: %q", banned)
		}
	}
	// The banner is the first thing in the alert section.
	if i, j := strings.Index(htmlTemplate, `id="collect-banner"`), strings.Index(htmlTemplate, "🚨 Alert Summary"); i < 0 || i > j {
		t.Error("banner is not above the alert summary")
	}
}

// An essential generator builds none of the panels that read every table.
func TestGenerator_EssentialSkipsHeavyPanels(t *testing.T) {
	g := NewGenerator(nil, "onprem").WithCollectMode("essential", "memory at 94 %", []string{"system.parts"})
	for label := range heavyPanels {
		if rows := g.safeQuery(label, "SELECT 1"); len(rows) != 0 {
			t.Errorf("%s built in an essential run", label)
		}
	}
	if len(g.skippedPanels) != len(heavyPanels) {
		t.Errorf("skipped panels %v", g.skippedPanels)
	}
}

// Past the run's time budget every live panel query is skipped and named;
// with no Keeper session the Keeper panel is.
func TestGenerator_BudgetAndKeeperDownSkip(t *testing.T) {
	g := NewGenerator(nil, "onprem").WithStop(func() bool { return true })
	if rows := g.safeQuery("query_by_time", "SELECT 1"); len(rows) != 0 || len(g.skippedPanels) != 1 || !strings.Contains(g.skippedPanels[0], "time budget") {
		t.Fatalf("budget: rows=%v skipped=%v", rows, g.skippedPanels)
	}
	k := NewGenerator(nil, "onprem").WithKeeperDown(true)
	if rows := k.safeQuery("keeper_connection", "SELECT 1"); len(rows) != 0 || len(k.skippedPanels) != 1 || !strings.Contains(k.skippedPanels[0], "no Keeper session") {
		t.Fatalf("keeper down: rows=%v skipped=%v", rows, k.skippedPanels)
	}
}
