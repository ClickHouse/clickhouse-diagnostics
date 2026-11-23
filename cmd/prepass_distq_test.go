package main

import (
	"strings"
	"testing"
)

// HC-3.26: the async Distributed insert queues. Failing, broken and stopped
// are warnings with the fix the error code points at; a queue that only
// counts old failures is information (error_count is never reset), files
// waiting alone say nothing, and counters near 2^64 are a counter bug.
func TestInspectBundle_DistributionQueue(t *testing.T) {
	q := func(table string, kv ...any) map[string]any {
		r := map[string]any{"database": "db", "table": table, "data_path": "/store/x/shard2_all_replicas/", "is_blocked": 0,
			"error_count": "0", "data_files": "0", "data_compressed_bytes": "0", "broken_data_files": "0",
			"last_exception_code": 0, "last_exception": "", "last_exception_time": ""}
		for i := 0; i+1 < len(kv); i += 2 {
			r[kv[i].(string)] = kv[i+1]
		}
		return r
	}
	out := runPrepassAZL(t, map[string][]map[string]any{
		"system.distribution_queue": {
			q("events_dist", "error_count", "99000", "data_files", "4700000", "data_compressed_bytes", "3000000000000",
				"last_exception_code", 107, "last_exception", "Code: 107. DB::Exception: File not found", "last_exception_time", "2026-11-10 01:59:00"),
			q("broken_dist", "broken_data_files", "2", "broken_data_compressed_bytes", "4096"),
			q("stopped_dist", "is_blocked", 1, "data_files", "3", "data_compressed_bytes", "900"),
			q("recovered_dist", "error_count", "8", "last_exception_time", "2026-11-09 10:00:00"),
			q("flushing_dist", "data_files", "2", "data_compressed_bytes", "100"),
			q("bug_dist", "data_files", "18446744073709551615", "data_compressed_bytes", "18446744073709551615"),
		},
		"system.disks": {{"name": "default", "type": "Local", "free_bytes": "200000000000", "total_bytes": "4000000000000"}},
		"system.metric_log_7_days": {
			{"time": "2026-11-10 00:00:00", "max_distributed_files": "4000000", "max_distributed_bytes": "2900000000000", "distributed_send_failures": "700", "distributed_delayed_inserts": "0", "distributed_rejected_inserts": "12"},
			{"time": "2026-11-10 01:00:00", "max_distributed_files": "4700000", "max_distributed_bytes": "3000000000000", "distributed_send_failures": "720", "distributed_delayed_inserts": "0", "distributed_rejected_inserts": "0"},
		},
	})
	for _, want := range []string{
		"1 Distributed insert queue(s) failing", "db.events_dist", "FILE_DOESNT_EXIST", "4700000 file(s) waiting (2.73 TiB)", "P-69", "HC-3.26",
		"1 queue(s) with broken files", "db.broken_dist",
		"1 queue(s) stopped by hand", "SYSTEM STOP DISTRIBUTED SENDS",
		"1 queue(s) recovered", "never reset",
		"counter near 2^64", "db.bug_dist",
		"waiting to be sent: 2.73 TiB", "free on the fullest disk: 186.26 GiB",
		"12 insert(s) refused with 574",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pre-pass lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "flushing_dist") || strings.Contains(out, "recovered_dist (") {
		t.Errorf("a flushing or recovered queue is reported as a problem:\n%s", out)
	}

	// No file (below 23.3, or cloud): nothing is said.
	if out := runPrepassAZL(t, map[string][]map[string]any{"system.errors": {}}); strings.Contains(out, "HC-3.26") {
		t.Errorf("HC-3.26 without the file:\n%s", out)
	}
}
