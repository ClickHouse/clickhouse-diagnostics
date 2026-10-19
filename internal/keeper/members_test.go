package keeper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadMembers turns keeper/<member>.txt into one row per member for the
// dashboard, with the same verdicts as the pre-pass checks (HC-3.18-3.20):
// a truncated reply is not evidence, members are compared only within one
// ensemble and only against an observed leader, the snapshot format is
// judged against each member's own version.

type fileSpec struct {
	name, ens                          string
	isro, lgif, srvr, mntr, conf, dirs string
}

func writeMemberFiles(t *testing.T, specs []fileSpec) string {
	t.Helper()
	run := t.TempDir()
	if err := os.MkdirAll(filepath.Join(run, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range specs {
		var b strings.Builder
		fmt.Fprintf(&b, "# clickhouse-diagnostic — Keeper four-letter commands\n# target: %s:9181\n", m.name)
		if m.ens != "-" {
			ens := m.ens
			if ens == "" {
				ens = "default"
			}
			fmt.Fprintf(&b, "# ensemble: %s\n", ens)
		}
		b.WriteString("# collected_at: 2026-10-19T12:00:00Z\n")
		for _, s := range [][2]string{{"ruok", "imok"}, {"isro", m.isro}, {"lgif", m.lgif}, {"srvr", m.srvr}, {"mntr", m.mntr}, {"conf", m.conf}, {"dirs", m.dirs}} {
			fmt.Fprintf(&b, "\n## %s\n%s\n", s[0], s[1])
		}
		if err := os.WriteFile(filepath.Join(run, DirName, m.name+"_9181.txt"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return run
}

func logInfo(first, last int64) string {
	return fmt.Sprintf("first_log_idx\t%d\nlast_log_idx\t%d\nlast_committed_log_idx\t%d\nleader_committed_log_idx\t%d\nlast_snapshot_idx\t%d", first, last, last, last, first)
}

const ns = "This instance is not currently serving requests"

func incident(t *testing.T) []Member {
	conf := "snapshot_distance=1000000\nreserved_log_items=100000\nsnapshot_transfer_chunk_size=0\nwrite_snapshot_version=6"
	return ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(9000000000, 9002500000), srvr: "ClickHouse Keeper version: v26.8.17.4\nMode: leader",
			mntr: "zk_server_state\tleader\nzk_znode_count\t90000000\nzk_latest_snapshot_size\t2400000000", conf: conf, dirs: "snapshot_dir_size: 7200000000\nlog_dir_size: 5000000000"},
		{name: "keeper-b", isro: "rw", lgif: logInfo(9000000000, 9002500000), srvr: "ClickHouse Keeper version: v26.8.17.4\nMode: follower",
			mntr: "zk_server_state\tfollower\nzk_latest_snapshot_size\t2400000000", conf: conf, dirs: "snapshot_dir_size: 7200000000\nlog_dir_size: 5000000000"},
		{name: "keeper-c", isro: "rw", lgif: logInfo(8996000000, 8999600000), srvr: ns, mntr: ns, conf: ns, dirs: ns},
	}))
}

func byName(ms []Member) map[string]Member {
	m := map[string]Member{}
	for _, x := range ms {
		m[strings.TrimSuffix(x.Member, ":9181")] = x
	}
	return m
}

func hasFlag(m Member, check, sev string) bool {
	for _, f := range m.Flags {
		if strings.HasPrefix(f.Check, check) && f.Severity == sev {
			return true
		}
	}
	return false
}

func TestReadMembers_IncidentShape(t *testing.T) {
	ms := byName(incident(t))
	a, c := ms["keeper-a"], ms["keeper-c"]
	if a.State != "serving" || a.Role != "leader" || a.Version != "26.8.17.4" || a.Ensemble != "default" {
		t.Errorf("leader row = %+v", a)
	}
	if a.LastLogIdx == nil || *a.LastLogIdx != 9002500000 || a.SnapshotSize == nil || *a.SnapshotSize != 2400000000 || a.ChunkSize == nil || *a.ChunkSize != 0 {
		t.Errorf("leader numbers = %+v", a)
	}
	if c.State != "not serving" || !hasFlag(c, "HC-3.18", "warning") {
		t.Errorf("keeper-c must be not serving with a warning: %+v", c)
	}
	if !hasFlag(c, "HC-3.19", "critical") {
		t.Errorf("keeper-c is behind the leader's retained log: %+v", c.Flags)
	}
	for _, n := range []string{"keeper-a", "keeper-b"} {
		if !hasFlag(ms[n], "HC-3.20", "critical") || !hasFlag(ms[n], "HC-3.20", "warning") {
			t.Errorf("%s: want the unchunked >2 GiB snapshot (critical) and the pinned format 6 on 26.8 (warning): %+v", n, ms[n].Flags)
		}
		if hasFlag(ms[n], "HC-3.19", "critical") {
			t.Errorf("%s is up to date", n)
		}
	}
}

func TestReadMembers_HealthyEnsembleHasNoFlags(t *testing.T) {
	conf := "snapshot_transfer_chunk_size=100000000\nwrite_snapshot_version=8"
	var specs []fileSpec
	for i, mode := range []string{"leader", "follower", "follower"} {
		specs = append(specs, fileSpec{name: fmt.Sprintf("keeper-%d", i), isro: "rw", lgif: logInfo(1000, 5000),
			srvr: "ClickHouse Keeper version: v26.8.17.4\nMode: " + mode, mntr: "zk_server_state\t" + mode + "\nzk_latest_snapshot_size\t52428800",
			conf: conf, dirs: "snapshot_dir_size: 1\nlog_dir_size: 1"})
	}
	for _, m := range ReadMembers(writeMemberFiles(t, specs)) {
		if len(m.Flags) != 0 {
			t.Errorf("%s flagged in a healthy ensemble: %+v", m.Member, m.Flags)
		}
	}
}

func TestReadMembers_NoLeaderNoSnapshotVerdict(t *testing.T) {
	ms := ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(1000, 2000), srvr: "Mode: follower", mntr: "zk_server_state\tfollower", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-b", isro: "rw", lgif: logInfo(1, 500), srvr: "Mode: follower", mntr: "zk_server_state\tfollower", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
	}))
	for _, m := range ms {
		if hasFlag(m, "HC-3.19", "critical") {
			t.Errorf("no leader answered, yet %s was declared behind it", m.Member)
		}
	}
}

