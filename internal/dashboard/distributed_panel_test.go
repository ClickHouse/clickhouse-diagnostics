package dashboard

import (
	"testing"

	"clickhouse-diagnostic/internal/bundle"
)

// The hours come from metric_log_7_days (24.1+ rung); a server whose file
// has no Distributed columns, or none of them ever non-zero, shows nothing.
func TestDistributedHourly(t *testing.T) {
	if got := distributedHourly([]bundle.Row{{"time": "2026-10-29 10:00:00", "avg_merge_pool_tasks": "1"}}); len(got) != 0 {
		t.Fatalf("root rung (no Distributed columns): %v", got)
	}
	idle := []bundle.Row{{"time": "2026-10-29 10:00:00", "max_distributed_files": "0", "distributed_send_failures": "0"}}
	if got := distributedHourly(idle); len(got) != 0 {
		t.Fatalf("never non-zero: %v", got)
	}
	ml := []bundle.Row{
		{"time": "2026-10-29 11:00:00", "max_distributed_files": "1", "max_distributed_bytes": "4354", "max_broken_distributed_files": "1",
			"distributed_send_failures": "1", "distributed_delayed_inserts": "0", "distributed_rejected_inserts": "0"},
		{"time": "2026-10-29 10:00:00", "max_distributed_files": "3", "max_distributed_bytes": "13062", "max_broken_distributed_files": "0",
			"distributed_send_failures": "8", "distributed_delayed_inserts": "2", "distributed_rejected_inserts": "5"},
	}
	got := distributedHourly(ml)
	if len(got) != 2 || got[0]["time"] != "2026-10-29 10:00:00" || got[0]["files"] != int64(3) || got[0]["bytes"] != int64(13062) ||
		got[0]["send_failures"] != int64(8) || got[0]["delayed"] != int64(2) || got[0]["rejected"] != int64(5) || got[1]["broken_files"] != int64(1) {
		t.Fatalf("hours: %v", got)
	}
}

// The queue table lists every queue that is not idle, worst first, with the
// alert's state and the two the alert does not fire on (recovered, waiting).
func TestDistributionQueuePanel(t *testing.T) {
	dq := []bundle.Row{
		{"database": "db", "table": "idle", "error_count": "0", "data_files": "0"},
		{"database": "db", "table": "recovered", "error_count": "8", "data_files": "0", "last_exception_time": "2026-10-29 11:00:00"},
		{"database": "db", "table": "waiting", "data_files": "2", "data_compressed_bytes": "100"},
		{"database": "db", "table": "broken", "broken_data_files": "1", "broken_data_compressed_bytes": "26"},
		{"database": "db", "table": "failing", "error_count": "7", "data_files": "3", "data_compressed_bytes": "13062",
			"last_exception_code": "279", "last_exception": "Code: 279. All connection tries failed"},
		{"database": "db", "table": "gov_failing", "has_exception": 1, "last_exception_code": "16", "data_files": "1"},
		{"database": "db", "table": "underflow", "data_files": "18446744073709551615", "data_compressed_bytes": "18446744073709551615"},
	}
	got := distributionQueuePanel(dq)
	var order []string
	for _, r := range got {
		order = append(order, r["table"].(string)+":"+r["state"].(string))
	}
	want := "failing:failing gov_failing:failing broken:broken underflow:counter underflow waiting:waiting recovered:recovered"
	if s := join(order); s != want {
		t.Fatalf("got %q, want %q", s, want)
	}
	if got[0]["pending"] != "12.76 KiB" || got[0]["last_exception_code"] != int64(279) {
		t.Fatalf("failing row: %v", got[0])
	}
}

func join(xs []string) string {
	s := ""
	for i, x := range xs {
		if i > 0 {
			s += " "
		}
		s += x
	}
	return s
}
