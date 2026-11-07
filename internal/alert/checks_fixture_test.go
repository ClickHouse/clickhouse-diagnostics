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

// 测试用 bundle：每个 check 都在手写的 collector 文件上测试一次，
// 一次刚好超过其阈值（触发），一次刚好低于阈值（干净）。

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

// hoursBack 是从 fixtureNow 所在小时往前 n 小时开始的小时桶。
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
		t.Fatalf("行：%v", rows)
	}
	// 一旦已知，只有用户表策略所使用的磁盘才计入。
	b = fixture(t, map[string][]row{
		"system.disks":            {{"name": "default", "free_bytes": "1", "total_bytes": "100"}, {"name": "cold", "free_bytes": "1", "total_bytes": "100"}},
		"system.tables":           {{"database": "db", "name": "t", "storage_policy": "tiered"}, {"database": "system", "name": "x", "storage_policy": "default"}},
		"system.storage_policies": {{"policy_name": "tiered", "disks": []string{"cold"}}, {"policy_name": "default", "disks": []string{"default"}}},
	})
	if rows := runCheck(t, "disk_space_low", "onprem", b); len(rows) != 1 || rows[0]["disk_name"] != "cold" {
		t.Fatalf("仅数据磁盘：%v", rows)
	}
}

func TestCheck_TooManyParts(t *testing.T) {
	b := fixture(t, map[string][]row{"system.parts_by_partition": {
		{"database": "db", "table": "a", "partition_id": "p", "parts": "301"},
		{"database": "db", "table": "b", "partition_id": "p", "parts": "300"},
	}})
	if rows := runCheck(t, "too_many_parts", "onprem", b); len(rows) != 1 || rows[0]["table"] != "a" {
		t.Fatalf("行：%v", rows)
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
		t.Fatalf("行：%v", rows)
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
		t.Fatalf("行：%v", rows)
	}
}

func TestCheck_CrashLog(t *testing.T) {
	b := fixture(t, map[string][]row{"system.crash_log": {
		{"event_time": "2026-10-28 10:00:00", "signal": 11},
		{"event_time": "2026-10-29 10:00:00", "signal": 6},
	}})
	if rows := runCheck(t, "crash_log_entries", "onprem", b); len(rows) != 2 || rows[0]["signal"] != "6" {
		t.Fatalf("最新的在前：%v", rows)
	}
	// 没有该文件的 Cloud：presence 文件在“不适用”与“指出崩溃无法读取的
	// replica 的错误”之间做决定。
	c, _ := lookupCheck("go:crash_log_entries")
	_, err := c(fixture(t, map[string][]row{"system.crash_log_presence": {{"hostname": "r1", "has_crash_log": 0}}}), "cloud")
	if _, ok := err.(*bundle.NotCollected); !ok {
		t.Fatalf("没有 replica 崩溃过：期望 NotCollected，实际为 %v", err)
	}
	_, err = c(fixture(t, map[string][]row{"system.crash_log_presence": {{"hostname": "r1", "has_crash_log": 1}}}), "cloud")
	if err == nil || !strings.Contains(err.Error(), "r1") {
		t.Fatalf("崩溃的 replica 无法读取：%v", err)
	}
}

func TestCheck_ReplicationQueueErrors(t *testing.T) {
	b := fixture(t, map[string][]row{"system.replication_queue": {
		{"database": "db", "table": "a", "last_exception": "Code: 999", "create_time": "2026-10-29 10:00:00"},
		{"database": "db", "table": "b", "last_exception": ""},
	}})
	if rows := runCheck(t, "replication_queue_errors", "onprem", b); len(rows) != 1 || rows[0]["table"] != "a" {
		t.Fatalf("行：%v", rows)
	}
}