func TestReadMembers_TruncatedReplyIsNotEvidence(t *testing.T) {
	ms := byName(ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(9000000000, 9002500000), srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-b", isro: "rw", lgif: "first_log_idx\t9000000000\nlast_log_idx\t8\nerror: timeout after 10s — reply truncated",
			srvr: "skipped — lgif timed out", mntr: "skipped — lgif timed out", conf: "skipped — lgif timed out", dirs: "skipped — lgif timed out"},
	})))
	b := ms["keeper-b"]
	if b.LastLogIdx != nil || hasFlag(b, "HC-3.19", "critical") || b.State != "probe failed" {
		t.Errorf("a truncated lgif must not be read: %+v", b)
	}
}

func TestReadMembers_EnsemblesAreNotCompared(t *testing.T) {
	ms := byName(ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(9000000000, 9002500000), srvr: "Mode: leader", mntr: "zk_server_state\tleader\nzk_latest_snapshot_size\t3000000000", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0"},
		{name: "zk-aux", ens: "aux_zk", isro: "rw", lgif: logInfo(10, 500), srvr: "Mode: follower", mntr: "zk_server_state\tfollower\nzk_latest_snapshot_size\t1000", conf: "snapshot_transfer_chunk_size=0", dirs: "snapshot_dir_size: 0"},
	})))
	if len(ms["zk-aux"].Flags) != 0 || len(ms["keeper-a"].Flags) != 0 {
		t.Errorf("two ensembles were compared: %+v / %+v", ms["keeper-a"].Flags, ms["zk-aux"].Flags)
	}
}

