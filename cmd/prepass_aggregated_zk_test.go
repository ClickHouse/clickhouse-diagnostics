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

// The pre-pass reads the three system.aggregated_zookeeper_log files (26.3+)
// and turns them into HC-3.21-3.23 findings that name a P-62-P-68 pattern.
// Each fixture below is the shape one known pattern showed in the field,
// scaled down to the minimum that crosses the threshold, so a threshold edit
// that would have missed it fails here.
func runPrepassAZL(t *testing.T, files map[string][]map[string]any) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("pre-pass script not present: %v", err)
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
		t.Fatalf("pre-pass failed (exit %v):\n%s", err, out)
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
			t.Errorf("pre-pass output lacks %q\n%s", w, out)
		}
	}
}

// P-65 shape: one replica's Keeper latency in seconds while its siblings sit
// at a normal 10-15 ms on the same ensemble.
func TestPrepassAZL_OneSlowReplica(t *testing.T) {
	var rows []map[string]any
	rows = append(rows, minuteRows("replica-a", 30, 500, 1_900_000)...)
	rows = append(rows, minuteRows("replica-b", 30, 500, 12_000)...)
	rows = append(rows, minuteRows("replica-c", 30, 500, 14_000)...)
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_minute_1_day": rows})
	mustHave(t, out, "One replica's Keeper requests are slow: replica-a", "HC-3.21/P-65")
	if strings.Contains(out, "slow: replica-b") || strings.Contains(out, "slow: replica-c") {
		t.Errorf("a healthy replica was flagged:\n%s", out)
	}
}

// P-64 restart shape: a restart minute with an order of magnitude more requests.
func TestPrepassAZL_RestartBurst(t *testing.T) {
	rows := minuteRows("", 30, 2_000, 12_000)
	rows[10]["ops"] = 160_000
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_minute_1_day": rows})
	mustHave(t, out, "Keeper request burst", "2026-10-29 10:10:00", "HC-3.21/P-64")
}

// The lab's stopped-fetches node: one background task sends nearly all of
// the server's Keeper requests, in hours far above its usual level.
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
	mustHave(t, out, "ReplicatedMergeMutateTaskBase::executeStep sends", "HC-3.22/P-68", "Keeper requests by ClickHouse component")
}

// P-62 shape: replicas trimming the same dedup records, so most
// removes answer ZNONODE; and the dedup paths take most removes.
func TestPrepassAZL_DedupRemoveStorm(t *testing.T) {
	hours := []map[string]any{{"time": "2026-10-28 09:00:00", "component": "ReplicatedMergeTreeCleanupThread", "operation": "Remove",
		"ops": 3_000_000, "in_multi": 0, "avg_latency_us": 3_000, "errors": map[string]any{"ZNONODE": 2_700_000}}}
	paths := []map[string]any{
		{"path": "/clickhouse/tables/t1/async_blocks", "ops": 2_000_000, "in_multi": 0, "creates": 200_000, "removes": 1_900_000, "net_creates": -1_700_000, "reads": 0, "avg_latency_us": 2_000, "raw_paths": 1},
		{"path": "/clickhouse/tables/t1/deduplication_hashes", "ops": 1_200_000, "in_multi": 0, "creates": 100_000, "removes": 1_100_000, "net_creates": -1_000_000, "reads": 0, "avg_latency_us": 2_000, "raw_paths": 1},
		// Other removes, so the dedup share is about 62% — just over the 50%
		// line, which is what this fixture pins.
		{"path": "/clickhouse/tables/t1/replicas/r1", "ops": 2_700_000, "in_multi": 0, "creates": 1_000, "removes": 1_800_000, "net_creates": -1_799_000, "reads": 898_000, "avg_latency_us": 800, "raw_paths": 1},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{
		"system.aggregated_zookeeper_log_by_hour_2_days": hours,
		"system.aggregated_zookeeper_log_paths_1_day":    paths,
	})
	mustHave(t, out, "answered ZNONODE", "HC-3.22/P-62", "Insert-deduplication records take", "HC-3.23/P-62")
}

