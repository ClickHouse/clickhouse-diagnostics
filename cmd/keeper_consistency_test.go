package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"clickhouse-diagnostic/internal/keeper"
)

// The dashboard's Keeper members panel (Go, internal/keeper.ReadMembers) and
// the pre-pass (Python, HC-3.18-3.20) read the same keeper/ files with the
// same rules, written twice. This runs both on the same fixtures and requires
// the same verdict for every member, so the two cannot drift apart.
func TestKeeperVerdicts_DashboardAndPrepassAgree(t *testing.T) {
	conf26 := "snapshot_distance=1000000\nsnapshot_transfer_chunk_size=0\nwrite_snapshot_version=6"
	ver := func(v, mode string) string { return "ClickHouse Keeper version: v" + v + "\nMode: " + mode }
	fixtures := map[string][]member{
		"incident": {
			{"keeper-a", "rw", lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000), ver("26.8.17.4", "leader"), "zk_server_state\tleader\nzk_latest_snapshot_size\t2400000000", conf26, "snapshot_dir_size: 1", ""},
			{"keeper-b", "rw", lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002400000), ver("26.8.17.4", "follower"), "zk_server_state\tfollower\nzk_latest_snapshot_size\t2400000000", conf26, "snapshot_dir_size: 1", ""},
			{"keeper-c", "rw", lgif(8996000000, 8999600000, 8999600000, 8999600000, 8996000000), notServing, notServing, notServing, notServing, ""},
		},
		"leaderless": {
			{"keeper-a", "rw", lgif(1, 100, 100, 100, 0), notServing, notServing, notServing, notServing, ""},
			{"keeper-b", "rw", lgif(1, 100, 100, 100, 0), notServing, notServing, notServing, notServing, ""},
		},
		"growing snapshot, rolling upgrade": {
			{"keeper-a", "rw", lgif(1, 100, 100, 100, 0), ver("26.7.5.10", "leader"), "zk_server_state\tleader\nzk_latest_snapshot_size\t1500000000", "snapshot_transfer_chunk_size=0\nwrite_snapshot_version=6", "snapshot_dir_size: 1", ""},
			{"keeper-b", "rw", lgif(1, 100, 100, 100, 0), ver("26.8.17.4", "follower"), "zk_server_state\tfollower", "snapshot_transfer_chunk_size=100000000\nwrite_snapshot_version=6", "snapshot_dir_size: 1", ""},
		},
		"no leader": {
			{"keeper-a", "rw", lgif(1000, 2000, 2000, 2000, 900), "Mode: follower", "zk_server_state\tfollower", "snapshot_transfer_chunk_size=1", "snapshot_dir_size: 0", ""},
			{"keeper-b", "rw", lgif(1, 500, 500, 500, 0), "Mode: follower", "zk_server_state\tfollower", "snapshot_transfer_chunk_size=1", "snapshot_dir_size: 0", ""},
		},
		"two ensembles": {
			{"keeper-a", "rw", lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000), "Mode: leader", "zk_server_state\tleader\nzk_latest_snapshot_size\t3000000000", "snapshot_transfer_chunk_size=100000000", "snapshot_dir_size: 0", ""},
			{"zk-aux", "rw", lgif(10, 500, 500, 500, 0), "Mode: follower", "zk_server_state\tfollower\nzk_latest_snapshot_size\t1000", "snapshot_transfer_chunk_size=0", "snapshot_dir_size: 0", "aux_zk"},
		},
	}
	for name, ms := range fixtures {
		t.Run(name, func(t *testing.T) {
			run := writeKeeperBundle(t, ms)
			out, errOut, code := runPrepass(t, "--json", run)
			if code != 0 {
				t.Fatalf("pre-pass exit %d: %s", code, errOut)
			}
			var pp struct {
				Findings []map[string]string `json:"findings"`
				Members  []map[string]any    `json:"keeper_members"`
			}
			if err := json.Unmarshal([]byte(out), &pp); err != nil {
				t.Fatal(err)
			}
			goMembers := keeper.ReadMembers(run)

			// One verdict string per member and check, from each side.
			prepass, dashboard := map[string]bool{}, map[string]bool{}
			for _, m := range pp.Members {
				prepass[fmt.Sprintf("%v state=%v", m["member"], m["state"])] = true
			}
			for _, m := range goMembers {
				dashboard[fmt.Sprintf("%v state=%v", m.Member, m.State)] = true
				for _, f := range m.Flags {
					if f.Check == "HC-3.18" {
						continue // per-member "not serving" mark; the pre-pass groups it into one line
					}
					dashboard[fmt.Sprintf("%s %s %s", m.Member, strings.SplitN(f.Check, "/", 2)[0]+map[bool]string{true: "/P-61", false: ""}[strings.Contains(f.Check, "P-61")], f.Severity)] = true
				}
			}
			for _, f := range pp.Findings {
				c := f["check"]
				if !strings.HasPrefix(c, "HC-3.19") && !strings.HasPrefix(c, "HC-3.20") {
					continue
				}
				for _, m := range goMembers {
					if strings.Contains(f["message"], m.Member) || (strings.HasPrefix(c, "HC-3.20/") && strings.Contains(f["message"], "on ") && strings.Contains(f["message"], m.Member)) {
						prepass[fmt.Sprintf("%s %s %s", m.Member, c, f["severity"])] = true
					}
				}
			}
			if testing.Verbose() {
				t.Logf("%d verdicts agreed: %s", len(prepass), strings.Join(keys(prepass), " | "))
			}
			if a, b := keys(prepass), keys(dashboard); strings.Join(a, "\n") != strings.Join(b, "\n") {
				t.Errorf("verdicts differ\npre-pass:\n  %s\ndashboard:\n  %s", strings.Join(a, "\n  "), strings.Join(b, "\n  "))
			}
			leaderlessPrepass := false
			for _, f := range pp.Findings {
				if strings.HasPrefix(f["check"], "HC-3.18") && f["severity"] == "critical" {
					leaderlessPrepass = true
				}
			}
			if leaderlessPrepass != (len(keeper.Leaderless(goMembers)) > 0) {
				t.Errorf("leaderless: pre-pass %v, dashboard %v", leaderlessPrepass, keeper.Leaderless(goMembers))
			}
		})
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