func TestReadMembers_FormatPerMemberVersion(t *testing.T) {
	ms := byName(ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(1, 100), srvr: "ClickHouse Keeper version: v26.7.5.10\nMode: leader", mntr: "zk_server_state\tleader", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-b", isro: "rw", lgif: logInfo(1, 100), srvr: "ClickHouse Keeper version: v26.8.17.4\nMode: follower", mntr: "zk_server_state\tfollower", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"},
	})))
	if !hasFlag(ms["keeper-a"], "HC-3.20", "info") || hasFlag(ms["keeper-a"], "HC-3.20", "warning") || !hasFlag(ms["keeper-b"], "HC-3.20", "warning") {
		t.Errorf("format 6: info on 26.7 (the default), warning on 26.8 (pinned): %+v / %+v", ms["keeper-a"].Flags, ms["keeper-b"].Flags)
	}
}

func TestReadMembers_NoFilesNoRows(t *testing.T) {
	if ms := ReadMembers(t.TempDir()); len(ms) != 0 {
		t.Errorf("no keeper/ directory must give no rows: %+v", ms)
	}
}

// Leaderless names the ensembles the pre-pass calls critical under HC-3.18:
// two or more members answering "not serving" themselves and none serving.
// Collector-side failures never count.
func TestLeaderless(t *testing.T) {
	m := func(name, state string) Member { return Member{Member: name, Ensemble: "default", State: state} }
	cases := []struct {
		ms   []Member
		want int
	}{
		{[]Member{m("a", "not serving"), m("b", "not serving"), m("c", "not serving")}, 1},
		{[]Member{m("a", "serving"), m("b", "not serving"), m("c", "not serving")}, 0},
		{[]Member{m("a", "probe failed"), m("b", "probe failed")}, 0},
		{[]Member{m("a", "not serving")}, 0},
		{[]Member{{Member: "a", State: "not serving"}, {Member: "b", State: "not serving"}}, 0}, // ensemble unknown
	}
	for i, c := range cases {
		if got := Leaderless(c.ms); len(got) != c.want {
			t.Errorf("case %d: Leaderless = %v, want %d ensemble(s)", i, got, c.want)
		}
	}
}

// Files from before the ensemble line: each member is judged on its own, so
// two members' logs are never compared.
func TestReadMembers_NoEnsembleLineSkipsCrossMemberChecks(t *testing.T) {
	ms := ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", ens: "-", isro: "rw", lgif: logInfo(9000000000, 9002500000), srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-b", ens: "-", isro: "rw", lgif: logInfo(8990000000, 8999600000), srvr: "Mode: follower", mntr: "zk_server_state\tfollower", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
	}))
	for _, m := range ms {
		if hasFlag(m, "HC-3.19", "critical") {
			t.Errorf("without ensemble identity %s was compared with another member's log", m.Member)
		}
	}
}

// mntr reports the build suffix (v26.7.17.7-stable-<git hash>); the panel
// shows the version number.
func TestReadMembers_VersionWithoutBuildSuffix(t *testing.T) {
	ms := ReadMembers(writeMemberFiles(t, []fileSpec{{name: "keeper-a", isro: "rw", lgif: logInfo(1, 2), srvr: "Mode: leader",
		mntr: "zk_version\tv26.7.17.7-stable-7abaef160cefd843cdcbbc980c8fcae258bb392c\nzk_server_state\tleader", conf: "x=1", dirs: "snapshot_dir_size: 0"}}))
	if ms[0].Version != "26.7.17.7" {
		t.Errorf("Version = %q, want 26.7.17.7", ms[0].Version)
	}
}
