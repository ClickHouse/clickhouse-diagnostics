package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	"clickhouse-diagnostic/internal/alert"
)

// TestBuildHTML_AlertsPreview renders an alerts page covering every collapse
// case the panel has to handle — a message with a stack trace, more instances
// than the inline cap, a short message that needs no disclosure at all, a rule
// whose own query failed, and a rule that was not applicable — so the
// interaction can be reviewed by eye rather than by reading the template.
// `make dashboard-preview` writes it to bin/alerts_preview.html.
func TestBuildHTML_AlertsPreview(t *testing.T) {
	dir := os.Getenv("DASHBOARD_PREVIEW_DIR")
	if dir == "" {
		t.Skip("set DASHBOARD_PREVIEW_DIR to render")
	}
	trace := "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION) (version 26.2.1.390 (official build)), " +
		"Stack trace (when copying this message, always include the lines below):\n\n" +
		"0. ./ci/tmp/build/./src/Common/Exception.cpp:141:1: DB::Exception::Exception(DB::Exception::MessageMasked&&, int, bool) @ 0x000000001373469f\n" +
		"1. ./src/Common/ZooKeeper/ZooKeeperImpl.cpp:1071: Coordination::ZooKeeper::pushRequest(Coordination::ZooKeeper::RequestInfo&&) @ 0x000000001a2b3c4d\n" +
		"2. ./src/Common/ZooKeeper/ZooKeeper.cpp:412: zkutil::ZooKeeper::multiImpl(std::vector<Coordination::RequestPtr> const&, ...) @ 0x000000001a2b9f10\n" +
		"3. ./src/Storages/MergeTree/ReplicatedMergeTreeQueue.cpp:1188: DB::ReplicatedMergeTreeQueue::processEntry(...) @ 0x000000001c0d4a22\n" +
		"4. ./src/Storages/StorageReplicatedMergeTree.cpp:3702: DB::StorageReplicatedMergeTree::processQueueEntry(...) @ 0x000000001bf51188\n" +
		"5. ./src/Storages/MergeTree/MergeTreeBackgroundExecutor.cpp:288: DB::MergeTreeBackgroundExecutor<...>::threadFunction() @ 0x000000001c113d90\n" +
		"6. ./base/poco/Foundation/src/ThreadPool.cpp:205:14: Poco::PooledThread::run() @ 0x000000002334b505\n" +
		"7. ./base/poco/Foundation/src/Thread_POSIX.cpp:335:5: Poco::ThreadImpl::runnableEntry(void*) @ 0x0000000023348f01\n"

	queue := []map[string]interface{}{}
	for i, tbl := range []string{"events_local", "device_log", "sensor_raw", "job_history", "audit_trail", "shift_report", "line_state"} {
		queue = append(queue, map[string]interface{}{
			"database": "demo_app", "table": tbl, "replica_name": "r-0" + string(rune('1'+i)),
			"type": "GET_PART", "num_tries": 40 + i*17, "last_exception": trace,
		})
	}
	keeper := []map[string]interface{}{}
	for h := 13; h <= 20; h++ {
		keeper = append(keeper, map[string]interface{}{
			"hour": "2026-09-10 " + string(rune('0'+h/10)) + string(rune('0'+h%10)) + ":00:00", "hostname": "node-07",
			"hw_exceptions": 10647545 - h*1000, "transactions": 11033023, "usual_transactions": 28010220, "pct_of_usual": 39 - h,
		})
	}

	data := map[string]interface{}{
		"generated_at": "2026-09-25 10:00:00 UTC", "mode": "onprem", "version": "26.2.1.390",
		"uptime": "1 hours 51 minutes", "total_databases": 410, "total_tables": 33882, "active_parts": 50000, "total_size": "28.40 TiB",
		"alerts": []alert.Result{
			// 1. long message (stack trace) AND more instances than the cap
			{Name: "replication_queue_errors", Title: "Replication queue entries have exceptions", Severity: "critical", File: "replication_queue_errors.yaml",
				Description: "A replication queue entry has a non-empty last_exception: the replica tried to execute it and the server refused.\n\nCheck: the exception text and num_tries — a high count with the same error is a stuck entry, not a slow one. Then system.replicas for the same table, and the source replica named in the message.",
				Message:     "{database}.{table} (replica {replica_name}): {type} failed after {num_tries} tries — {last_exception}",
				Rows:        queue},
			// 2. short messages, many instances → only the row cap applies
			{Name: "keeper_health", Title: "Keeper unavailable: session loss storm while Keeper traffic collapsed", Severity: "critical", File: "keeper_health.yaml",
				Description: "The two-signal Keeper health test over the last 7 days, per hour.\n\nCheck next: system.zookeeper_connection (session age), part_log errors in the same hours, then the Keeper nodes themselves.",
				Message:     "hour starting {hour} on {hostname}: {hw_exceptions} Keeper hardware exceptions, {transactions} Keeper transactions = {pct_of_usual}% of usual ({usual_transactions}/h median) — Keeper effectively unavailable to this server",
				Rows:        keeper},
			// 3. the control case: one short instance, single-paragraph description → no toggles at all
			{Name: "too_many_parts", Title: "Table partition approaching too-many-parts limit", Severity: "warning", File: "too_many_parts.yaml",
				Description: "A table partition has more than 300 active parts.",
				Message:     "{database}.{table} partition '{partition_id}' has {parts_count} active parts — inserts are delayed from 1000 parts (parts_to_delay_insert) and rejected with code 252 TOO_MANY_PARTS at 3000 (parts_to_throw_insert)",
				Rows:        []map[string]interface{}{{"database": "demo_app", "table": "events_summary", "partition_id": "202609", "parts_count": 3469}}},
			// 4. a rule whose own query failed, with a trace in the error text
			{Name: "detached_parts_exist", Title: "Detached parts present", Severity: "info", File: "detached_parts_exist.yaml",
				Description: "Parts exist in the detached/ folder.",
				Error:       "error executing query: non-OK status: 500, body: " + trace},
			// 5. not applicable here
			{Name: "crash_log_entries", Title: "Server crash detected", Severity: "critical", File: "crash_log_entries.yaml", Skipped: true, Reason: "table not present"},
		},
	}
	if err := os.WriteFile(filepath.Join(dir, "alerts_preview.html"), []byte(buildHTML(data)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("written: %s", filepath.Join(dir, "alerts_preview.html"))
}
