package alert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/bundle"

	"gopkg.in/yaml.v3"
)

// Fixture bundles: each check is driven over hand-written collector files,
// once just past its threshold (fires) and once just short of it (clean).

const fixtureNow = "2026-10-29 12:00:00"

func fixture(t *testing.T, files map[string][]row) *bundle.Bundle {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["system.version"]; !ok {
		files["system.version"] = []row{{"version": "26.7.1.1", "hostname": "h1", "timezone": "UTC", "collected_at": fixtureNow}}
	}
	for name, rows := range files {
		var sb strings.Builder
		for _, r := range rows {
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			sb.Write(b)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, name+"_20261029_120000.jsonl"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return bundle.Open(dir, nil)
}

func runCheck(t *testing.T, name, mode string, b *bundle.Bundle) []row {
	t.Helper()
	c, err := lookupCheck("go:" + name)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c(b, mode)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return rows
}

// hoursBack is the hour bucket starting n hours before fixtureNow's hour.
func hoursBack(n int) string {
	t, _ := time.Parse("2006-01-02 15:04:05", fixtureNow)
	return t.Add(-time.Duration(n) * time.Hour).Format("2006-01-02 15:04:05")
}

func TestCheck_DiskSpaceLow(t *testing.T) {
	b := fixture(t, map[string][]row{"system.disks": {
		{"name": "default", "free_bytes": "14", "total_bytes": "100"},
		{"name": "big", "free_bytes": "15", "total_bytes": "100"},
	}})
	rows := runCheck(t, "disk_space_low", "onprem", b)
	if len(rows) != 1 || rows[0]["disk_name"] != "default" || rows[0]["free_pct"] != 14.0 || rows[0]["hostname"] != "h1" {
		t.Fatalf("rows: %v", rows)
	}
	// Only the disks user tables' policies use count, once that is known.
	b = fixture(t, map[string][]row{
		"system.disks":            {{"name": "default", "free_bytes": "1", "total_bytes": "100"}, {"name": "cold", "free_bytes": "1", "total_bytes": "100"}},
		"system.tables":           {{"database": "db", "name": "t", "storage_policy": "tiered"}, {"database": "system", "name": "x", "storage_policy": "default"}},
		"system.storage_policies": {{"policy_name": "tiered", "disks": []string{"cold"}}, {"policy_name": "default", "disks": []string{"default"}}},
	})
	if rows := runCheck(t, "disk_space_low", "onprem", b); len(rows) != 1 || rows[0]["disk_name"] != "cold" {
		t.Fatalf("data disks only: %v", rows)
	}
}

func TestCheck_TooManyParts(t *testing.T) {
	b := fixture(t, map[string][]row{"system.parts_by_partition": {
		{"database": "db", "table": "a", "partition_id": "p", "parts": "301"},
		{"database": "db", "table": "b", "partition_id": "p", "parts": "300"},
	}})
	if rows := runCheck(t, "too_many_parts", "onprem", b); len(rows) != 1 || rows[0]["table"] != "a" {
		t.Fatalf("rows: %v", rows)
	}
}

func TestCheck_LargeParts(t *testing.T) {
	limit := int64(150) << 30
	b := fixture(t, map[string][]row{"system.parts": {
		{"database": "db", "table": "a", "name": "p1", "active": 1, "bytes_on_disk": fmt.Sprint(limit + 1), "data_compressed_bytes": "10", "data_uncompressed_bytes": "35"},
		{"database": "db", "table": "b", "name": "p2", "active": 1, "bytes_on_disk": fmt.Sprint(limit)},
		{"database": "db", "table": "c", "name": "p3", "active": 0, "bytes_on_disk": fmt.Sprint(limit * 2)},
	}})
	rows := runCheck(t, "large_parts", "onprem", b)
	if len(rows) != 1 || rows[0]["part_name"] != "p1" || rows[0]["compression_ratio"] != 3.5 || rows[0]["size_on_disk"] != "150.00 GiB" {
		t.Fatalf("rows: %v", rows)
	}
}

func TestCheck_DetachedParts(t *testing.T) {
	b := fixture(t, map[string][]row{"system.detached_parts": {
		{"database": "db", "table": "a", "reason": "broken", "bytes_on_disk": "100"},
		{"database": "db", "table": "a", "reason": "ignored", "bytes_on_disk": "100"},
		{"database": "db", "table": "b", "reason": "broken", "bytes_on_disk": "1000"},
		{"database": "system", "table": "query_log", "reason": "broken", "bytes_on_disk": "9999"},
	}})
	rows := runCheck(t, "detached_parts_exist", "onprem", b)
	if len(rows) != 2 || rows[0]["table"] != "b" || rows[1]["detached_count"] != int64(2) || fmt.Sprint(rows[1]["reasons"]) != "[broken ignored]" {
		t.Fatalf("rows: %v", rows)
	}
}

func TestCheck_CrashLog(t *testing.T) {
	b := fixture(t, map[string][]row{"system.crash_log": {
		{"event_time": "2026-10-28 10:00:00", "signal": 11},
		{"event_time": "2026-10-29 10:00:00", "signal": 6},
	}})
	if rows := runCheck(t, "crash_log_entries", "onprem", b); len(rows) != 2 || rows[0]["signal"] != "6" {
		t.Fatalf("newest first: %v", rows)
	}
	// Cloud without the file: presence decides between not applicable and
	// an error naming the replicas whose crashes could not be read.
	c, _ := lookupCheck("go:crash_log_entries")
	_, err := c(fixture(t, map[string][]row{"system.crash_log_presence": {{"hostname": "r1", "has_crash_log": 0}}}), "cloud")
	if _, ok := err.(*bundle.NotCollected); !ok {
		t.Fatalf("no replica crashed: want NotCollected, got %v", err)
	}
	_, err = c(fixture(t, map[string][]row{"system.crash_log_presence": {{"hostname": "r1", "has_crash_log": 1}}}), "cloud")
	if err == nil || !strings.Contains(err.Error(), "r1") {
		t.Fatalf("crashed replica unread: %v", err)
	}
}

func TestCheck_ReplicationQueueErrors(t *testing.T) {
	b := fixture(t, map[string][]row{"system.replication_queue": {
		{"database": "db", "table": "a", "last_exception": "Code: 999", "create_time": "2026-10-29 10:00:00"},
		{"database": "db", "table": "b", "last_exception": ""},
	}})
	if rows := runCheck(t, "replication_queue_errors", "onprem", b); len(rows) != 1 || rows[0]["table"] != "a" {
		t.Fatalf("rows: %v", rows)
	}
}

func TestCheck_MutationTooLong(t *testing.T) {
	b := fixture(t, map[string][]row{"system.mutations": {
		{"database": "db", "table": "a", "mutation_id": "m1", "parts_to_do": "1", "create_time": "2026-10-29 08:59:59"}, // 4 boundaries
		{"database": "db", "table": "a", "mutation_id": "m1", "parts_to_do": "1", "create_time": "2026-10-29 08:59:59"}, // cloud duplicate
		{"database": "db", "table": "b", "mutation_id": "m2", "parts_to_do": "1", "create_time": "2026-10-29 09:00:00"}, // 3: not > 3
		{"database": "db", "table": "c", "mutation_id": "m3", "parts_to_do": "1", "create_time": "2026-10-28 00:00:00", "is_killed": 1},
		{"database": "db", "table": "d", "mutation_id": "m4", "parts_to_do": "0", "create_time": "2026-10-28 00:00:00"},
	}})
	rows := runCheck(t, "mutation_running_too_long", "cloud", b)
	if len(rows) != 1 || rows[0]["mutation_id"] != "m1" || rows[0]["hours_running"] != int64(4) {
		t.Fatalf("rows: %v", rows)
	}
}

func TestCheck_ReplicaReadonly(t *testing.T) {
	b := fixture(t, map[string][]row{
		"system.replicas":                {{"hostname": "r1", "database": "db", "table": "a", "is_readonly": 1}, {"hostname": "r1", "database": "db", "table": "b", "is_readonly": 1}, {"database": "db", "table": "c", "is_readonly": 0}},
		"system.replicas_problem_tables": {{"hostname": "r1", "database": "db", "table": "a", "active_replicas": 1, "total_replicas": 3}},
	})
	rows := runCheck(t, "replica_readonly", "cloud", b)
	if len(rows) != 2 || rows[0]["active_replicas"] != "1" || rows[1]["total_replicas"] != "?" {
		t.Fatalf("rows: %v", rows)
	}
}

func TestCheck_MergesStalledAndFailures(t *testing.T) {
	h := hoursBack(1)
	b := fixture(t, map[string][]row{"system.part_log_3_days": {
		{"time": h, "hostname": "h1", "event_type": "NewPart", "error": 0, "count": "101"},
		{"time": h, "hostname": "h1", "event_type": "MergeParts", "error": 241, "count": "51", "exception": "Memory limit"},
		{"time": h, "hostname": "h2", "event_type": "NewPart", "error": 0, "count": "101"},
		{"time": h, "hostname": "h2", "event_type": "MergeParts", "error": 0, "count": "1"},
		{"time": hoursBack(25), "hostname": "h3", "event_type": "NewPart", "error": 0, "count": "500"}, // outside 24 h
		{"time": hoursBack(2), "hostname": "h1", "event_type": "MergeParts", "error": 241, "count": "50"},
	}})
	if rows := runCheck(t, "merges_stalled", "onprem", b); len(rows) != 1 || rows[0]["hostname"] != "h1" || rows[0]["failed_merges"] != int64(51) {
		t.Fatalf("merges_stalled: %v", rows)
	}
	if rows := runCheck(t, "background_operation_failures", "onprem", b); len(rows) != 1 || rows[0]["failures"] != int64(51) || rows[0]["example_message"] != "Memory limit" {
		t.Fatalf("background_operation_failures: %v", rows)
	}
}

func TestCheck_ExceptionRules(t *testing.T) {
	q := func(hour, typ, code string, n int) row {
		return row{"time": hour, "type": typ, "exception_code": code, "count": fmt.Sprint(n), "exception": "e" + code}
	}
	const w = "ExceptionWhileProcessing"
	b := fixture(t, map[string][]row{"system.query_log_exceptions_1_day": {
		q(hoursBack(1), w, "60", 51),
		q(hoursBack(1), "ExceptionBeforeStart", "60", 100), // not while processing
		q(hoursBack(2), w, "62", 50),
		q(hoursBack(26), w, "62", 500), // outside 24 h
		q(hoursBack(3), w, "999", 21),
		q(hoursBack(4), w, "999", 20),
		q(hoursBack(0), "ExceptionBeforeStart", "202", 6), q(hoursBack(1), "ExceptionBeforeStart", "202", 5),
	}})
	if rows := runCheck(t, "high_exception_rate", "onprem", b); len(rows) != 1 || rows[0]["exception_code"] != int64(60) || rows[0]["error_count"] != int64(51) || rows[0]["example_message"] != "e60" {
		t.Fatalf("high_exception_rate: %v", rows)
	}
	if rows := runCheck(t, "keeper_exception_spike", "onprem", b); len(rows) != 1 || rows[0]["error_count"] != int64(21) {
		t.Fatalf("keeper_exception_spike: %v", rows)
	}
	if rows := runCheck(t, "too_many_simultaneous_queries", "onprem", b); len(rows) != 1 || rows[0]["error_count"] != int64(11) {
		t.Fatalf("too_many_simultaneous_queries: %v", rows)
	}
	ten := fixture(t, map[string][]row{"system.query_log_exceptions_1_day": {q(hoursBack(0), "ExceptionBeforeStart", "202", 10)}})
	if rows := runCheck(t, "too_many_simultaneous_queries", "onprem", ten); len(rows) != 0 {
		t.Fatalf("10 refusals is not more than 10: %v", rows)
	}
}

// metricHoursFixture: 10 ordinary hours of 1000 transactions, then the
// hours under test.
func metricHoursFixture(extra ...row) []row {
	var rows []row
	for i := 20; i < 30; i++ {
		rows = append(rows, row{"time": hoursBack(i), "zk_transactions": "1000", "zk_hw_exceptions": "0", "span_s": "3599"})
	}
	return append(rows, extra...)
}

func TestCheck_KeeperHealthAndBlips(t *testing.T) {
	b := fixture(t, map[string][]row{"system.metric_log_7_days": metricHoursFixture(
		row{"time": hoursBack(1), "zk_transactions": "499", "zk_hw_exceptions": "1001"}, // collapsed
		row{"time": hoursBack(2), "zk_transactions": "500", "zk_hw_exceptions": "1001"}, // half: blip
		row{"time": hoursBack(3), "zk_transactions": "100", "zk_hw_exceptions": "1000"}, // not > 1000
	)})
	rows := runCheck(t, "keeper_health", "onprem", b)
	if len(rows) != 1 || rows[0]["transactions"] != int64(499) || rows[0]["usual_transactions"] != 1000.0 || rows[0]["pct_of_usual"] != 50.0 || rows[0]["hostname"] != "h1" {
		t.Fatalf("keeper_health: %v", rows)
	}
	rows = runCheck(t, "keeper_connection_blips", "onprem", b)
	if len(rows) != 1 || rows[0]["transactions"] != int64(500) {
		t.Fatalf("keeper_connection_blips: %v", rows)
	}
	if _, ok := rows[0]["usual_transactions"]; ok {
		t.Error("blips has no usual_transactions column")
	}
	// Cloud: per host, each against its own median.
	var cloud []row
	for _, h := range []string{"r1", "r2"} {
		for _, r := range metricHoursFixture() {
			r["hostname"] = h
			cloud = append(cloud, r)
		}
	}
	cloud = append(cloud, row{"hostname": "r2", "time": hoursBack(1), "zk_transactions": "10", "zk_hw_exceptions": "5000"})
	rows = runCheck(t, "keeper_health", "cloud", fixture(t, map[string][]row{"system.metric_log_by_host_3_days": cloud}))
	if len(rows) != 1 || rows[0]["hostname"] != "r2" {
		t.Fatalf("cloud keeper_health: %v", rows)
	}
}

func TestCheck_PoolSaturated(t *testing.T) {
	hours := func(col string, vals ...string) []row {
		var rows []row
		for i, v := range vals {
			rows = append(rows, row{"time": hoursBack(i + 1), col: v})
		}
		return rows
	}
	settings := []row{{"name": "background_fetches_pool_size", "value": "16"}, {"name": "background_schedule_pool_size", "value": "512"}}
	b := fixture(t, map[string][]row{
		"system.metric_log_7_days": append(hours("avg_fetch_pool_tasks", "14.4", "16", "15", "1"),
			hours("avg_schedule_pool_tasks", "486.4", "500", "486.3", "512")...),
		"system.server_settings": settings,
	})
	rows := runCheck(t, "fetch_pool_saturated", "onprem", b)
	if len(rows) != 1 || rows[0]["hours_saturated"] != int64(3) || rows[0]["pool_size"] != int64(16) || rows[0]["avg_tasks_in_pool"] != 15.1 {
		t.Fatalf("fetch: %v", rows)
	}
	// 486.3 < 0.95 × 512: only 3 hours qualify.
	if rows := runCheck(t, "schedule_pool_saturated", "onprem", b); len(rows) != 1 || rows[0]["hours_saturated"] != int64(3) {
		t.Fatalf("schedule: %v", rows)
	}
	// Older server: the size is in system.settings.
	v228 := []row{{"version": "22.8.21.38", "hostname": "h1", "timezone": "UTC", "collected_at": fixtureNow}}
	b = fixture(t, map[string][]row{"system.version": v228, "system.metric_log_7_days": hours("avg_fetch_pool_tasks", "16", "16", "16"), "system.settings": settings})
	if rows := runCheck(t, "fetch_pool_saturated", "onprem", b); len(rows) != 1 {
		t.Fatalf("22.8 reads system.settings: %v", rows)
	}
	b = fixture(t, map[string][]row{"system.version": v228, "system.metric_log_7_days": hours("avg_fetch_pool_tasks", "16", "16"), "system.settings": settings})
	if rows := runCheck(t, "fetch_pool_saturated", "onprem", b); len(rows) != 0 {
		t.Fatalf("two hours are not three: %v", rows)
	}
	// 23.3+ without server_settings: system.settings holds the obsolete
	// session default (16 against a configured 32) — not applicable, never
	// judged against it, never clean for want of a size.
	b = fixture(t, map[string][]row{"system.metric_log_7_days": hours("avg_fetch_pool_tasks", "16", "16", "16"), "system.settings": settings})
	c, _ := lookupCheck("go:fetch_pool_saturated")
	if _, err := c(b, "onprem"); !isNotCollected(err) {
		t.Fatalf("23.3+ without server_settings: want NotCollected, got %v", err)
	}
	// Cloud: the live per-host size wins over server_settings.
	cloud := hours("avg_fetch_pool_tasks", "30", "30", "30")
	for _, r := range cloud {
		r["hostname"], r["max(CurrentMetric_BackgroundFetchesPoolSize)"] = "r1", "32"
	}
	b = fixture(t, map[string][]row{"system.metric_log_by_host_3_days": cloud, "system.server_settings": settings})
	if rows := runCheck(t, "fetch_pool_saturated", "cloud", b); len(rows) != 1 || rows[0]["pool_size"] != int64(32) {
		t.Fatalf("cloud: %v", rows)
	}
}

func TestCheck_KeeperXid(t *testing.T) {
	// 100 tx/s over 3 covered hours; 2^31-1 − xid = 8 000 000 → 80 000 s left.
	bucket := func(i int) row {
		return row{"time": hoursBack(i), "zk_transactions": "360000", "span_s": "3599", "first_sample": hoursBack(i), "last_sample": hoursBack(i - 1)}
	}
	hours := []row{bucket(3), bucket(2), bucket(1)}
	conn := func(xid int64) map[string][]row {
		return map[string][]row{"system.metric_log_7_days": hours, "system.zookeeper_connection": {{"name": "default", "host": "k1", "is_expired": 0, "xid": fmt.Sprint(xid)}}}
	}
	rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, conn(xidMax-8_000_000)))
	if len(rows) != 1 || rows[0]["tx_per_s"] != 100.0 || rows[0]["headroom_h"] != 22.2 || rows[0]["measured_over_h"] != 3.0 {
		t.Fatalf("rows: %v", rows)
	}
	if rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, conn(xidMax-8_640_000))); len(rows) != 0 {
		t.Fatalf("exactly a day of headroom does not fire: %v", rows)
	}
	two := conn(xidMax - 8_000_000)
	two["system.zookeeper_connection"] = append(two["system.zookeeper_connection"], row{"name": "aux", "host": "k2", "is_expired": 0, "xid": "5"})
	if rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, two)); len(rows) != 0 {
		t.Fatalf("two live sessions: the rate cannot be attributed, does not fire: %v", rows)
	}
	// Two sampled hours 19 h apart: the span is ~20 h (10 tx/s, 222 h of
	// headroom), not the 2 h the buckets' own spans add up to.
	gap := conn(xidMax - 8_000_000)
	gap["system.metric_log_7_days"] = []row{bucket(20), bucket(1)}
	if rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, gap)); len(rows) != 0 {
		t.Fatalf("gaps between buckets count as covered time: %v", rows)
	}
}

