package dashboard

import (
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/hostinfo"
)

func findCheck(t *testing.T, rows []map[string]interface{}, setting string) map[string]interface{} {
	t.Helper()
	for _, r := range rows {
		if r["setting"] == setting {
			return r
		}
	}
	t.Fatalf("no check for %q", setting)
	return nil
}

// The warning bar is "ClickHouse warns about it too". These two are real checks
// in programs/server/Server.cpp::sanityChecks.
func TestHostChecks_FlagsWhatClickHouseFlags(t *testing.T) {
	rep := &hostinfo.Report{Tunables: hostinfo.Tunables{
		TransparentHugepages: "always",
		OvercommitMemory:     "2",
	}}
	rows := hostChecks(rep)
	if got := findCheck(t, rows, "transparent_hugepages")["status"]; got != "warning" {
		t.Errorf("THP=always must warn (ClickHouse does at startup), got %v", got)
	}
	if got := findCheck(t, rows, "vm.overcommit_memory")["status"]; got != "warning" {
		t.Errorf("overcommit_memory=2 must warn (ClickHouse does at startup), got %v", got)
	}

	ok := hostChecks(&hostinfo.Report{Tunables: hostinfo.Tunables{
		TransparentHugepages: "madvise",
		OvercommitMemory:     "1",
	}})
	if got := findCheck(t, ok, "transparent_hugepages")["status"]; got != "ok" {
		t.Errorf("THP=madvise should be ok, got %v", got)
	}
	if got := findCheck(t, ok, "vm.overcommit_memory")["status"]; got != "ok" {
		t.Errorf("overcommit_memory=1 should be ok, got %v", got)
	}
}

// vm.swappiness is common database tuning folklore, but ClickHouse neither
// checks it at startup nor documents a target — so the dashboard reports it
// and does not grade it. Flagging it would put a recommendation in ClickHouse's
// mouth that ClickHouse does not make.
func TestHostChecks_DoesNotInventRecommendations(t *testing.T) {
	rows := hostChecks(&hostinfo.Report{Tunables: hostinfo.Tunables{Swappiness: "60"}})
	sw := findCheck(t, rows, "vm.swappiness")
	if sw["status"] == "warning" {
		t.Error("swappiness must not be graded: ClickHouse has no documented target for it")
	}
	if sw["value"] != "60" {
		t.Errorf("swappiness value should still be reported, got %v", sw["value"])
	}
}

// ClickHouse warns when available memory at startup is under 2 GiB.
func TestHostChecks_LowAvailableMemory(t *testing.T) {
	low := hostChecks(&hostinfo.Report{Memory: hostinfo.MemoryInfo{Available: true, AvailableBytes: 1 << 30}})
	if got := findCheck(t, low, "available memory")["status"]; got != "warning" {
		t.Errorf("1 GiB available must warn, got %v", got)
	}
	high := hostChecks(&hostinfo.Report{Memory: hostinfo.MemoryInfo{Available: true, AvailableBytes: 8 << 30}})
	if got := findCheck(t, high, "available memory")["status"]; got != "ok" {
		t.Errorf("8 GiB available should be ok, got %v", got)
	}
}

// An unreadable tunable must never render as a healthy one.
func TestHostChecks_UnreadableIsNotHealthy(t *testing.T) {
	rows := hostChecks(&hostinfo.Report{})
	c := findCheck(t, rows, "transparent_hugepages")
	if c["status"] == "ok" {
		t.Error("an unread tunable must not report as ok")
	}
	if c["value"] != "(unreadable)" {
		t.Errorf("unread value should say so, got %v", c["value"])
	}
}

// Both rlimits unreadable used to join to the literal "/" — non-empty, so it
// sailed past the (unreadable) guard and rendered as a healthy row.
func TestHostChecks_UnreadableRlimitsAreNotSlash(t *testing.T) {
	rows := hostChecks(&hostinfo.Report{})
	c := findCheck(t, rows, "open files (soft/hard)")
	if c["value"] == "/" {
		t.Fatal(`two unread rlimits rendered as "/" instead of (unreadable)`)
	}
	if c["value"] != "(unreadable)" {
		t.Errorf("unread rlimits should say (unreadable), got %v", c["value"])
	}
}

func TestHostChecks_NilReport(t *testing.T) {
	if rows := hostChecks(nil); rows != nil {
		t.Errorf("nil report should yield no checks, got %d", len(rows))
	}
}

// The panel is hidden unless host_info is present, so a skipped host-info run
// does not leave an empty shell in Overview.
func TestTemplate_HostPanelIsConditional(t *testing.T) {
	for _, want := range []string{`id="host-block"`, "const hi=DATA.host_info;", "if(!hi)return;",
		"tbl-host-os", "tbl-host-tunables", "tbl-host-procs", "host-notes"} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("host panel missing %q", want)
		}
	}
}
