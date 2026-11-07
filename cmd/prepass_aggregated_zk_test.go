package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pre-pass 会读取三个 system.aggregated_zookeeper_log 文件（26.3+），
// 并将它们转换为命名 P-62-P-68 模式的 HC-3.21-3.23 finding。下面的每个
// fixture 都是某个已知模式在现场呈现的形态，缩小到刚好越过阈值的最小规模，
// 因此任何本会漏掉它的阈值改动都会在这里失败。
func runPrepassAZL(t *testing.T, files map[string][]map[string]any) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("pre-pass 脚本不存在：%v", err)
	}
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	files["system.version"] = []map[string]any{{"version": "26.7.1.1"}}
	for name, rows := range files {
		var sb strings.Builder
		for _, r := range rows {
			line, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			sb.Write(line)
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(b, name+"_20261029_120000.jsonl"), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass 失败（退出码 %v）：\n%s", err, out)
	}
	return string(out)
}

func minuteRows(host string, n int, ops, latencyUs int) []map[string]any {
	rows := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		r := map[string]any{"time": fmt.Sprintf("2026-10-29 10:%02d:00", i), "ops": ops, "in_multi": 0,
			"avg_latency_us": latencyUs, "worst_group_latency_us": latencyUs, "failed": 0, "loss_failures": 0, "sessions": 1}
		if host != "" {
			r["hostname"] = host
		}
		rows = append(rows, r)
	}
	return rows
}

func mustHave(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("pre-pass 输出缺少 %q\n%s", w, out)
		}
	}
}

// P-65 形态：一个 replica 的 Keeper 延迟以秒计，而同一 ensemble 中的
// 其他节点处于正常的 10-15 ms。
func TestPrepassAZL_OneSlowReplica(t *testing.T) {
	var rows []map[string]any
	rows = append(rows, minuteRows("replica-a", 30, 500, 1_900_000)...)
	rows = append(rows, minuteRows("replica-b", 30, 500, 12_000)...)
	rows = append(rows, minuteRows("replica-c", 30, 500, 14_000)...)
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_minute_1_day": rows})
	mustHave(t, out, "某个 replica 的 Keeper 请求偏慢：replica-a", "HC-3.21/P-65")
	if strings.Contains(out, "偏慢：replica-b") || strings.Contains(out, "偏慢：replica-c") {
		t.Errorf("健康的 replica 被标记了：\n%s", out)
	}
}

// P-64 重启形态：重启那一分钟的请求量高出一个数量级。
func TestPrepassAZL_RestartBurst(t *testing.T) {
	rows := minuteRows("", 30, 2_000, 12_000)
	rows[10]["ops"] = 160_000
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_minute_1_day": rows})
	mustHave(t, out, "Keeper 请求突发", "2026-10-29 10:10:00", "HC-3.21/P-64")
}