func TestCheck_PartsPropagationLag(t *testing.T) {
	b := fixture(t, map[string][]row{"system.parts_max_block_by_replica": {
		{"hostname": "r2", "database": "db", "table": "t", "partition_id": "p1", "blocks_behind": "40", "newest_part_skew_s": "9",
			"cluster_max_block": "100", "cluster_last_part_time": "2026-10-29 11:00:00", "replicas_missing_partition": []string{"r3"}},
		{"hostname": "r1", "database": "db", "table": "t", "partition_id": "p1", "blocks_behind": "31",
			"cluster_max_block": "100", "cluster_last_part_time": "2026-10-29 11:00:00", "replicas_missing_partition": []string{"r3"}},
		{"hostname": "r1", "database": "db", "table": "t", "partition_id": "old", "blocks_behind": "0",
			"cluster_max_block": "7", "cluster_last_part_time": "2026-10-27 11:00:00", "replicas_missing_partition": []string{"r3"}},
	}})
	rows := runCheck(t, "parts_propagation_lag", "cloud", b)
	if len(rows) != 2 || rows[0]["hostname"] != "r3" || rows[0]["partitions_not_fetched"] != int64(1) || rows[0]["max_blocks_behind"] != int64(100) ||
		rows[1]["hostname"] != "r2" || rows[1]["worst_partition"] != "db.t / p1" {
		t.Fatalf("rows: %v", rows)
	}
}