func TestCheck_MutationTooLong(t *testing.T) {
	b := fixture(t, map[string][]row{"system.mutations": {
		{"database": "db", "table": "a", "mutation_id": "m1", "parts_to_do": "1", "create_time": "2026-10-29 08:59:59"}, // 4 个边界
		{"database": "db", "table": "a", "mutation_id": "m1", "parts_to_do": "1", "create_time": "2026-10-29 08:59:59"}, // cloud 重复项
		{"database": "db", "table": "b", "mutation_id": "m2", "parts_to_do": "1", "create_time": "2026-10-29 09:00:00"}, // 3：不大于 3
		{"database": "db", "table": "c", "mutation_id": "m3", "parts_to_do": "1", "create_time": "2026-10-28 00:00:00", "is_killed": 1},
		{"database": "db", "table": "d", "mutation_id": "m4", "parts_to_do": "0", "create_time": "2026-10-28 00:00:00"},
	}})
	rows := runCheck(t, "mutation_running_too_long", "cloud", b)
	if len(rows) != 1 || rows[0]["mutation_id"] != "m1" || rows[0]["hours_running"] != int64(4) {
		t.Fatalf("行：%v", rows)
	}
}

func TestCheck_ReplicaReadonly(t *testing.T) {
	b := fixture(t, map[string][]row{
		"system.replicas":                {{"hostname": "r1", "database": "db", "table": "a", "is_readonly": 1}, {"hostname": "r1", "database": "db", "table": "b", "is_readonly": 1}, {"database": "db", "table": "c", "is_readonly": 0}},
		"system.replicas_problem_tables": {{"hostname": "r1", "database": "db", "table": "a", "active_replicas": 1, "total_replicas": 3}},
	})
	rows := runCheck(t, "replica_readonly", "cloud", b)
	if len(rows) != 2 || rows[0]["active_replicas"] != "1" || rows[1]["total_replicas"] != "?" {
		t.Fatalf("行：%v", rows)
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
		t.Fatalf("merges_stalled：%v", rows)
	}
	if rows := runCheck(t, "background_operation_failures", "onprem", b); len(rows) != 1 || rows[0]["failures"] != int64(51) || rows[0]["example_message"] != "Memory limit" {
		t.Fatalf("background_operation_failures：%v", rows)
	}
}

func TestCheck_ExceptionRules(t *testing.T) {
	q := func(hour, typ, code string, n int) row {
		return row{"time": hour, "type": typ, "exception_code": code, "count": fmt.Sprint(n), "exception": "e" + code}
	}
	const w = "ExceptionWhileProcessing"
	b := fixture(t, map[string][]row{"system.query_log_exceptions_1_day": {
		q(hoursBack(1), w, "60", 51),
		q(hoursBack(1), "ExceptionBeforeStart", "60", 100), // 不是处理过程中
		q(hoursBack(2), w, "62", 50),
		q(hoursBack(26), w, "62", 500), // 24 小时之外
		q(hoursBack(3), w, "999", 21),
		q(hoursBack(4), w, "999", 20),
		q(hoursBack(0), "ExceptionBeforeStart", "202", 6), q(hoursBack(1), "ExceptionBeforeStart", "202", 5),
	}})
	if rows := runCheck(t, "high_exception_rate", "onprem", b); len(rows) != 1 || rows[0]["exception_code"] != int64(60) || rows[0]["error_count"] != int64(51) || rows[0]["example_message"] != "e60" {
		t.Fatalf("high_exception_rate：%v", rows)
	}
	if rows := runCheck(t, "keeper_exception_spike", "onprem", b); len(rows) != 1 || rows[0]["error_count"] != int64(21) {
		t.Fatalf("keeper_exception_spike：%v", rows)
	}
	if rows := runCheck(t, "too_many_simultaneous_queries", "onprem", b); len(rows) != 1 || rows[0]["error_count"] != int64(11) {
		t.Fatalf("too_many_simultaneous_queries：%v", rows)
	}
	ten := fixture(t, map[string][]row{"system.query_log_exceptions_1_day": {q(hoursBack(0), "ExceptionBeforeStart", "202", 10)}})
	if rows := runCheck(t, "too_many_simultaneous_queries", "onprem", ten); len(rows) != 0 {
		t.Fatalf("10 次拒绝不大于 10：%v", rows)
	}
}

// metricHoursFixture：10 个普通的、各有 1000 次事务的小时，然后是
// 被测的小时。
func metricHoursFixture(extra ...row) []row {
	var rows []row
	for i := 20; i < 30; i++ {
		rows = append(rows, row{"time": hoursBack(i), "zk_transactions": "1000", "zk_hw_exceptions": "0", "span_s": "3599"})
	}
	return append(rows, extra...)
}

