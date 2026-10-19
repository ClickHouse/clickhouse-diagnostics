package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-pass reads keeper/<host>_<port>.txt — the four-letter replies of
// every Keeper member — and turns them into findings. The fixture is the
// shape of a real quorum loss: a 2.38 GB snapshot sent unchunked, and one
// member that fell behind the leader's retained log, so it could only
// rejoin through that snapshot (P-61).

const notServing = "This instance is not currently serving requests"

type member struct {
	name, isro, lgif, srvr, mntr, conf, dirs string
	ens                                      string // "" = default, "-" = no ensemble line (older bundles)
}

func writeKeeperBundle(t *testing.T, members []member) string {
	t.Helper()
	dir := t.TempDir()
	run := filepath.Join(dir, "clickhouse_diagnostic_20261016_230000")
	if err := os.MkdirAll(filepath.Join(run, "keeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range minimalBundle {
		if err := os.WriteFile(filepath.Join(run, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range members {
		var b strings.Builder
		fmt.Fprintf(&b, "# clickhouse-diagnostic — Keeper four-letter commands\n# target: %s:9181\n", m.name)
		switch m.ens {
		case "":
			b.WriteString("# ensemble: default\n")
		case "-":
		default:
			fmt.Fprintf(&b, "# ensemble: %s\n", m.ens)
		}
		b.WriteString("# collected_at: 2026-10-16T23:00:00Z\n# commands: ruok, isro, lgif, srvr, mntr, conf, dirs\n")
		for _, s := range [][2]string{{"ruok", "imok"}, {"isro", m.isro}, {"lgif", m.lgif}, {"srvr", m.srvr}, {"mntr", m.mntr}, {"conf", m.conf}, {"dirs", m.dirs}} {
			fmt.Fprintf(&b, "\n## %s\n%s\n", s[0], s[1])
		}
		if err := os.WriteFile(filepath.Join(run, "keeper", m.name+"_9181.txt"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

func lgif(first, last, committed, leaderCommitted, snapshot int64) string {
	return fmt.Sprintf("first_log_idx\t%d\nfirst_log_term\t18\nlast_log_idx\t%d\nlast_log_term\t19\nlast_committed_log_idx\t%d\nleader_committed_log_idx\t%d\ntarget_committed_log_idx\t%d\nlast_snapshot_idx\t%d",
		first, last, committed, leaderCommitted, leaderCommitted, snapshot)
}

func keeperFindings(t *testing.T, run string) []map[string]string {
	t.Helper()
	out, errOut, code := runPrepass(t, "--json", run)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var r struct {
		Findings []map[string]string `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("pre-pass did not emit JSON: %v\n%s", err, out)
	}
	var k []map[string]string
	for _, f := range r.Findings {
		if strings.HasPrefix(f["check"], "HC-3.18") || strings.HasPrefix(f["check"], "HC-3.19") || strings.HasPrefix(f["check"], "HC-3.20") {
			k = append(k, f)
		}
	}
	return k
}

func find(fs []map[string]string, check, sev, substr string) bool {
	for _, f := range fs {
		if strings.HasPrefix(f["check"], check) && f["severity"] == sev && strings.Contains(f["message"]+" "+f["evidence"], substr) {
			return true
		}
	}
	return false
}

func TestInspectBundle_KeeperSnapshotTransferQuorumLoss(t *testing.T) {
	const snap = "2400000000"
	conf := "server_id=%d\nsnapshot_distance=1000000\nreserved_log_items=100000\nsnapshot_transfer_chunk_size=0\nwrite_snapshot_version=6"
	run := writeKeeperBundle(t, []member{
		{"keeper-a", "rw", lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000),
			"ClickHouse Keeper version: v26.8.17.4\nMode: leader\nNode count: 90000000",
			"zk_server_state\tleader\nzk_znode_count\t90000000\nzk_approximate_data_size\t18000000000\nzk_latest_snapshot_size\t" + snap,
			fmt.Sprintf(conf, 2), "snapshot_dir_size: 7200000000\nlog_dir_size: 5000000000", ""},
		{"keeper-b", "rw", lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002400000),
			"ClickHouse Keeper version: v26.8.17.4\nMode: follower\nNode count: 90000000",
			"zk_server_state\tfollower\nzk_latest_snapshot_size\t" + snap, fmt.Sprintf(conf, 3), "snapshot_dir_size: 7200000000\nlog_dir_size: 5000000000", ""},
		// Restarted last and came back below the leader's first log index.
		{"keeper-c", "rw", lgif(8996000000, 8999600000, 8999600000, 8999600000, 8996000000), notServing, notServing, notServing, notServing, ""},
	})
	fs := keeperFindings(t, run)
	if !find(fs, "HC-3.20", "critical", "snapshot_transfer_chunk_size") {
		t.Errorf("no critical finding for a 2.2 GiB snapshot sent unchunked: %v", fs)
	}
	if !find(fs, "HC-3.19", "critical", "keeper-c") {
		t.Errorf("no critical finding for the member behind the leader's retained log: %v", fs)
	}
	if !find(fs, "HC-3.20", "warning", "write_snapshot_version") {
		t.Errorf("no warning for snapshot format 6: %v", fs)
	}
	if !find(fs, "HC-3.18", "warning", "keeper-c") {
		t.Errorf("no warning for the member that is not serving (quorum still 2 of 3): %v", fs)
	}
	if find(fs, "HC-3.18", "critical", "") {
		t.Errorf("2 of 3 serving is a quorum, not a quorum loss: %v", fs)
	}
}

func TestInspectBundle_KeeperQuorumLost(t *testing.T) {
	m := func(name string) member {
		return member{name, "rw", lgif(1, 100, 100, 100, 0), notServing, notServing, notServing, notServing, ""}
	}
	// Leaderless: every member answers "not serving" itself.
	fs := keeperFindings(t, writeKeeperBundle(t, []member{m("keeper-a"), m("keeper-b"), m("keeper-c")}))
	if !find(fs, "HC-3.18", "critical", "no member in this bundle is serving") {
		t.Errorf("every member answering 'not serving' is critical: %v", fs)
	}
	// With one member serving, the files cannot prove the other two are the
	// rest of the voters: a warning, not an asserted outage.
	healthy := member{"keeper-a", "rw", lgif(1, 100, 100, 100, 0), "Mode: follower", "zk_server_state\tfollower", "snapshot_transfer_chunk_size=0", "snapshot_dir_size: 0", ""}
	fs = keeperFindings(t, writeKeeperBundle(t, []member{healthy, m("keeper-b"), m("keeper-c")}))
	if find(fs, "HC-3.18", "critical", "") || !find(fs, "HC-3.18", "warning", "keeper-b") {
		t.Errorf("1 serving + 2 not serving: warning, not critical: %v", fs)
	}
}

func TestInspectBundle_KeeperHealthyEnsembleRaisesNothing(t *testing.T) {
	conf := "snapshot_distance=100000\nreserved_log_items=100000\nsnapshot_transfer_chunk_size=100000000\nwrite_snapshot_version=7"
	var ms []member
	for i, mode := range []string{"leader", "follower", "follower"} {
		ms = append(ms, member{fmt.Sprintf("keeper-%d", i), "rw", lgif(1000, 5000, 5000, 5000, 4000),
			"Mode: " + mode, "zk_server_state\t" + mode + "\nzk_latest_snapshot_size\t52428800", conf, "snapshot_dir_size: 157286400\nlog_dir_size: 3372003", ""})
	}
	if fs := keeperFindings(t, writeKeeperBundle(t, ms)); len(fs) != 0 {
		t.Errorf("a healthy ensemble raised: %v", fs)
	}
}

func TestInspectBundle_OlderKeeperWithoutNewCommands(t *testing.T) {
	// Bundles from before these commands, or a 22.8 Keeper: no lgif / conf / dirs.
	empty := "(empty reply — command not enabled in four_letter_word_white_list, or not known to this Keeper version)"
	ms := []member{{"keeper-a", empty, empty, "Mode: leader", "zk_server_state\tleader\nzk_latest_snapshot_size\t2400000000", empty, empty, ""}}
	if fs := keeperFindings(t, writeKeeperBundle(t, ms)); len(fs) != 0 {
		t.Errorf("without conf the chunk size is unknown — no finding may claim it: %v", fs)
	}
}

func TestInspectBundle_KeeperSnapshotFormatDefaultBefore268(t *testing.T) {
	// 26.7 writes format 6 by default: worth knowing, not a warning.
	m := member{"keeper-a", "rw", lgif(1, 100, 100, 100, 0), "ClickHouse Keeper version: v26.7.5.10\nMode: leader",
		"zk_version\tv26.7.5.10\nzk_server_state\tleader", "snapshot_transfer_chunk_size=0\nwrite_snapshot_version=6", "snapshot_dir_size: 0", ""}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{m}))
	if !find(fs, "HC-3.20", "info", "the default on 26.7") || find(fs, "HC-3.20", "warning", "write_snapshot_version") {
		t.Errorf("format 6 on 26.7 is the default: want info, not warning: %v", fs)
	}
}

// ---- review round: what the files can and cannot prove

const truncated = "\nerror: timeout after 10s — reply truncated"

func TestInspectBundle_KeeperTruncatedReplyIsNotEvidence(t *testing.T) {
	// A cut-off lgif: last_log_idx arrived as "8" of a longer number.
	leader := member{name: "keeper-a", isro: "rw", lgif: lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000),
		srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0"}
	cut := member{name: "keeper-b", isro: "rw", lgif: "first_log_idx\t9000000000\nlast_log_idx\t8" + truncated,
		srvr: "skipped — lgif timed out", mntr: "skipped — lgif timed out", conf: "skipped — lgif timed out", dirs: "skipped — lgif timed out"}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{leader, cut}))
	if find(fs, "HC-3.19", "critical", "keeper-b") {
		t.Errorf("a truncated lgif produced a 'behind the log' critical: %v", fs)
	}
	if !find(fs, "HC-3.18", "warning", "keeper-b") {
		t.Errorf("the member whose probe failed must still be reported: %v", fs)
	}
}

func TestInspectBundle_KeeperUnreachableIsNeverAQuorumLoss(t *testing.T) {
	// A remote run probed only the connected member, and it timed out.
	m := member{name: "keeper-a", isro: "error: timeout after 10s", lgif: "skipped — isro timed out", srvr: "skipped — isro timed out",
		mntr: "skipped — isro timed out", conf: "skipped — isro timed out", dirs: "skipped — isro timed out"}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{m}))
	if find(fs, "HC-3.18", "critical", "") || !find(fs, "HC-3.18", "warning", "keeper-a") {
		t.Errorf("a collector-side timeout is a warning, never a quorum loss: %v", fs)
	}
}

func TestInspectBundle_KeeperEnsemblesAreNotCompared(t *testing.T) {
	// The default ensemble is healthy with a large, chunked snapshot; an
	// auxiliary ZooKeeper has its own small log and chunk size 0.
	def := member{name: "keeper-a", isro: "rw", lgif: lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000),
		srvr: "Mode: leader", mntr: "zk_server_state\tleader\nzk_latest_snapshot_size\t3000000000",
		conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0"}
	aux := member{name: "zk-aux", isro: "rw", lgif: lgif(10, 500, 500, 500, 0), srvr: "Mode: follower",
		mntr: "zk_server_state\tfollower\nzk_latest_snapshot_size\t1000", conf: "snapshot_transfer_chunk_size=0", dirs: "snapshot_dir_size: 0", ens: "aux_zk"}
	if fs := keeperFindings(t, writeKeeperBundle(t, []member{def, aux})); len(fs) != 0 {
		t.Errorf("two ensembles were compared with each other: %v", fs)
	}
}

func TestInspectBundle_KeeperFormatJudgedPerMember(t *testing.T) {
	// A rolling upgrade: one member on 26.7 (6 is its default), one on 26.8
	// still writing 6 (pinned below 8).
	old := member{name: "keeper-a", isro: "rw", lgif: lgif(1, 100, 100, 100, 0), srvr: "ClickHouse Keeper version: v26.7.5.10\nMode: leader",
		mntr: "zk_server_state\tleader", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"}
	upgraded := member{name: "keeper-b", isro: "rw", lgif: lgif(1, 100, 100, 100, 0), srvr: "ClickHouse Keeper version: v26.8.17.4\nMode: follower",
		mntr: "zk_server_state\tfollower", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{old, upgraded}))
	if !find(fs, "HC-3.20", "warning", "keeper-b") || find(fs, "HC-3.20", "warning", "keeper-a") {
		t.Errorf("the 26.8 member's pinned format must warn, the 26.7 default must not: %v", fs)
	}
}

func TestInspectBundle_KeeperNoEnsembleLineSkipsCrossMemberChecks(t *testing.T) {
	leader := member{name: "keeper-a", isro: "rw", lgif: lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000),
		srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0", ens: "-"}
	behind := member{name: "keeper-b", isro: "rw", lgif: lgif(8990000000, 8999600000, 8999600000, 8999600000, 8990000000),
		srvr: "Mode: follower", mntr: "zk_server_state\tfollower", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0", ens: "-"}
	if fs := keeperFindings(t, writeKeeperBundle(t, []member{leader, behind})); find(fs, "HC-3.19", "critical", "") {
		t.Errorf("without ensemble identity two members' logs must not be compared: %v", fs)
	}
}
