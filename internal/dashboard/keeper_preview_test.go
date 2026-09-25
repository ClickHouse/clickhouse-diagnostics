package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/alert"
)

// keeperIncidentFixture is a dashboard payload shaped like a real Keeper
// outage on a self-hosted SharedMergeTree cluster, with every identifier
// replaced: 48 hours of Keeper counters in which the ensemble lost quorum at
// 13:00 on day two, sessions expired for eight hours, merges stopped, one
// insert-heavy table hit TOO_MANY_PARTS, and reads failed with
// FILE_DOESNT_EXIST after recovery. The numbers are the incident's; the names
// are not. It exists so the Keeper Health panel and the Keeper alerts can be
// looked at without a live outage:
//
//	DASHBOARD_PREVIEW_DIR=/tmp go test ./internal/dashboard -run TestBuildHTML_KeeperIncidentPreview
//
// writes /tmp/keeper_incident_preview.html (Chart.js from CDN, as always).
func keeperIncidentFixture() map[string]interface{} {
	// hour → (transactions, hardware exceptions), day one is a quiet baseline
	// with one blip at 09:00, day two is the outage.
	type h struct {
		t      string
		tx, hw int64
	}
	hours := []h{
		{"2026-09-09 00:00:00", 28301972, 0}, {"2026-09-09 01:00:00", 28293765, 0}, {"2026-09-09 02:00:00", 28231828, 0},
		{"2026-09-09 03:00:00", 28010220, 0}, {"2026-09-09 04:00:00", 28042643, 0}, {"2026-09-09 05:00:00", 27970065, 0},
		{"2026-09-09 06:00:00", 28132003, 0}, {"2026-09-09 07:00:00", 27964995, 0}, {"2026-09-09 08:00:00", 27995066, 0},
		{"2026-09-09 09:00:00", 28545548, 43477}, {"2026-09-09 10:00:00", 28032622, 0}, {"2026-09-09 11:00:00", 28980044, 0},
		{"2026-09-09 12:00:00", 29981645, 0}, {"2026-09-09 13:00:00", 26478292, 0}, {"2026-09-09 14:00:00", 28707456, 0},
		{"2026-09-09 15:00:00", 27559140, 0}, {"2026-09-09 16:00:00", 27624651, 0}, {"2026-09-09 17:00:00", 28681563, 0},
		{"2026-09-09 18:00:00", 27731815, 0}, {"2026-09-09 19:00:00", 28374936, 0}, {"2026-09-09 20:00:00", 28519447, 0},
		{"2026-09-09 21:00:00", 28473114, 0}, {"2026-09-09 22:00:00", 28599150, 0}, {"2026-09-09 23:00:00", 28665767, 0},
		{"2026-09-10 00:00:00", 28553295, 0}, {"2026-09-10 01:00:00", 28654757, 0}, {"2026-09-10 02:00:00", 27655990, 0},
		{"2026-09-10 03:00:00", 28537643, 0}, {"2026-09-10 04:00:00", 28246011, 0}, {"2026-09-10 05:00:00", 27294409, 0},
		{"2026-09-10 06:00:00", 27875935, 0}, {"2026-09-10 07:00:00", 27728495, 0}, {"2026-09-10 08:00:00", 27475104, 0},
		{"2026-09-10 09:00:00", 28132123, 0}, {"2026-09-10 10:00:00", 27485499, 0}, {"2026-09-10 11:00:00", 28394169, 0},
		{"2026-09-10 12:00:00", 27873172, 0},
		{"2026-09-10 13:00:00", 11033023, 10647545}, {"2026-09-10 14:00:00", 2448832, 25068351}, {"2026-09-10 15:00:00", 8841801, 8077157},
		{"2026-09-10 16:00:00", 3015349, 2463350}, {"2026-09-10 17:00:00", 1447504, 299556}, {"2026-09-10 18:00:00", 11367, 7808},
		{"2026-09-10 19:00:00", 10435, 10354}, {"2026-09-10 20:00:00", 59394, 12938},
		{"2026-09-10 21:00:00", 27710725, 0}, {"2026-09-10 22:00:00", 30546583, 0},
	}
	metric := make([]map[string]interface{}, 0, len(hours))
	for _, x := range hours {
		// mean latency ≈ 2 ms in quiet hours, rising with the exceptions
		latency := int64(2000)
		if x.hw > 0 {
			latency = 2000 + x.hw/2000
		}
		sess := int64(1)
		if x.hw > 1000000 {
			sess = 0
		}
		metric = append(metric, map[string]interface{}{
			"time": x.t, "transactions": x.tx, "hw_exceptions": x.hw, "user_exceptions": x.tx / 900,
			"wait_us": x.tx * latency, "sessions_min": sess, "sessions_max": 1,
		})
	}
	errRow := func(t, code string, n int64) map[string]interface{} {
		return map[string]interface{}{"time": t, "code_name": code, "count": n}
	}
	errors := []map[string]interface{}{
		errRow("2026-09-09 09:00:00", "KEEPER_EXCEPTION", 2),
		errRow("2026-09-10 13:00:00", "KEEPER_EXCEPTION", 133), errRow("2026-09-10 13:00:00", "UNKNOWN_STATUS_OF_INSERT", 9), errRow("2026-09-10 13:00:00", "TOO_MANY_PARTS", 210),
		errRow("2026-09-10 14:00:00", "KEEPER_EXCEPTION", 732), errRow("2026-09-10 14:00:00", "UNKNOWN_STATUS_OF_INSERT", 47), errRow("2026-09-10 14:00:00", "TOO_MANY_PARTS", 285),
		errRow("2026-09-10 15:00:00", "KEEPER_EXCEPTION", 1959), errRow("2026-09-10 15:00:00", "DATABASE_REPLICATION_FAILED", 4), errRow("2026-09-10 15:00:00", "TOO_MANY_PARTS", 437), errRow("2026-09-10 15:00:00", "UNKNOWN_STATUS_OF_INSERT", 34),
		errRow("2026-09-10 16:00:00", "KEEPER_EXCEPTION", 465), errRow("2026-09-10 16:00:00", "DATABASE_REPLICATION_FAILED", 6), errRow("2026-09-10 16:00:00", "TOO_MANY_PARTS", 4204),
		errRow("2026-09-10 17:00:00", "KEEPER_EXCEPTION", 225), errRow("2026-09-10 17:00:00", "TOO_MANY_PARTS", 7761),
		errRow("2026-09-10 18:00:00", "KEEPER_EXCEPTION", 11), errRow("2026-09-10 18:00:00", "TOO_MANY_PARTS", 128),
		errRow("2026-09-10 19:00:00", "KEEPER_EXCEPTION", 42),
		errRow("2026-09-10 20:00:00", "KEEPER_EXCEPTION", 39), errRow("2026-09-10 20:00:00", "TABLE_IS_READ_ONLY", 12),
	}
	keeperHealthRows := []map[string]interface{}{}
	for _, x := range hours[37:45] {
		keeperHealthRows = append(keeperHealthRows, map[string]interface{}{
			"hour": x.t, "hw_exceptions": x.hw, "transactions": x.tx, "usual_transactions": 28010220,
			"pct_of_usual": x.tx * 100 / 28010220,
		})
	}
	// A real ClickHouse exception, anonymised: the frames are public source
	// paths, the table and replica are invented. This is what made the alert
	// panel unreadable before the messages collapsed — 1764 characters over
	// 15 lines per row, of which the first 216 are the error.
	trace := "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION) (version 26.2.1.390 (official build)), " +
		"Stack trace (when copying this message, always include the lines below):\n\n" +
		"0. ./ci/tmp/build/./src/Common/Exception.cpp:141:1: DB::Exception::Exception(DB::Exception::MessageMasked&&, int, bool) @ 0x000000001373469f\n" +
		"1. ./src/Common/ZooKeeper/ZooKeeperImpl.cpp:1071: Coordination::ZooKeeper::pushRequest(Coordination::ZooKeeper::RequestInfo&&) @ 0x000000001a2b3c4d\n" +
		"2. ./src/Common/ZooKeeper/ZooKeeper.cpp:412: zkutil::ZooKeeper::multiImpl(...) @ 0x000000001a2b9f10\n" +
		"3. ./src/Storages/MergeTree/ReplicatedMergeTreeQueue.cpp:1188: DB::ReplicatedMergeTreeQueue::processEntry(...) @ 0x000000001c0d4a22\n" +
		"4. ./src/Storages/StorageReplicatedMergeTree.cpp:3702: DB::StorageReplicatedMergeTree::processQueueEntry(...) @ 0x000000001bf51188\n" +
		"5. ./src/Storages/MergeTree/MergeTreeBackgroundExecutor.cpp:288: DB::MergeTreeBackgroundExecutor<...>::threadFunction() @ 0x000000001c113d90\n" +
		"6. ./base/poco/Foundation/src/ThreadPool.cpp:205:14: Poco::PooledThread::run() @ 0x000000002334b505\n" +
		"7. ./base/poco/Foundation/src/Thread_POSIX.cpp:335:5: Poco::ThreadImpl::runnableEntry(void*) @ 0x0000000023348f01\n"
	queueRows := []map[string]interface{}{}
	for i, tbl := range []string{"events_local", "device_log", "sensor_raw", "job_history", "audit_trail", "shift_report", "line_state"} {
		queueRows = append(queueRows, map[string]interface{}{
			"database": "demo_app", "table": tbl, "replica_name": "r-0" + string(rune('1'+i)),
			"type": "GET_PART", "create_time": "2026-09-10 15:0" + string(rune('1'+i)) + ":22",
			"num_tries": 40 + i*17, "last_exception": trace,
		})
	}

	alerts := []alert.Result{
		{Name: "replication_queue_errors", Title: "Replication queue entries have exceptions", Severity: "critical", File: "replication_queue_errors.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "{database}.{table} (replica {replica_name}): {type} failed after {num_tries} tries — {last_exception}",
			Rows:    queueRows},
		{Name: "keeper_health", Title: "Keeper unavailable: session loss storm while Keeper traffic collapsed", Severity: "critical", File: "keeper_health.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "hour starting {hour}: {hw_exceptions} Keeper hardware exceptions, {transactions} Keeper transactions = {pct_of_usual}% of usual ({usual_transactions}/h median) — Keeper effectively unavailable to this server",
			Rows:    keeperHealthRows},
		{Name: "keeper_connection_blips", Title: "Keeper connection losses in an hour while traffic stayed normal", Severity: "warning", File: "keeper_connection_blips.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "hour starting {hour}: {hw_exceptions} Keeper hardware exceptions while traffic stayed at {pct_of_usual}% of usual ({transactions} transactions) — session lost and re-established",
			Rows:    []map[string]interface{}{{"hour": "2026-09-09 09:00:00", "hw_exceptions": 43477, "transactions": 28545548, "pct_of_usual": 102}}},
		{Name: "merges_stalled", Title: "Parts were inserted but no merge completed for a whole hour", Severity: "warning", File: "merges_stalled.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "hour starting {hour}: {new_parts} parts inserted, 0 merges completed ({failed_merges} merges failed) — merges were not running",
			Rows: []map[string]interface{}{
				{"hour": "2026-09-10 17:00:00", "new_parts": 4985, "merges": 0, "failed_merges": 0},
				{"hour": "2026-09-10 18:00:00", "new_parts": 4406, "merges": 0, "failed_merges": 0},
				{"hour": "2026-09-10 19:00:00", "new_parts": 4385, "merges": 0, "failed_merges": 0},
				{"hour": "2026-09-10 20:00:00", "new_parts": 4575, "merges": 0, "failed_merges": 0}}},
		{Name: "background_operation_failures", Title: "Background merges / fetches / mutations failing repeatedly", Severity: "warning", File: "background_operation_failures.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "{failures} {event_type} operations failed with code {error} in the hour starting {hour} — example: {example_message}",
			Rows: []map[string]interface{}{
				{"hour": "2026-09-10 15:00:00", "event_type": "MergeParts", "error": 999, "failures": 387, "example_message": "Code: 999. Coordination::Exception: Coordination error: Session expired. (KEEPER_EXCEPTION)"},
				{"hour": "2026-09-10 16:00:00", "event_type": "MergeParts", "error": 999, "failures": 155, "example_message": "Code: 999. Coordination::Exception: Coordination error: Session expired. (KEEPER_EXCEPTION)"},
				{"hour": "2026-09-10 14:00:00", "event_type": "MergeParts", "error": 999, "failures": 118, "example_message": "Code: 999. Coordination::Exception: Coordination error: Out of Memory, path /clickhouse/sessions/zookeeper/<session>. (KEEPER_EXCEPTION)"}}},
		{Name: "high_exception_rate", Title: "High query exception rate for one error code in an hour", Severity: "warning", File: "high_exception_rate.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "Exception code {exception_code} fired {error_count} times in the hour starting {hour} — example: {example_message}",
			Rows: []map[string]interface{}{
				{"hour": "2026-09-10 17:00:00", "exception_code": 252, "error_count": 7761, "example_message": "Code: 252. DB::Exception: Too many parts (3469 with average size of 1.71 MiB) in table 'demo_app.events_summary'. Merges are processing significantly slower than inserts. (TOO_MANY_PARTS)"},
				{"hour": "2026-09-10 15:00:00", "exception_code": 999, "error_count": 1959, "example_message": "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION)"},
				{"hour": "2026-09-10 16:00:00", "exception_code": 57, "error_count": 1095, "example_message": "Code: 57. DB::Exception: Mapping for table with UUID=<uuid> already exists. It happened due to UUID collision, most likely because some not random UUIDs were manually specified in CREATE queries. (TABLE_ALREADY_EXISTS)"},
				{"hour": "2026-09-10 20:00:00", "exception_code": 107, "error_count": 365, "example_message": "Code: 107. DB::Exception: File data/<uuid>/202609_12149_12162_3/primary.idx doesn't exist: while parsing metadata for data/<uuid>/202609_12149_12162_3/primary.idx. (FILE_DOESNT_EXIST)"}}},
		{Name: "keeper_exception_spike", Title: "Keeper/ZooKeeper exceptions spiking (code 999)", Severity: "warning", File: "keeper_exception_spike.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "Keeper exception (code 999) fired {error_count} times in the hour starting {hour} — example: {example_message}",
			Rows: []map[string]interface{}{
				{"hour": "2026-09-10 13:00:00", "error_count": 133, "example_message": "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION)"},
				{"hour": "2026-09-10 14:00:00", "error_count": 732, "example_message": "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION)"},
				{"hour": "2026-09-10 15:00:00", "error_count": 1959, "example_message": "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION)"},
				{"hour": "2026-09-10 16:00:00", "error_count": 465, "example_message": "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION)"},
				{"hour": "2026-09-10 17:00:00", "error_count": 225, "example_message": "Code: 999. Coordination::Exception: Session expired. (KEEPER_EXCEPTION)"}}},
		{Name: "too_many_parts", Title: "Table partition approaching too-many-parts limit", Severity: "warning", File: "too_many_parts.yaml", FiredAt: "2026-09-10T22:58:57Z",
			Message: "{database}.{table} partition '{partition_id}' has {parts_count} active parts — inserts are delayed from 1000 parts (parts_to_delay_insert) and rejected with code 252 TOO_MANY_PARTS at 3000 (parts_to_throw_insert)",
			Rows:    []map[string]interface{}{{"database": "demo_app", "table": "events_summary", "partition_id": "202609", "parts_count": 3469}}},
		{Name: "replica_readonly", Title: "Replica in read-only mode", Severity: "critical", File: "replica_readonly.yaml", FiredAt: "2026-09-10T22:58:57Z", Message: "{database}.{table} is read-only"},
		{Name: "crash_log_entries", Title: "Server crash detected", Severity: "critical", File: "crash_log_entries.yaml", FiredAt: "2026-09-10T22:58:57Z", Message: "crash", Skipped: true, Reason: "table not present"},
	}
	return map[string]interface{}{
		"generated_at": "2026-09-10 22:58:57 UTC", "mode": "onprem", "version": "26.2.1.390",
		"uptime": "1 hours 51 minutes", "total_databases": 410, "total_tables": 33882, "active_parts": 50000, "total_size": "28.40 TiB",
		"alerts":               alerts,
		"keeper_metric_hourly": metric,
		// collect() sets these from hasColumn(); the fixture states them
		// explicitly so the latency panel exercises the "column present"
		// path rather than relying on the absent-key default.
		"keeper_wait_available":    true,
		"keeper_session_available": true,
		"keeper_errors_hourly":     errors,
		"keeper_errors_source":     "system.error_log — every thread, background merges and fetches included",
		"keeper_connection": []map[string]interface{}{
			{"name": "default", "host": "keeper-3.example.internal", "port": "9281", "index": "2", "connected_time": "2026-09-10 21:04:31", "session_uptime_s": "6866", "is_expired": "0", "api_version": "4"},
		},
		"replicas": []map[string]interface{}{
			{"database": "demo_app", "table": "events_summary", "is_leader": 1, "is_readonly": 0, "is_session_expired": 0, "future_parts": 0, "parts_to_check": 0, "queue_size": 1, "inserts_in_queue": 509, "merges_in_queue": 0, "queue_oldest_time": "2026-09-10 21:20:01", "absolute_delay": 5914, "total_replicas": 29, "active_replicas": 29},
			{"database": "demo_app", "table": "device_log", "is_leader": 1, "is_readonly": 0, "is_session_expired": 0, "future_parts": 0, "parts_to_check": 0, "queue_size": 0, "inserts_in_queue": 12, "merges_in_queue": 0, "queue_oldest_time": "2026-09-10 22:40:11", "absolute_delay": 380, "total_replicas": 29, "active_replicas": 29},
		},
		"part_log_by_time": []map[string]interface{}{
			{"time": "2026-09-09 00:00:00", "event_type": "NewPart", "count": 480000}, {"time": "2026-09-09 00:00:00", "event_type": "MergeParts", "count": 133000},
			{"time": "2026-09-10 00:00:00", "event_type": "NewPart", "count": 295000}, {"time": "2026-09-10 00:00:00", "event_type": "MergeParts", "count": 69000},
		},
		"server_errors": []map[string]interface{}{
			{"name": "RECEIVED_ERROR_FROM_REMOTE_IO_SERVER", "code": 86, "value": 118453, "last_error_time": "2026-09-10 22:58:57", "last_error_message": "Received error from remote server <peer>:9010 … HTTP status code: 500 'Internal Server Error'"},
			{"name": "NO_SUCH_INTERSERVER_IO_ENDPOINT", "code": 221, "value": 108529, "last_error_time": "2026-09-10 22:00:29", "last_error_message": "No interserver IO endpoint named SharedMergeTreePartsUpdate:/clickhouse/tables/<uuid>/default/virtual_parts/<replica>"},
			{"name": "KEEPER_EXCEPTION", "code": 999, "value": 214, "last_error_time": "2026-09-10 22:58:53", "last_error_message": "Transaction failed (Bad version): Op #46, path: /clickhouse/tables/<uuid>/default/replicas"},
		},
	}
}

// TestBuildHTML_KeeperIncidentPreview renders the incident fixture and, when
// DASHBOARD_PREVIEW_DIR is set, writes the page there for a human to open.
func TestBuildHTML_KeeperIncidentPreview(t *testing.T) {
	html := buildHTML(keeperIncidentFixture())
	for _, want := range []string{
		`id="sec-keeper"`, `"keeper_metric_hourly"`, `"hw_exceptions":25068351`, `"keeper_health"`,
		`"keeper_connection_blips"`, `"merges_stalled"`, `keeper-3.example.internal`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("preview page missing %q", want)
		}
	}
	if dir := os.Getenv("DASHBOARD_PREVIEW_DIR"); dir != "" {
		dst := filepath.Join(dir, "keeper_incident_preview.html")
		if err := os.WriteFile(dst, []byte(html), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("preview written: %s", dst)
	}
}