func TestCheck_KeeperHealthAndBlips(t *testing.T) {
	b := fixture(t, map[string][]row{"system.metric_log_7_days": metricHoursFixture(
		row{"time": hoursBack(1), "zk_transactions": "499", "zk_hw_exceptions": "1001"}, // 已崩溃
		row{"time": hoursBack(2), "zk_transactions": "500", "zk_hw_exceptions": "1001"}, // 一半：短暂抖动
		row{"time": hoursBack(3), "zk_transactions": "100", "zk_hw_exceptions": "1000"}, // 不大于 1000
	)})
	rows := runCheck(t, "keeper_health", "onprem", b)
	if len(rows) != 1 || rows[0]["transactions"] != int64(499) || rows[0]["usual_transactions"] != 1000.0 || rows[0]["pct_of_usual"] != 50.0 || rows[0]["hostname"] != "h1" {
		t.Fatalf("keeper_health：%v", rows)
	}
	rows = runCheck(t, "keeper_connection_blips", "onprem", b)
	if len(rows) != 1 || rows[0]["transactions"] != int64(500) {
		t.Fatalf("keeper_connection_blips：%v", rows)
	}
	if _, ok := rows[0]["usual_transactions"]; ok {
		t.Error("blips 不应包含 usual_transactions 列")
	}
	// Cloud：按主机计算，每台主机对比自己的中位数。
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
		t.Fatalf("cloud keeper_health：%v", rows)
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
		t.Fatalf("fetch：%v", rows)
	}
	// 486.3 < 0.95 × 512：只有 3 个小时符合条件。
	if rows := runCheck(t, "schedule_pool_saturated", "onprem", b); len(rows) != 1 || rows[0]["hours_saturated"] != int64(3) {
		t.Fatalf("schedule：%v", rows)
	}
	// 较旧的服务器：大小在 system.settings 中。
	v228 := []row{{"version": "22.8.21.38", "hostname": "h1", "timezone": "UTC", "collected_at": fixtureNow}}
	b = fixture(t, map[string][]row{"system.version": v228, "system.metric_log_7_days": hours("avg_fetch_pool_tasks", "16", "16", "16"), "system.settings": settings})
	if rows := runCheck(t, "fetch_pool_saturated", "onprem", b); len(rows) != 1 {
		t.Fatalf("22.8 应读取 system.settings：%v", rows)
	}
	b = fixture(t, map[string][]row{"system.version": v228, "system.metric_log_7_days": hours("avg_fetch_pool_tasks", "16", "16"), "system.settings": settings})
	if rows := runCheck(t, "fetch_pool_saturated", "onprem", b); len(rows) != 0 {
		t.Fatalf("两个小时不等于三个：%v", rows)
	}
	// 23.3+ 且没有 server_settings：system.settings 保存的是过时的会话默认值
	//（16，而配置值为 32）—— 不适用，从不据此判定，也不会因为缺少大小
	// 而被判为干净。
	b = fixture(t, map[string][]row{"system.metric_log_7_days": hours("avg_fetch_pool_tasks", "16", "16", "16"), "system.settings": settings})
	c, _ := lookupCheck("go:fetch_pool_saturated")
	if _, err := c(b, "onprem"); !isNotCollected(err) {
		t.Fatalf("23.3+ 且没有 server_settings：期望 NotCollected，实际为 %v", err)
	}
	// Cloud：按主机的实时大小胜过 server_settings。
	cloud := hours("avg_fetch_pool_tasks", "30", "30", "30")
	for _, r := range cloud {
		r["hostname"], r["max(CurrentMetric_BackgroundFetchesPoolSize)"] = "r1", "32"
	}
	b = fixture(t, map[string][]row{"system.metric_log_by_host_3_days": cloud, "system.server_settings": settings})
	if rows := runCheck(t, "fetch_pool_saturated", "cloud", b); len(rows) != 1 || rows[0]["pool_size"] != int64(32) {
		t.Fatalf("cloud：%v", rows)
	}
	// 只有第一个小时缺少大小：不需要 server_settings。
	cloud[0]["max(CurrentMetric_BackgroundFetchesPoolSize)"] = "0"
	b = fixture(t, map[string][]row{"system.metric_log_by_host_3_days": cloud})
	if rows := runCheck(t, "fetch_pool_saturated", "cloud", b); len(rows) != 1 || rows[0]["pool_size"] != int64(32) {
		t.Fatalf("cloud，某一小时缺少大小：%v", rows)
	}
}

