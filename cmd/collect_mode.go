package main

import (
	"fmt"
	"strings"
	"time"

	"clickhouse-diagnostic/internal/collectmode"
	"clickhouse-diagnostic/internal/runlog"
	"clickhouse-diagnostic/pkg"
)

// gentleSettings keep the tool from making a struggling server worse: few
// threads, a memory cap, and the lowest scheduling priority (in ClickHouse a
// larger priority value runs later).
var gentleSettings = []struct{ name, value string }{
	{"max_threads", "2"},
	{"max_memory_usage", "1073741824"},
	{"priority", "10"},
}

// setupCollection runs the load probe (not in dry-run, which may only send
// metadata queries), decides the starting mode and returns the watchdog the
// collectors run under.
func setupCollection(client *pkg.ClickHouseClient, requested collectmode.Mode, budget time.Duration, dryRun bool) (collectmode.Decision, *collectmode.Watchdog) {
	var d collectmode.Decision
	if dryRun {
		d = collectmode.Decide(requested, collectmode.Signals{}, nil)
	} else {
		s, err := collectmode.Probe(client)
		d = collectmode.Decide(requested, s, err)
	}
	if requested == collectmode.Full {
		// -collect full is a promise: no switch to essential, only the
		// budget if one was given.
		return d, collectmode.NewWatchdog(budget).Locked()
	}
	return d, collectmode.NewWatchdog(budget).WithTripBudget(collectmode.EssentialBudget)
}

// applyEssential makes the rest of the run gentle: a short per-query
// timeout (unless the operator chose one) and the gentle settings. Each
// setting is checked with SELECT 1, because a settings profile with a
// constraint on it would otherwise make every later query fail; a refused
// setting is left out and reported.
func applyEssential(client *pkg.ClickHouseClient, timeoutExplicit bool) []string {
	var notes []string
	client.SetClientMargin(5 * time.Second)
	if !timeoutExplicit {
		client.SetQueryTimeout(collectmode.EssentialQueryTimeout)
		notes = append(notes, fmt.Sprintf("query timeout %v", collectmode.EssentialQueryTimeout))
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
		notes = append(notes, "refused by the settings profile, left out: "+strings.Join(refused, ", "))
	}
	return notes
}

// collectKind is "essential" if the run was essential at any point (chosen
// up front or switched mid-run), else "full".
func collectKind(w *collectmode.Watchdog) string {
	if tripped, _ := w.Tripped(); tripped {
		return string(collectmode.Essential)
	}
	return string(collectmode.Full)
}

// skippedCollectors lists the collectors the run did not execute.
func skippedCollectors(rec *runlog.Recorder) []string {
	var out []string
	for _, e := range rec.Entries() {
		if e.Stage == "collector" && e.Status == "skipped" {
			out = append(out, strings.TrimSuffix(e.Name, ".sql"))
		}
	}
	return out
}
