package main

import (
	"strings"
	"testing"
)

// HC-3.24 / HC-3.25: the per-host replication-delay series and the schedule
// pool's task types.
func TestInspectBundle_ReplicationDelayAndSchedulePool(t *testing.T) {
	var rep []map[string]any
	for _, h := range []string{"ch-01", "ch-02", "ch-03"} {
		for hr := 0; hr < 3; hr++ {
			d := 2
			if h == "ch-02" && hr > 0 {
				d = 900 * hr // 900 s, then 1800 s
			}
			rep = append(rep, map[string]any{"hostname": h, "time": "2026-11-10 0" + string(rune('0'+hr)) + ":00:00",
				"max_absolute_delay_s": d, "max_queue_size": 10, "max_inserts_in_queue": 8, "samples": 3600})
		}
	}
	sp := []map[string]any{
		{"hostname": "ch-02", "time": "2026-11-10 01:00:00", "task_type": "ReplicatedMergeTreeRestartingThread", "runs": 4000, "total_ms": 900000, "errors": 12, "example_exception": "Code: 999. Coordination::Exception: Session expired"},
		{"hostname": "ch-01", "time": "2026-11-10 01:00:00", "task_type": "CleanupThread", "runs": 100, "total_ms": 3000, "errors": 0},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{
		"system.asynchronous_metric_log_replication_3_days": rep,
		"system.background_schedule_pool_log_by_type_1_day": sp,
	})
	for _, want := range []string{
		"replication delay reached 1800 s on ch-02", "2 hour(s) at ≥ 5 min", "HC-3.24",
		"only ch-02 lagged", "schedule pool, most time by task type — ch-02: ReplicatedMergeTreeRestartingThread 900 s",
		"failed 12 time(s)", "Session expired", "HC-3.25",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pre-pass lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "replication delay reached 2 s") {
		t.Errorf("a 2 s delay is reported:\n%s", out)
	}
}

// Review: peers whose delay is always 0 still prove a lag was host-local,
// and every host's leading schedule-pool task types are reported.
func TestInspectBundle_LagReviewFixes(t *testing.T) {
	var rep []map[string]any
	for _, h := range []string{"ch-01", "ch-02", "ch-03"} {
		d := 0
		if h == "ch-02" {
			d = 900
		}
		rep = append(rep, map[string]any{"hostname": h, "time": "2026-11-10 01:00:00", "max_absolute_delay_s": d, "samples": 3600})
	}
	var sp []map[string]any
	for i, h := range []string{"ch-01", "ch-02", "ch-03", "ch-04", "ch-05"} {
		sp = append(sp, map[string]any{"hostname": h, "time": "2026-11-10 01:00:00", "task_type": "CleanupThread", "total_ms": 1000 * (i + 1), "errors": 0})
	}
	for _, ty := range []string{"A", "B", "C", "D"} {
		sp = append(sp, map[string]any{"hostname": "ch-01", "time": "2026-11-10 01:00:00", "task_type": ty, "total_ms": 900000, "errors": 0})
	}
	out := runPrepassAZL(t, map[string][]map[string]any{
		"system.asynchronous_metric_log_replication_3_days": rep,
		"system.background_schedule_pool_log_by_type_1_day": sp,
	})
	if !strings.Contains(out, "only ch-02 lagged") {
		t.Errorf("zero-delay peers do not count:\n%s", out)
	}
	for _, h := range []string{"ch-02: CleanupThread", "ch-05: CleanupThread"} {
		if !strings.Contains(out, h) {
			t.Errorf("%q hidden by a busier host:\n%s", h, out)
		}
	}
}

// A peer with no rows in the lagging hours proves nothing: no host-local
// warning then, while the individual delay warning stays.
func TestInspectBundle_HostLocalNeedsPeerCoverage(t *testing.T) {
	rep := []map[string]any{
		{"hostname": "ch-02", "time": "2026-11-10 10:00:00", "max_absolute_delay_s": 900},
		{"hostname": "ch-01", "time": "2026-11-10 11:00:00", "max_absolute_delay_s": 0},
		{"hostname": "ch-03", "time": "2026-11-10 11:00:00", "max_absolute_delay_s": 0},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.asynchronous_metric_log_replication_3_days": rep})
	if strings.Contains(out, "only ch-02 lagged") {
		t.Errorf("host-local claimed without peer telemetry in the lagging hour:\n%s", out)
	}
	if !strings.Contains(out, "replication delay reached 900 s on ch-02") {
		t.Errorf("the individual delay warning is lost:\n%s", out)
	}
}