func TestCheck_KeeperXid(t *testing.T) {
	// 覆盖的 3 小时内 100 tx/s；2^31-1 − xid = 8 000 000 → 剩余 80 000 秒。
	bucket := func(i int) row {
		return row{"time": hoursBack(i), "zk_transactions": "360000", "span_s": "3599", "first_sample": hoursBack(i), "last_sample": hoursBack(i - 1)}
	}
	hours := []row{bucket(3), bucket(2), bucket(1)}
	conn := func(xid int64) map[string][]row {
		return map[string][]row{"system.metric_log_7_days": hours, "system.zookeeper_connection": {{"name": "default", "host": "k1", "is_expired": 0, "xid": fmt.Sprint(xid)}}}
	}
	rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, conn(xidMax-8_000_000)))
	if len(rows) != 1 || rows[0]["tx_per_s"] != 100.0 || rows[0]["headroom_h"] != 22.2 || rows[0]["measured_over_h"] != 3.0 {
		t.Fatalf("行：%v", rows)
	}
	if rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, conn(xidMax-8_640_000))); len(rows) != 0 {
		t.Fatalf("恰好一天的余量不触发：%v", rows)
	}
	two := conn(xidMax - 8_000_000)
	two["system.zookeeper_connection"] = append(two["system.zookeeper_connection"], row{"name": "aux", "host": "k2", "is_expired": 0, "xid": "5"})
	if rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, two)); len(rows) != 0 {
		t.Fatalf("两个存活会话：速率无法归属，不触发：%v", rows)
	}
	// 两个采样小时相隔 19 小时：跨度约为 20 小时（10 tx/s，222 小时余量），
	// 而不是各桶自身跨度相加得到的 2 小时。
	gap := conn(xidMax - 8_000_000)
	gap["system.metric_log_7_days"] = []row{bucket(20), bucket(1)}
	if rows := runCheck(t, "keeper_xid_renewal_due", "onprem", fixture(t, gap)); len(rows) != 0 {
		t.Fatalf("桶之间的间隙应计入覆盖时间：%v", rows)
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
		t.Fatalf("行：%v", rows)
	}
}

// 每条随包发布的规则都是一个已注册的 Go check，其数据源都是随包发布的
// collector —— 而且在 bundle 上运行 RunAll 不会向服务器发送任何内容。
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
			t.Errorf("%s: check %q，期望 go:%s", p, def.Check, def.Name)
		}
		if len(def.Source) == 0 {
			t.Errorf("%s: 缺少 source: 列表", p)
		}
		for _, s := range def.Source {
			if !collectors[s] {
				t.Errorf("%s: 数据源 %s 不是随包发布的 collector", p, s)
			}
		}
		return nil
	})
	if n < 20 {
		t.Fatalf("只遍历了 %d 条规则", n)
	}
	for _, mode := range []string{"onprem", "cloud", "gov"} {
		sent := 0
		ev := (&Evaluator{mode: mode, queryFn: func(string) (string, error) { sent++; return noRows, nil }}).WithBundle(fixture(t, map[string][]row{}))
		results := ev.RunAll("../../alerts", internal.Version{Major: 26, Minor: 7, Patch: 1, Build: 0})
		if sent != 0 {
			t.Errorf("%s: RunAll 向服务器发送了 %d 条查询", mode, sent)
		}
		for _, r := range results {
			if r.Error != "" {
				t.Errorf("%s: %s 在空 bundle 上出错了：%s", mode, r.Name, r.Error)
			}
		}
	}
}

// 使用 -to 时，规则的“最近 24 小时”以采集文件结束的地方为终点。
func TestCheck_WindowEndsAtExplicitTo(t *testing.T) {
	rows := []row{{"time": hoursBack(40), "hostname": "h1", "event_type": "NewPart", "error": 0, "count": "101"}}
	b := fixture(t, map[string][]row{"system.part_log_3_days": rows})
	if got := runCheck(t, "merges_stalled", "onprem", b); len(got) != 0 {
		t.Fatalf("40 小时前不在截至现在的最近 24 小时之内：%v", got)
	}
	to, _ := time.Parse("2006-01-02 15:04:05", hoursBack(30))
	b = fixture(t, map[string][]row{"system.part_log_3_days": rows}).WithWindowEnd(to)
	if got := runCheck(t, "merges_stalled", "onprem", b); len(got) != 1 {
		t.Fatalf("-to 为 30 小时前时，40 小时前应在其最近 24 小时之内：%v", got)
	}
}