// Every shipped rule is a Go check, registered, whose sources are shipped
// collectors — and RunAll over a bundle sends nothing to the server.
func TestShippedRules_AreGoChecksOverCollectors(t *testing.T) {
	collectors := map[string]bool{}
	for _, m := range []string{"onprem", "cloud", "gov"} {
		_ = filepath.Walk("../../queries."+m, func(p string, info os.FileInfo, err error) error {
			if err == nil && strings.HasSuffix(p, ".sql") {
				collectors[strings.TrimSuffix(filepath.Base(p), ".sql")] = true
			}
			return nil
		})
	}
	n := 0
	_ = filepath.Walk("../../alerts", func(p string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(p, ".yaml") {
			return nil
		}
		n++
		raw, _ := os.ReadFile(p)
		var def Definition
		if err := yaml.Unmarshal(raw, &def); err != nil {
			t.Errorf("%s: %v", p, err)
			return nil
		}
		if _, err := lookupCheck(def.Check); err != nil {
			t.Errorf("%s: %v", p, err)
		}
		if def.Check != "go:"+def.Name {
			t.Errorf("%s: check %q, want go:%s", p, def.Check, def.Name)
		}
		if len(def.Source) == 0 {
			t.Errorf("%s: no source: list", p)
		}
		for _, s := range def.Source {
			if !collectors[s] {
				t.Errorf("%s: source %s is not a shipped collector", p, s)
			}
		}
		return nil
	})
	if n < 20 {
		t.Fatalf("walked only %d rules", n)
	}
	for _, mode := range []string{"onprem", "cloud", "gov"} {
		sent := 0
		ev := (&Evaluator{mode: mode, queryFn: func(string) (string, error) { sent++; return noRows, nil }}).WithBundle(fixture(t, map[string][]row{}))
		results := ev.RunAll("../../alerts", internal.Version{Major: 26, Minor: 7, Patch: 1, Build: 0})
		if sent != 0 {
			t.Errorf("%s: RunAll sent %d query(ies) to the server", mode, sent)
		}
		for _, r := range results {
			if r.Error != "" {
				t.Errorf("%s: %s errored on an empty bundle: %s", mode, r.Name, r.Error)
			}
		}
	}
}