// 实验室那台停止取数的节点：一个后台任务发送了服务器几乎全部的
// Keeper 请求，在某些小时远高于其通常水平。
func TestPrepassAZL_HotComponent(t *testing.T) {
	var rows []map[string]any
	for h := 0; h < 24; h++ {
		ops := 2_000
		if h == 20 {
			ops = 500_000
		}
		rows = append(rows,
			map[string]any{"time": fmt.Sprintf("2026-10-28 %02d:00:00", h), "component": "ReplicatedMergeMutateTaskBase::executeStep",
				"operation": "List", "ops": ops, "in_multi": 0, "avg_latency_us": 900, "errors": map[string]any{}},
			map[string]any{"time": fmt.Sprintf("2026-10-28 %02d:00:00", h), "component": "ReplicatedMergeTreeCleanupThread",
				"operation": "Get", "ops": 300, "in_multi": 0, "avg_latency_us": 400, "errors": map[string]any{}})
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_hour_2_days": rows})
	mustHave(t, out, "ReplicatedMergeMutateTaskBase::executeStep 占全部 Keeper 请求的", "HC-3.22/P-68", "ClickHouse 各 component 的 Keeper 请求")
}

// P-62 形态：各 replica 在裁剪相同的去重记录，因此大多数 remove 得到
// ZNONODE；而去重路径占了大多数 remove。
func TestPrepassAZL_DedupRemoveStorm(t *testing.T) {
	hours := []map[string]any{{"time": "2026-10-28 09:00:00", "component": "ReplicatedMergeTreeCleanupThread", "operation": "Remove",
		"ops": 3_000_000, "in_multi": 0, "avg_latency_us": 3_000, "errors": map[string]any{"ZNONODE": 2_700_000}}}
	paths := []map[string]any{
		{"path": "/clickhouse/tables/t1/async_blocks", "ops": 2_000_000, "in_multi": 0, "creates": 200_000, "removes": 1_900_000, "net_creates": -1_700_000, "reads": 0, "avg_latency_us": 2_000, "raw_paths": 1},
		{"path": "/clickhouse/tables/t1/deduplication_hashes", "ops": 1_200_000, "in_multi": 0, "creates": 100_000, "removes": 1_100_000, "net_creates": -1_000_000, "reads": 0, "avg_latency_us": 2_000, "raw_paths": 1},
		// 其他 remove，使去重占比约为 62%——刚好越过 50% 的线，
		// 这正是本 fixture 所固定的。
		{"path": "/clickhouse/tables/t1/replicas/r1", "ops": 2_700_000, "in_multi": 0, "creates": 1_000, "removes": 1_800_000, "net_creates": -1_799_000, "reads": 898_000, "avg_latency_us": 800, "raw_paths": 1},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{
		"system.aggregated_zookeeper_log_by_hour_2_days": hours,
		"system.aggregated_zookeeper_log_paths_1_day":    paths,
	})
	mustHave(t, out, "返回 ZNONODE", "HC-3.22/P-62", "插入去重记录占 Keeper remove 的", "HC-3.23/P-62")
}

// P-63 形态：磁盘元数据 znode 的创建速度是删除速度的数千倍。
// P-64 形态：一个 _objects 类承载了大多数请求，其中几乎都是
// MultiRead 内部的子请求。
func TestPrepassAZL_ObjectsGrowthAndReadStorm(t *testing.T) {
	paths := []map[string]any{
		{"path": "/disk-s3/mergetree/_objects/N", "ops": 40_000_000, "in_multi": 39_000_000, "creates": 15_000_000, "removes": 1_000, "net_creates": 14_999_000, "reads": 24_000_000, "avg_latency_us": 0, "raw_paths": 20000},
		{"path": "/clickhouse/tables/t1/blocks", "ops": 2_000_000, "in_multi": 100_000, "creates": 1_000_000, "removes": 990_000, "net_creates": 10_000, "reads": 10_000, "avg_latency_us": 2_000, "raw_paths": 1},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_paths_1_day": paths})
	mustHave(t, out, "/disk-s3/mergetree/_objects/N 下成功的 create 远多于 remove", "HC-3.23/P-63",
		"请求净增", "用 keeper/<member>.txt 中的 zk_znode_count 确认",
		"占最繁忙 Keeper 路径请求的 95%", "在 Multi 内", "HC-3.23/P-64")
	if strings.Contains(out, "**critical**") {
		t.Errorf("仅有请求失衡不得判为 critical：\n%s", out)
	}
}

// 当 bundle 中包含 Keeper 文件时，增长 finding 会引用独立证据：
// 每个成员自身的 znode 数量。
func TestPrepassAZL_GrowthCitesZnodeCount(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	b := filepath.Join(t.TempDir(), "clickhouse_diagnostic_20261029_120000")
	if err := os.MkdirAll(filepath.Join(b, "keeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(b, "system.version_20261029_120000.jsonl"), []byte(`{"version":"26.7.1.1"}`+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(b, "system.aggregated_zookeeper_log_paths_1_day_20261029_120000.jsonl"),
		[]byte(`{"path":"/d/mergetree/_objects/N","ops":"5000000","in_multi":"0","creates":"4000000","removes":"10","net_creates":"3999990","reads":"0","avg_latency_us":"0","raw_paths":"10"}`+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(b, "keeper", "keeper1_9181.txt"), []byte("# target: keeper1:9181\n\n## mntr\nzk_znode_count\t98000000\n"), 0o644)
	out, err := exec.Command(py, "-I", "-B", script, b).CombinedOutput()
	if err != nil {
		t.Fatalf("pre-pass 失败：%v\n%s", err, out)
	}
	mustHave(t, string(out), "zk_znode_count：keeper1_9181 98.0M")
}

// Cloud 行会跨 replica 求和：finding 会指明单个 replica 小时中最大的那个，
// 而不是赢下最多安静小时的那个。
func TestPrepassAZL_BusiestReplicaIsTheLargestContribution(t *testing.T) {
	var rows []map[string]any
	for h := 0; h < 24; h++ {
		r := map[string]any{"time": fmt.Sprintf("2026-10-28 %02d:00:00", h), "component": "ReplicatedMergeMutateTaskBase::executeStep",
			"operation": "List", "ops": 2_000, "in_multi": 0, "avg_latency_us": 900, "errors": map[string]any{},
			"replicas": 3, "busiest_replica": "replica-quiet", "busiest_replica_ops": 1_000}
		if h == 20 {
			r["ops"], r["busiest_replica"], r["busiest_replica_ops"] = 500_000, "replica-loud", 480_000
		}
		rows = append(rows, r)
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_hour_2_days": rows})
	mustHave(t, out, "其最大的单 replica 小时：480.0k 个 List 请求来自 replica-loud")
	if strings.Contains(out, "replica-quiet") {
		t.Errorf("赢下安静小时的那个 replica 被点名了：\n%s", out)
	}
}

// ZNONODE 占比以 errors map 覆盖的同一批请求为分母——
// 包括 Multi 内部的子请求——因此批量的风暴能被捕获，
// 且占比绝不会超过 100%。
func TestPrepassAZL_DedupStormInsideMulti(t *testing.T) {
	hours := []map[string]any{{"time": "2026-10-28 09:00:00", "component": "AsyncBlockIDsCache", "operation": "Remove",
		"ops": 0, "in_multi": 2_000_000, "avg_latency_us": 0, "errors": map[string]any{"ZNONODE": 1_500_000}}}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_hour_2_days": hours})
	mustHave(t, out, "2.0M 次 Keeper remove 中有 75% 返回 ZNONODE", "HC-3.22/P-62")
}

// 由 create 构成的 _objects 类是热路径，不是读风暴。
func TestPrepassAZL_ObjectsCreatesAreNotAReadStorm(t *testing.T) {
	paths := []map[string]any{
		{"path": "/disk-s3/mergetree/_objects/N", "ops": 4_000_000, "in_multi": 0, "creates": 400_000, "removes": 390_000, "net_creates": 10_000, "reads": 0, "avg_latency_us": 0, "raw_paths": 9000},
		{"path": "/clickhouse/tables/t1/blocks", "ops": 1_000_000, "in_multi": 0, "creates": 1_000, "removes": 1_000, "net_creates": 0, "reads": 0, "avg_latency_us": 0, "raw_paths": 1},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_paths_1_day": paths})
	mustHave(t, out, "占最繁忙 Keeper 路径请求的 80%")
	if strings.Contains(out, "P-64") || strings.Contains(out, "从对象存储元数据加载") {
		t.Errorf("以 create 为主的 _objects 类被标为读风暴：\n%s", out)
	}
}

// gov bundle 携带的是路径形态而不是路径本身。
func TestPrepassAZL_GovShape(t *testing.T) {
	paths := []map[string]any{{"shape": "/*/mergetree/_objects/N", "path_id": "AB", "ops": 5_000_000, "in_multi": 0,
		"creates": 4_000_000, "removes": 100, "net_creates": 3_999_900, "reads": 0, "avg_latency_us": 0, "raw_paths": 9000}}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_paths_1_day": paths})
	mustHave(t, out, "/*/mergetree/_objects/N 下成功的 create 远多于 remove", "HC-3.23/P-63")
}

// 26.3 以下这些文件都不存在；pre-pass 必须对此只字不提。
func TestPrepassAZL_AbsentFilesAreSilent(t *testing.T) {
	out := runPrepassAZL(t, map[string][]map[string]any{})
	for _, id := range []string{"HC-3.21", "HC-3.22", "HC-3.23"} {
		if strings.Contains(out, id) {
			t.Errorf("没有 aggregated_zookeeper_log 文件，却报告了 %s：\n%s", id, out)
		}
	}
}

// 健康的流量不会越过任何阈值。
func TestPrepassAZL_HealthyIsQuiet(t *testing.T) {
	out := runPrepassAZL(t, map[string][]map[string]any{
		"system.aggregated_zookeeper_log_by_minute_1_day": append(minuteRows("replica-a", 30, 2_000, 12_000), minuteRows("replica-b", 30, 2_000, 14_000)...),
	})
	for _, id := range []string{"HC-3.21/"} {
		if strings.Contains(out, id) {
			t.Errorf("健康的分钟产生了 %s：\n%s", id, out)
		}
	}
}

// 只在缓慢时段出现的 replica，如果同一批分钟里它的同伴同样慢，
// 就不算“慢 replica”：要比较对应的分钟。
func TestPrepassAZL_SlowReplicaComparesTheSameMinutes(t *testing.T) {
	var rows []map[string]any
	at := func(host string, minute int, latencyUs int) map[string]any {
		return map[string]any{"time": fmt.Sprintf("2026-10-29 %02d:%02d:00", 10+minute/60, minute%60), "hostname": host, "ops": 500, "in_multi": 0,
			"avg_latency_us": latencyUs, "worst_group_latency_us": latencyUs, "failed": 0, "loss_failures": 0, "sessions": 1}
	}
	// replica-a 和 replica-b：前 60 分钟很快，随后所有节点慢 20 分钟。
	for m := 0; m < 80; m++ {
		lat := 12_000
		if m >= 60 {
			lat = 1_500_000
		}
		rows = append(rows, at("replica-a", m, lat), at("replica-b", m, lat))
	}
	// replica-new 只在那缓慢的 20 分钟里存在，延迟相同。
	for m := 60; m < 80; m++ {
		rows = append(rows, at("replica-new", m, 1_500_000))
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_minute_1_day": rows})
	if strings.Contains(out, "某个 replica 的 Keeper 请求偏慢") {
		t.Errorf("某 replica 因同伴同样缓慢的分钟而被归咎：\n%s", out)
	}
}

// 只在故障小时存在的组件仍然有安静（零）小时：
// 它的典型小时是 0，而不是峰值。
func TestPrepassAZL_HotComponentCountsItsZeroHours(t *testing.T) {
	var rows []map[string]any
	for h := 0; h < 24; h++ {
		rows = append(rows, map[string]any{"time": fmt.Sprintf("2026-10-28 %02d:00:00", h), "component": "ReplicatedMergeTreeCleanupThread",
			"operation": "Get", "ops": 300, "in_multi": 0, "avg_latency_us": 400, "errors": map[string]any{}})
	}
	rows = append(rows, map[string]any{"time": "2026-10-28 20:00:00", "component": "StorageReplicatedMergeTree::fetchPart",
		"operation": "List", "ops": 500_000, "in_multi": 0, "avg_latency_us": 900, "errors": map[string]any{}})
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_hour_2_days": rows})
	mustHave(t, out, "StorageReplicatedMergeTree::fetchPart 占全部 Keeper 请求的", "HC-3.22/P-68")
}