func isNotCollected(err error) bool {
	_, ok := err.(*bundle.NotCollected)
	return ok
}

// 失败 collector 的文件可能是部分写入：其规则不适用，
// 不能根据落到磁盘上的内容来判定。
func TestBundle_FailedCollectorFileIsNotRead(t *testing.T) {
	dir := fixture(t, map[string][]row{"system.parts_by_partition": {{"database": "db", "table": "a", "partition_id": "p", "parts": "5"}}}).Dir()
	b := bundle.Open(dir, map[string]bundle.Status{"system.parts_by_partition": {State: "failed", Reason: "write: no space left on device"}})
	c, _ := lookupCheck("go:too_many_parts")
	if _, err := c(b, "onprem"); !isNotCollected(err) || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("期望指明该失败的 NotCollected，实际为 %v", err)
	}
}

// gov 会哈希 database 名称：system 标志会把 system 表所用的磁盘排除在外。
func TestCheck_DiskSpaceLow_GovSystemFlag(t *testing.T) {
	b := fixture(t, map[string][]row{
		"system.disks":            {{"name": "default", "free_bytes": "1", "total_bytes": "100"}, {"name": "cold", "free_bytes": "50", "total_bytes": "100"}},
		"system.tables":           {{"database": "9F86D0", "name": "A1", "storage_policy": "tiered", "is_system_db": 0}, {"database": "3B867E", "name": "B2", "storage_policy": "default", "is_system_db": 1}},
		"system.storage_policies": {{"policy_name": "tiered", "disks": []string{"cold"}}, {"policy_name": "default", "disks": []string{"default"}}},
	})
	if rows := runCheck(t, "disk_space_low", "gov", b); len(rows) != 0 {
		t.Fatalf("只有 system 表使用的磁盘不属于用户数据：%v", rows)
	}
}

// Cloud 会把共享的 detached part 按每个 replica 各列一次，路径相同。
func TestCheck_DetachedParts_CloudCountsSharedPartOnce(t *testing.T) {
	var rows []row
	for i := 0; i < 3; i++ {
		rows = append(rows, row{"database": "db", "table": "a", "reason": "broken", "bytes_on_disk": "1024", "path": "/s3/store/u/detached/broken_all_1_1_0/"})
	}
	b := fixture(t, map[string][]row{"system.detached_parts": rows})
	got := runCheck(t, "detached_parts_exist", "cloud", b)
	if len(got) != 1 || got[0]["detached_count"] != int64(1) || got[0]["total_size"] != "1.00 KiB" {
		t.Fatalf("行：%v", got)
	}
	if got := runCheck(t, "detached_parts_exist", "onprem", b); got[0]["detached_count"] != int64(3) {
		t.Fatalf("onprem 对每个 part 只列一次；应保留每一行：%v", got)
	}
}

// -to 会移动日志窗口，但不会改变实时快照的时长。
func TestCheck_SnapshotAgesIgnoreTo(t *testing.T) {
	to, _ := time.Parse("2006-01-02 15:04:05", hoursBack(30))
	b := fixture(t, map[string][]row{"system.mutations": {{"database": "db", "table": "a", "mutation_id": "m1", "parts_to_do": "1", "create_time": hoursBack(10)}}}).WithWindowEnd(to)
	if rows := runCheck(t, "mutation_running_too_long", "onprem", b); len(rows) != 1 || rows[0]["hours_running"] != int64(10) {
		t.Fatalf("采集时已存在 10 小时的 mutation：%v", rows)
	}
}

// 没有 bundle（-output-format 为 native/tsv）时，check 规则会实时运行。
func TestEvalFile_NoBundleRunsTheQueryLive(t *testing.T) {
	sent := 0
	ev := &Evaluator{mode: "onprem", queryFn: func(string) (string, error) { sent++; return noRows, nil }}
	r := ev.evalFile("../../alerts/too_many_parts.yaml")
	if sent != 1 || r.Error != "" || r.Skipped {
		t.Fatalf("sent=%d，result=%+v", sent, r)
	}
}
