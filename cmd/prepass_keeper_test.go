package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pre-pass 会读取 keeper/<host>_<port>.txt——每个 Keeper 成员的
// four-letter command 应答——并将其转换为 finding。这个 fixture 是真实
// quorum 丢失的形态：一个 2.38 GB 的快照未分块发送，且一个成员落后于
// leader 保留的日志，因此只能通过该快照重新加入（P-61）。

const notServing = "This instance is not currently serving requests"

type member struct {
	name, isro, lgif, srvr, mntr, conf, dirs string
	ens                                      string // "" = 默认，"-" = 无 ensemble 行（较旧的 bundle）
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
		t.Fatalf("退出码 %d：%s", code, errOut)
	}
	var r struct {
		Findings []map[string]string `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("pre-pass 未输出 JSON：%v\n%s", err, out)
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
		// 最后重启，回来时低于 leader 的首个日志索引。
		{"keeper-c", "rw", lgif(8996000000, 8999600000, 8999600000, 8999600000, 8996000000), notServing, notServing, notServing, notServing, ""},
	})
	fs := keeperFindings(t, run)
	if !find(fs, "HC-3.20", "critical", "snapshot_transfer_chunk_size") {
		t.Errorf("对 2.2 GiB 未分块发送的快照没有 critical finding：%v", fs)
	}
	if !find(fs, "HC-3.19", "critical", "keeper-c") {
		t.Errorf("落后于 leader 保留日志的成员没有 critical finding：%v", fs)
	}
	if !find(fs, "HC-3.20", "warning", "write_snapshot_version") {
		t.Errorf("快照格式 6 没有 warning：%v", fs)
	}
	if !find(fs, "HC-3.18", "warning", "keeper-c") {
		t.Errorf("未提供服务的成员没有 warning（quorum 仍为 3 之 2）：%v", fs)
	}
	if find(fs, "HC-3.18", "critical", "") {
		t.Errorf("3 个中有 2 个在服务即满足 quorum，不是 quorum 丢失：%v", fs)
	}
}

func TestInspectBundle_KeeperQuorumLost(t *testing.T) {
	m := func(name string) member {
		return member{name, "rw", lgif(1, 100, 100, 100, 0), notServing, notServing, notServing, notServing, ""}
	}
	// 无 leader：每个成员自身都回答 "not serving"。
	fs := keeperFindings(t, writeKeeperBundle(t, []member{m("keeper-a"), m("keeper-b"), m("keeper-c")}))
	if !find(fs, "HC-3.18", "critical", "本 bundle 中没有任何成员正在服务") {
		t.Errorf("所有成员都回答 'not serving' 应为 critical：%v", fs)
	}
	// 有一个成员在服务时，这些文件无法证明另外两个就是其余投票者：
	// 应给 warning，而不是断言故障。
	healthy := member{"keeper-a", "rw", lgif(1, 100, 100, 100, 0), "Mode: follower", "zk_server_state\tfollower", "snapshot_transfer_chunk_size=0", "snapshot_dir_size: 0", ""}
	fs = keeperFindings(t, writeKeeperBundle(t, []member{healthy, m("keeper-b"), m("keeper-c")}))
	if find(fs, "HC-3.18", "critical", "") || !find(fs, "HC-3.18", "warning", "keeper-b") {
		t.Errorf("1 个在服务 + 2 个不在服务：应为 warning，而不是 critical：%v", fs)
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
		t.Errorf("健康的 ensemble 却报告了：%v", fs)
	}
}

func TestInspectBundle_OlderKeeperWithoutNewCommands(t *testing.T) {
	// 这些命令之前的 bundle，或 22.8 的 Keeper：没有 lgif / conf / dirs。
	empty := "(empty reply — command not enabled in four_letter_word_white_list, or not known to this Keeper version)"
	ms := []member{{"keeper-a", empty, empty, "Mode: leader", "zk_server_state\tleader\nzk_latest_snapshot_size\t2400000000", empty, empty, ""}}
	if fs := keeperFindings(t, writeKeeperBundle(t, ms)); len(fs) != 0 {
		t.Errorf("没有 conf 时 chunk 大小未知——任何 finding 都不得断言它：%v", fs)
	}
}

func TestInspectBundle_KeeperSnapshotFormatDefaultBefore268(t *testing.T) {
	// 26.7 默认写入格式 6：值得知道，但不是 warning。
	m := member{"keeper-a", "rw", lgif(1, 100, 100, 100, 0), "ClickHouse Keeper version: v26.7.5.10\nMode: leader",
		"zk_version\tv26.7.5.10\nzk_server_state\tleader", "snapshot_transfer_chunk_size=0\nwrite_snapshot_version=6", "snapshot_dir_size: 0", ""}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{m}))
	if !find(fs, "HC-3.20", "info", "26.7 上的默认值") || find(fs, "HC-3.20", "warning", "write_snapshot_version") {
		t.Errorf("26.7 上的格式 6 是默认值：应为 info，而不是 warning：%v", fs)
	}
}

// ---- 评审轮次：这些文件能证明什么、不能证明什么

const truncated = "\nerror: timeout after 10s — reply truncated"

func TestInspectBundle_KeeperTruncatedReplyIsNotEvidence(t *testing.T) {
	// 被截断的 lgif：last_log_idx 只收到了更长数字中的 "8"。
	leader := member{name: "keeper-a", isro: "rw", lgif: lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000),
		srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0"}
	cut := member{name: "keeper-b", isro: "rw", lgif: "first_log_idx\t9000000000\nlast_log_idx\t8" + truncated,
		srvr: "skipped — lgif timed out", mntr: "skipped — lgif timed out", conf: "skipped — lgif timed out", dirs: "skipped — lgif timed out"}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{leader, cut}))
	if find(fs, "HC-3.19", "critical", "keeper-b") {
		t.Errorf("被截断的 lgif 产生了“落后于日志”的 critical：%v", fs)
	}
	if !find(fs, "HC-3.18", "warning", "keeper-b") {
		t.Errorf("探测失败的成员仍必须被报告：%v", fs)
	}
}

func TestInspectBundle_KeeperUnreachableIsNeverAQuorumLoss(t *testing.T) {
	// 远程运行只探测了已连接的成员，而它超时了。
	m := member{name: "keeper-a", isro: "error: timeout after 10s", lgif: "skipped — isro timed out", srvr: "skipped — isro timed out",
		mntr: "skipped — isro timed out", conf: "skipped — isro timed out", dirs: "skipped — isro timed out"}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{m}))
	if find(fs, "HC-3.18", "critical", "") || !find(fs, "HC-3.18", "warning", "keeper-a") {
		t.Errorf("collector 侧超时是 warning，绝不是 quorum 丢失：%v", fs)
	}
}

func TestInspectBundle_KeeperEnsemblesAreNotCompared(t *testing.T) {
	// 默认 ensemble 是健康的，快照很大且已分块；一个辅助 ZooKeeper
	// 有自己的小日志，chunk 大小为 0。
	def := member{name: "keeper-a", isro: "rw", lgif: lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000),
		srvr: "Mode: leader", mntr: "zk_server_state\tleader\nzk_latest_snapshot_size\t3000000000",
		conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0"}
	aux := member{name: "zk-aux", isro: "rw", lgif: lgif(10, 500, 500, 500, 0), srvr: "Mode: follower",
		mntr: "zk_server_state\tfollower\nzk_latest_snapshot_size\t1000", conf: "snapshot_transfer_chunk_size=0", dirs: "snapshot_dir_size: 0", ens: "aux_zk"}
	if fs := keeperFindings(t, writeKeeperBundle(t, []member{def, aux})); len(fs) != 0 {
		t.Errorf("两个 ensemble 被相互比较了：%v", fs)
	}
}

func TestInspectBundle_KeeperFormatJudgedPerMember(t *testing.T) {
	// 滚动升级：一个成员在 26.7（6 是它的默认值），另一个在 26.8
	// 但仍写入 6（固定低于 8）。
	old := member{name: "keeper-a", isro: "rw", lgif: lgif(1, 100, 100, 100, 0), srvr: "ClickHouse Keeper version: v26.7.5.10\nMode: leader",
		mntr: "zk_server_state\tleader", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"}
	upgraded := member{name: "keeper-b", isro: "rw", lgif: lgif(1, 100, 100, 100, 0), srvr: "ClickHouse Keeper version: v26.8.17.4\nMode: follower",
		mntr: "zk_server_state\tfollower", conf: "write_snapshot_version=6", dirs: "snapshot_dir_size: 0"}
	fs := keeperFindings(t, writeKeeperBundle(t, []member{old, upgraded}))
	if !find(fs, "HC-3.20", "warning", "keeper-b") || find(fs, "HC-3.20", "warning", "keeper-a") {
		t.Errorf("26.8 成员固定使用的格式必须告警，26.7 的默认值不得告警：%v", fs)
	}
}

func TestInspectBundle_KeeperNoEnsembleLineSkipsCrossMemberChecks(t *testing.T) {
	leader := member{name: "keeper-a", isro: "rw", lgif: lgif(9000000000, 9002500000, 9002500000, 9002500000, 9002000000),
		srvr: "Mode: leader", mntr: "zk_server_state\tleader", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0", ens: "-"}
	behind := member{name: "keeper-b", isro: "rw", lgif: lgif(8990000000, 8999600000, 8999600000, 8999600000, 8990000000),
		srvr: "Mode: follower", mntr: "zk_server_state\tfollower", conf: "snapshot_transfer_chunk_size=100000000", dirs: "snapshot_dir_size: 0", ens: "-"}
	if fs := keeperFindings(t, writeKeeperBundle(t, []member{leader, behind})); find(fs, "HC-3.19", "critical", "") {
		t.Errorf("没有 ensemble 身份时不得比较两个成员的日志：%v", fs)
	}
}

// follower 会独立压缩自己的日志：在未观察到 leader 时，
// 不能宣布任何成员落后于 leader 所保留的内容。
func TestInspectBundle_KeeperNoLeaderNoSnapshotVerdict(t *testing.T) {
	a := member{"keeper-a", "rw", lgif(1000, 2000, 2000, 2000, 900), "Mode: follower", "zk_server_state\tfollower", "snapshot_transfer_chunk_size=100000000", "snapshot_dir_size: 0", ""}
	b := member{"keeper-b", "rw", lgif(1, 500, 500, 500, 0), "Mode: follower", "zk_server_state\tfollower", "snapshot_transfer_chunk_size=100000000", "snapshot_dir_size: 0", ""}
	if fs := keeperFindings(t, writeKeeperBundle(t, []member{a, b})); find(fs, "HC-3.19", "critical", "") {
		t.Errorf("没有 leader 应答，却宣布某成员落后于 leader 的日志：%v", fs)
	}
}

// Apache ZooKeeper 对 "not serving" 的措辞与 Keeper 一样会被计入。
func TestInspectBundle_ZooKeeperNotServing(t *testing.T) {
	const zk = "This ZooKeeper instance is not currently serving requests"
	m := func(name string) member { return member{name, "rw", "(empty reply — not known)", zk, zk, zk, zk, ""} }
	fs := keeperFindings(t, writeKeeperBundle(t, []member{m("zk-a"), m("zk-b"), m("zk-c")}))
	if !find(fs, "HC-3.18", "critical", "本 bundle 中没有任何成员正在服务") {
		t.Errorf("三个 ZooKeeper 成员都未提供服务必须判定为无成员在服务：%v", fs)
	}
}

// 选举中途时，bundle 里可能有两个成员都报告 leader。这样就没有
// 任何一方能说明“leader”保留了哪些内容，因此不能宣布任何成员落后。
func TestInspectBundle_KeeperTwoLeadersNoSnapshotVerdict(t *testing.T) {
	a := member{"keeper-a", "rw", lgif(200, 1000, 1000, 1000, 150), "Mode: leader", "zk_server_state\tleader", "snapshot_transfer_chunk_size=1", "snapshot_dir_size: 0", ""}
	b := member{"keeper-b", "rw", lgif(100, 1000, 1000, 1000, 50), "Mode: leader", "zk_server_state\tleader", "snapshot_transfer_chunk_size=1", "snapshot_dir_size: 0", ""}
	c := member{"keeper-c", "rw", lgif(10, 150, 150, 150, 0), "Mode: follower", "zk_server_state\tfollower", "snapshot_transfer_chunk_size=1", "snapshot_dir_size: 0", ""}
	if fs := keeperFindings(t, writeKeeperBundle(t, []member{a, b, c})); find(fs, "HC-3.19", "critical", "") {
		t.Errorf("两个 leader：不应有快照判定，实际得到 %v", fs)
	}
}