// P-63 shape: disk-metadata znodes created thousands of times faster than
// they are removed. P-64 shape: one _objects class carrying most requests,
// nearly all of them sub-requests inside MultiRead.
func TestPrepassAZL_ObjectsGrowthAndReadStorm(t *testing.T) {
	paths := []map[string]any{
		{"path": "/disk-s3/mergetree/_objects/N", "ops": 40_000_000, "in_multi": 39_000_000, "creates": 15_000_000, "removes": 1_000, "net_creates": 14_999_000, "reads": 24_000_000, "avg_latency_us": 0, "raw_paths": 20000},
		{"path": "/clickhouse/tables/t1/blocks", "ops": 2_000_000, "in_multi": 100_000, "creates": 1_000_000, "removes": 990_000, "net_creates": 10_000, "reads": 10_000, "avg_latency_us": 2_000, "raw_paths": 1},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_paths_1_day": paths})
	mustHave(t, out, "Successful creates far above removes under /disk-s3/mergetree/_objects/N", "HC-3.23/P-63",
		"a request balance", "confirm with zk_znode_count",
		"takes 95% of the requests", "inside Multi", "HC-3.23/P-64")
	if strings.Contains(out, "**critical**") {
		t.Errorf("a request balance alone must not be critical:\n%s", out)
	}
}

// With the Keeper files in the bundle, the growth finding cites the
// independent evidence: each member's own znode count.
func TestPrepassAZL_GrowthCitesZnodeCount(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
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
		t.Fatalf("pre-pass failed: %v\n%s", err, out)
	}
	mustHave(t, string(out), "zk_znode_count: keeper1_9181 98.0M")
}

// Cloud rows are summed over replicas: the finding names the replica of the
// largest single-replica hour, not the one that won the most quiet hours.
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
	mustHave(t, out, "largest single-replica hour: 480.0k List requests from replica-loud")
	if strings.Contains(out, "replica-quiet") {
		t.Errorf("the replica that won the quiet hours was named:\n%s", out)
	}
}

// The ZNONODE share is over the same requests the errors map covers —
// sub-requests inside Multi included — so a batched storm is caught and the
// share never exceeds 100 %.
func TestPrepassAZL_DedupStormInsideMulti(t *testing.T) {
	hours := []map[string]any{{"time": "2026-10-28 09:00:00", "component": "AsyncBlockIDsCache", "operation": "Remove",
		"ops": 0, "in_multi": 2_000_000, "avg_latency_us": 0, "errors": map[string]any{"ZNONODE": 1_500_000}}}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_by_hour_2_days": hours})
	mustHave(t, out, "75% of 2.0M Keeper removes answered ZNONODE", "HC-3.22/P-62")
}

// An _objects class made of creates is a hot path, not a read storm.
func TestPrepassAZL_ObjectsCreatesAreNotAReadStorm(t *testing.T) {
	paths := []map[string]any{
		{"path": "/disk-s3/mergetree/_objects/N", "ops": 4_000_000, "in_multi": 0, "creates": 400_000, "removes": 390_000, "net_creates": 10_000, "reads": 0, "avg_latency_us": 0, "raw_paths": 9000},
		{"path": "/clickhouse/tables/t1/blocks", "ops": 1_000_000, "in_multi": 0, "creates": 1_000, "removes": 1_000, "net_creates": 0, "reads": 0, "avg_latency_us": 0, "raw_paths": 1},
	}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_paths_1_day": paths})
	mustHave(t, out, "takes 80% of the requests")
	if strings.Contains(out, "P-64") || strings.Contains(out, "loaded from object-storage metadata") {
		t.Errorf("a create-heavy _objects class was labelled a read storm:\n%s", out)
	}
}

// Gov bundles carry the path shape instead of the path.
func TestPrepassAZL_GovShape(t *testing.T) {
	paths := []map[string]any{{"shape": "/*/mergetree/_objects/N", "path_id": "AB", "ops": 5_000_000, "in_multi": 0,
		"creates": 4_000_000, "removes": 100, "net_creates": 3_999_900, "reads": 0, "avg_latency_us": 0, "raw_paths": 9000}}
	out := runPrepassAZL(t, map[string][]map[string]any{"system.aggregated_zookeeper_log_paths_1_day": paths})
	mustHave(t, out, "Successful creates far above removes under /*/mergetree/_objects/N", "HC-3.23/P-63")
}

// Below 26.3 none of the files exist; the pre-pass must say nothing about it.
func TestPrepassAZL_AbsentFilesAreSilent(t *testing.T) {
	out := runPrepassAZL(t, map[string][]map[string]any{})
	for _, id := range []string{"HC-3.21", "HC-3.22", "HC-3.23"} {
		if strings.Contains(out, id) {
			t.Errorf("no aggregated_zookeeper_log files, yet %s reported:\n%s", id, out)
		}
	}
}

// Healthy traffic crosses no threshold.
func TestPrepassAZL_HealthyIsQuiet(t *testing.T) {
	out := runPrepassAZL(t, map[string][]map[string]any{
		"system.aggregated_zookeeper_log_by_minute_1_day": append(minuteRows("replica-a", 30, 2_000, 12_000), minuteRows("replica-b", 30, 2_000, 14_000)...),
	})
	for _, id := range []string{"HC-3.21/"} {
		if strings.Contains(out, id) {
			t.Errorf("healthy minutes produced %s:\n%s", id, out)
		}
	}
}
