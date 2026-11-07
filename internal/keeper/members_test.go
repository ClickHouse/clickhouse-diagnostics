package keeper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadMembers 将 keeper/<member>.txt 转换为 dashboard 的每成员一行，
// 判定与预检检查一致（HC-3.18-3.20）：
// 截断的回复不构成证据，成员只在一个 ensemble 内比较，
// 且只与观察到的 leader 比较，快照格式按每个成员
// 自身的版本判断。

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
		t.Errorf("leader 行 = %+v", a)
	}
	if a.LastLogIdx == nil || *a.LastLogIdx != 9002500000 || a.SnapshotSize == nil || *a.SnapshotSize != 2400000000 || a.ChunkSize == nil || *a.ChunkSize != 0 {
		t.Errorf("leader 数值 = %+v", a)
	}
	if c.State != "not serving" || !hasFlag(c, "HC-3.18", "warning") {
		t.Errorf("keeper-c 必须是 not serving 并带 warning：%+v", c)
	}
	if !hasFlag(c, "HC-3.19", "critical") {
		t.Errorf("keeper-c 落后于 leader 保留的日志：%+v", c.Flags)
	}
	for _, n := range []string{"keeper-a", "keeper-b"} {
		if !hasFlag(ms[n], "HC-3.20", "critical") || !hasFlag(ms[n], "HC-3.20", "warning") {
			t.Errorf("%s：期望未分块 >2 GiB 快照（critical）以及 26.8 上钉死的 format 6（warning）：%+v", n, ms[n].Flags)
		}
		if hasFlag(ms[n], "HC-3.19", "critical") {
			t.Errorf("%s 是最新的", n)
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
			t.Errorf("%s 在健康 ensemble 中被标记：%+v", m.Member, m.Flags)
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
			t.Errorf("没有 leader 应答，却把 %s 判定为落后于它", m.Member)
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
		t.Errorf("截断的 lgif 绝不能读取：%+v", b)
	}
}

func TestReadMembers_EnsemblesAreNotCompared(t *testing.T) {
	ms := byName(ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(9000000000, 9002500000), srvr: "Mode: leader", mntr: "zk_server_state\tleader\nzk_latest_snapshot_size\t3000000000", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0"},
		{name: "zk-aux", ens: "aux_zk", isro: "rw", lgif: logInfo(10, 500), srvr: "Mode: follower", mntr: "zk_server_state\tfollower\nzk_latest_snapshot_size\t1000", conf: "snapshot_transfer_chunk_size=0", dirs: "snapshot_dir_size: 0"},
	})))
	if len(ms["zk-aux"].Flags) != 0 || len(ms["keeper-a"].Flags) != 0 {
		t.Errorf("两个 ensemble 被比较了：%+v / %+v", ms["keeper-a"].Flags, ms["zk-aux"].Flags)
	}
}

func TestReadMembers_FormatPerMemberVersion(t *testing.T) {
	ms := byName(ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(1, 100), srvr: "ClickHouse Keeper version: v26.7.5.10\nMode: leader", mntr: "zk_server_state\tleader", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-b", isro: "rw", lgif: logInfo(1, 100), srvr: "ClickHouse Keeper version: v26.8.17.4\nMode: follower", mntr: "zk_server_state\tfollower", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"},
	})))
	if !hasFlag(ms["keeper-a"], "HC-3.20", "info") || hasFlag(ms["keeper-a"], "HC-3.20", "warning") || !hasFlag(ms["keeper-b"], "HC-3.20", "warning") {
		t.Errorf("format 6：26.7 上是 info（默认值），26.8 上是 warning（被钉死）：%+v / %+v", ms["keeper-a"].Flags, ms["keeper-b"].Flags)
	}
}

func TestReadMembers_NoFilesNoRows(t *testing.T) {
	if ms := ReadMembers(t.TempDir()); len(ms) != 0 {
		t.Errorf("没有 keeper/ 目录时必须没有行：%+v", ms)
	}
}

// Leaderless 列出预检脚本在 HC-3.18 下判为 critical 的 ensemble：
// 两个或更多成员自己回答 "not serving" 且没有成员在服务。
// 采集器侧的失败绝不计数。
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
		{[]Member{{Member: "a", State: "not serving"}, {Member: "b", State: "not serving"}}, 0}, // ensemble 未知
	}
	for i, c := range cases {
		if got := Leaderless(c.ms); len(got) != c.want {
			t.Errorf("case %d：Leaderless = %v，期望 %d 个 ensemble", i, got, c.want)
		}
	}
}

// 早于 ensemble 行的文件：每个成员按自身判断，
// 因此两个成员的日志绝不会被比较。
func TestReadMembers_NoEnsembleLineSkipsCrossMemberChecks(t *testing.T) {
	ms := ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", ens: "-", isro: "rw", lgif: logInfo(9000000000, 9002500000), srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-b", ens: "-", isro: "rw", lgif: logInfo(8990000000, 8999600000), srvr: "Mode: follower", mntr: "zk_server_state\tfollower", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
	}))
	for _, m := range ms {
		if hasFlag(m, "HC-3.19", "critical") {
			t.Errorf("没有 ensemble 标识，%s 却被与另一个成员的日志比较了", m.Member)
		}
	}
}

// mntr 报告构建后缀（v26.7.17.7-stable-<git hash>）；面板
// 显示版本号。
func TestReadMembers_VersionWithoutBuildSuffix(t *testing.T) {
	ms := ReadMembers(writeMemberFiles(t, []fileSpec{{name: "keeper-a", isro: "rw", lgif: logInfo(1, 2), srvr: "Mode: leader",
		mntr: "zk_version\tv26.7.17.7-stable-7abaef160cefd843cdcbbc980c8fcae258bb392c\nzk_server_state\tleader", conf: "x=1", dirs: "snapshot_dir_size: 0"}}))
	if ms[0].Version != "26.7.17.7" {
		t.Errorf("Version = %q，期望 26.7.17.7", ms[0].Version)
	}
}

func TestReadMembers_ZooKeeperNotServing(t *testing.T) {
	const zk = "This ZooKeeper instance is not currently serving requests"
	ms := ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "zk-a", isro: "rw", lgif: "(empty reply)", srvr: zk, mntr: zk, conf: zk, dirs: zk},
		{name: "zk-b", isro: "rw", lgif: "(empty reply)", srvr: zk, mntr: zk, conf: zk, dirs: zk},
	}))
	for _, m := range ms {
		if m.State != "not serving" {
			t.Errorf("%s：state %q，ZooKeeper 的措辞应判为 not serving", m.Member, m.State)
		}
	}
	if len(Leaderless(ms)) != 1 {
		t.Errorf("两个 ZooKeeper 成员 not serving 且无成员服务：Leaderless = %v", Leaderless(ms))
	}
}

func TestReadMembers_TwoLeadersNoSnapshotVerdict(t *testing.T) {
	ms := ReadMembers(writeMemberFiles(t, []fileSpec{
		{name: "keeper-a", isro: "rw", lgif: logInfo(100, 1000), srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-b", isro: "rw", lgif: logInfo(200, 1000), srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
		{name: "keeper-c", isro: "rw", lgif: logInfo(10, 150), srvr: "Mode: follower", mntr: "zk_server_state\tfollower", conf: "snapshot_transfer_chunk_size=1", dirs: "snapshot_dir_size: 0"},
	}))
	for _, m := range ms {
		if hasFlag(m, "HC-3.19", "critical") {
			t.Errorf("两个 leader：任何成员都不能被判定为落后于“该 leader”，得到 %s", m.Member)
		}
	}
}