// With -to, the rules' "last 24 hours" ends where the collected files end.
func TestCheck_WindowEndsAtExplicitTo(t *testing.T) {
	rows := []row{{"time": hoursBack(40), "hostname": "h1", "event_type": "NewPart", "error": 0, "count": "101"}}
	b := fixture(t, map[string][]row{"system.part_log_3_days": rows})
	if got := runCheck(t, "merges_stalled", "onprem", b); len(got) != 0 {
		t.Fatalf("40 h back is outside the last 24 h of a run up to now: %v", got)
	}
	to, _ := time.Parse("2006-01-02 15:04:05", hoursBack(30))
	b = fixture(t, map[string][]row{"system.part_log_3_days": rows}).WithWindowEnd(to)
	if got := runCheck(t, "merges_stalled", "onprem", b); len(got) != 1 {
		t.Fatalf("with -to 30 h back, 40 h back is inside its last 24 h: %v", got)
	}
}

func isNotCollected(err error) bool {
	_, ok := err.(*bundle.NotCollected)
	return ok
}

// A failed collector's file may be a partial write: its rule is not
// applicable, not judged on what reached the disk.
func TestBundle_FailedCollectorFileIsNotRead(t *testing.T) {
	dir := fixture(t, map[string][]row{"system.parts_by_partition": {{"database": "db", "table": "a", "partition_id": "p", "parts": "5"}}}).Dir()
	b := bundle.Open(dir, map[string]bundle.Status{"system.parts_by_partition": {State: "failed", Reason: "write: no space left on device"}})
	c, _ := lookupCheck("go:too_many_parts")
	if _, err := c(b, "onprem"); !isNotCollected(err) || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("want NotCollected naming the failure, got %v", err)
	}
}

