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
		r := map[string]any{"database": "db", "table": table, "data_path": "/var/lib/clickhouse/store/x/shard2_all_replicas/", "is_blocked": 0,
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
		"system.disks": {
			{"name": "default", "type": "Local", "path": "/var/lib/clickhouse/", "free_bytes": "200000000000", "total_bytes": "4000000000000"},
			// A small unrelated disk: it holds no queue and must not be blamed.
			{"name": "small", "type": "Local", "path": "/mnt/small/", "free_bytes": "1000000000", "total_bytes": "2000000000"},
		},
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
		"waiting to be sent on disk `default`: 2.73 TiB", "186.26 GiB free",
		"12 insert(s) refused with 574",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pre-pass lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "disk `small`") {
		t.Errorf("an unrelated disk is blamed for the queues' bytes:\n%s", out)
	}
	if strings.Contains(out, "flushing_dist") || strings.Contains(out, "recovered_dist (") {
		t.Errorf("a flushing or recovered queue is reported as a problem:\n%s", out)
	}

	// A small queue on a roomy disk, beside an unrelated nearly full disk:
	// no disk-pressure warning (review: the fullest disk was blamed).
	out = runPrepassAZL(t, map[string][]map[string]any{
		"system.distribution_queue": {q("d", "data_files", "5", "data_compressed_bytes", "1000000000")},
		"system.disks": {
			{"name": "default", "type": "Local", "path": "/var/lib/clickhouse/", "free_bytes": "2000000000000"},
			{"name": "small", "type": "Local", "path": "/mnt/small/", "free_bytes": "1000000000"},
		},
	})
	if strings.Contains(out, "waiting to be sent") {
		t.Errorf("disk pressure from an unrelated disk:\n%s", out)
	}
	// Gov: data_path and disk paths are hashed, so the disk is unknown — the
	// comparison is information, never a warning.
	gq := q("g", "data_path", "AB12", "data_files", "5", "data_compressed_bytes", "900000000")
	out = runPrepassAZL(t, map[string][]map[string]any{
		"system.distribution_queue": {gq},
		"system.disks":              {{"name": "C3D4", "type": "Local", "path": "E5F6", "free_bytes": "1000000000"}},
	})
	if !strings.Contains(out, "the disk that holds them is not known") || strings.Contains(out, "**warning** (distributed) Distributed queues waiting") {
		t.Errorf("gov disk comparison should be info:\n%s", out)
	}

	// Review: the worst failing queue is the one with a real backlog, and a
	// failing queue with underflowed counters is not reported as 16 EiB.
	out = runPrepassAZL(t, map[string][]map[string]any{"system.distribution_queue": {
		q("bad_dist", "data_files", "18446744073709551615", "data_compressed_bytes", "18446744073709551615", "last_exception", "Code: 107.", "last_exception_code", 107),
		q("real_dist", "data_files", "3", "data_compressed_bytes", "13062", "last_exception", "Code: 279.", "last_exception_code", 279),
	}})
	if !strings.Contains(out, "worst db.real_dist") || strings.Contains(out, "EiB") || strings.Contains(out, "PiB") {
		t.Errorf("underflowed counters drive the failing report:\n%s", out)
	}

	// No file (below 23.3, or cloud): nothing is said.
	if out := runPrepassAZL(t, map[string][]map[string]any{"system.errors": {}}); strings.Contains(out, "HC-3.26") {
		t.Errorf("HC-3.26 without the file:\n%s", out)
	}
}
