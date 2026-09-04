package main

import (
	"strings"
	"testing"
)

// Gov mode must never produce dashboard.html. The dashboard's panels select
// raw identifiers that queries.gov/*.sql exists to hash, and it ships in the
// same archive — so this is a disclosure rule, not a preference.
func TestDashboardDecision_GovNeverGenerates(t *testing.T) {
	for _, skip := range []bool{false, true} {
		generate, reason := dashboardDecision(skip, "gov")
		if generate {
			t.Fatalf("gov mode must not generate a dashboard (skipDashboard=%v)", skip)
		}
		if reason == "" {
			t.Errorf("gov refusal must explain itself (skipDashboard=%v)", skip)
		}
	}
	// With no --skip-dashboard, the reason given must be the gov one, so the
	// operator learns it was policy rather than a flag or a failure.
	_, reason := dashboardDecision(false, "gov")
	if !strings.Contains(reason, "gov mode") {
		t.Errorf("gov refusal should name gov mode, got %q", reason)
	}
}

// The gov refusal must not depend on the caller having normalised the mode.
// getUserInput lowercases before this is reached today, so these forms are
// unreachable from main() — that is the point: the guard is here so a future
// call site cannot reintroduce the leak, and "GOV" already slipped past a bare
// == "gov" comparison once in this file.
func TestDashboardDecision_GovIsNormalised(t *testing.T) {
	for _, mode := range []string{"GOV", "Gov", " gov", "gov ", "\tGOV\n"} {
		if generate, _ := dashboardDecision(false, mode); generate {
			t.Errorf("mode %q generated a dashboard — gov must be refused whatever its casing or padding", mode)
		}
	}
}

func TestDashboardDecision_NonGov(t *testing.T) {
	for _, mode := range []string{"onprem", "cloud"} {
		if generate, reason := dashboardDecision(false, mode); !generate {
			t.Errorf("mode %q should generate a dashboard, refused with %q", mode, reason)
		}
		// An explicit --skip-dashboard is honoured and reported as the flag,
		// not as a policy refusal.
		generate, reason := dashboardDecision(true, mode)
		if generate {
			t.Errorf("mode %q ignored --skip-dashboard", mode)
		}
		if !strings.Contains(reason, "--skip-dashboard") {
			t.Errorf("mode %q should attribute the skip to the flag, got %q", mode, reason)
		}
	}
}