// Gov hashes database names: the system flag keeps system-table disks out.
func TestCheck_DiskSpaceLow_GovSystemFlag(t *testing.T) {
	b := fixture(t, map[string][]row{
		"system.disks":            {{"name": "default", "free_bytes": "1", "total_bytes": "100"}, {"name": "cold", "free_bytes": "50", "total_bytes": "100"}},
		"system.tables":           {{"database": "9F86D0", "name": "A1", "storage_policy": "tiered", "is_system_db": 0}, {"database": "3B867E", "name": "B2", "storage_policy": "default", "is_system_db": 1}},
		"system.storage_policies": {{"policy_name": "tiered", "disks": []string{"cold"}}, {"policy_name": "default", "disks": []string{"default"}}},
	})
	if rows := runCheck(t, "disk_space_low", "gov", b); len(rows) != 0 {
		t.Fatalf("a disk only system tables use is not user data: %v", rows)
	}
}

// Cloud lists a shared detached part once per replica, with one path.
func TestCheck_DetachedParts_CloudCountsSharedPartOnce(t *testing.T) {
	var rows []row
	for i := 0; i < 3; i++ {
		rows = append(rows, row{"database": "db", "table": "a", "reason": "broken", "bytes_on_disk": "1024", "path": "/s3/store/u/detached/broken_all_1_1_0/"})
	}
	b := fixture(t, map[string][]row{"system.detached_parts": rows})
	got := runCheck(t, "detached_parts_exist", "cloud", b)
	if len(got) != 1 || got[0]["detached_count"] != int64(1) || got[0]["total_size"] != "1.00 KiB" {
		t.Fatalf("rows: %v", got)
	}
	if got := runCheck(t, "detached_parts_exist", "onprem", b); got[0]["detached_count"] != int64(3) {
		t.Fatalf("onprem lists each part once; keep every row: %v", got)
	}
}

// -to moves the log windows, not the age of a live snapshot.
func TestCheck_SnapshotAgesIgnoreTo(t *testing.T) {
	to, _ := time.Parse("2006-01-02 15:04:05", hoursBack(30))
	b := fixture(t, map[string][]row{"system.mutations": {{"database": "db", "table": "a", "mutation_id": "m1", "parts_to_do": "1", "create_time": hoursBack(10)}}}).WithWindowEnd(to)
	if rows := runCheck(t, "mutation_running_too_long", "onprem", b); len(rows) != 1 || rows[0]["hours_running"] != int64(10) {
		t.Fatalf("a mutation 10 h old at collection: %v", rows)
	}
}

// Without a bundle (-output-format native/tsv) a check rule runs live.
func TestEvalFile_NoBundleRunsTheQueryLive(t *testing.T) {
	sent := 0
	ev := &Evaluator{mode: "onprem", queryFn: func(string) (string, error) { sent++; return noRows, nil }}
	r := ev.evalFile("../../alerts/too_many_parts.yaml")
	if sent != 1 || r.Error != "" || r.Skipped {
		t.Fatalf("sent=%d result=%+v", sent, r)
	}
}
