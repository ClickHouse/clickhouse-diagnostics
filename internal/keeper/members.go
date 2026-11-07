package keeper

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Member 是 dashboard 的 Keeper members 面板所展示的一个 Keeper 成员：
// 它在 keeper/<member>.txt 中报告的自身信息，以及预检脚本从
// 相同文件得出的判定（HC-3.18-3.20）。nil 数字表示
// 成员未报告该值——绝不臆测。
type Member struct {
	Member   string `json:"member"`
	Ensemble string `json:"ensemble"` // 文件早于 ensemble 行时为 ""
	State    string `json:"state"`    // serving | not serving | probe failed | unknown
	Role     string `json:"role,omitempty"`
	Version  string `json:"version,omitempty"`

	FirstLogIdx     *int64 `json:"first_log_idx,omitempty"`
	LastLogIdx      *int64 `json:"last_log_idx,omitempty"`
	LastCommitted   *int64 `json:"last_committed_log_idx,omitempty"`
	LeaderCommitted *int64 `json:"leader_committed_log_idx,omitempty"`
	LastSnapshotIdx *int64 `json:"last_snapshot_idx,omitempty"`

	SnapshotSize *int64 `json:"snapshot_size,omitempty"`
	ZnodeCount   *int64 `json:"znode_count,omitempty"`
	ChunkSize    *int64 `json:"snapshot_transfer_chunk_size,omitempty"`
	Format       *int64 `json:"write_snapshot_version,omitempty"`
	SnapDirSize  *int64 `json:"snapshot_dir_size,omitempty"`
	LogDirSize   *int64 `json:"log_dir_size,omitempty"`

	Flags []Flag `json:"flags,omitempty"`

	versionMM [2]int
}

// Flag 是对某个成员的一项判定，以得出相同结论的预检检查命名，
// 以便 dashboard 和 skill 永不失和。
type Flag struct {
	Severity string `json:"severity"` // critical | warning | info
	Check    string `json:"check"`
	Text     string `json:"text"`
}

const int32Max = 1<<31 - 1

var (
	reTarget   = regexp.MustCompile(`(?m)^# target: (\S+)`)
	reEnsemble = regexp.MustCompile(`(?m)^# ensemble: (\S+)`)
	reMode     = regexp.MustCompile(`(?m)^Mode: (\S+)`)
	reSrvrVer  = regexp.MustCompile(`(?m)^ClickHouse Keeper version: v?(\d+\.\d+[\w.]*)`)
	reVerMM    = regexp.MustCompile(`^v?(\d+)\.(\d+)`)
	reVerNum   = regexp.MustCompile(`^v?(\d+(?:\.\d+)+)`)
)

// ReadMembers 读取 <runDir>/keeper/*.txt；没有目录就没有行。
func ReadMembers(runDir string) []Member {
	paths, _ := filepath.Glob(filepath.Join(runDir, DirName, "*.txt"))
	sort.Strings(paths)
	var ms []Member
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil {
			ms = append(ms, parseMember(string(b), filepath.Base(p)))
		}
	}
	flagMembers(ms)
	return ms
}

func parseMember(text, fallback string) Member {
	m := Member{Member: fallback, State: "unknown"}
	if t := reTarget.FindStringSubmatch(text); t != nil {
		m.Member = t[1]
	}
	if e := reEnsemble.FindStringSubmatch(text); e != nil {
		m.Ensemble = e[1]
	}
	sec := map[string]string{}
	for _, part := range strings.Split("\n"+text, "\n## ")[1:] {
		name, body, _ := strings.Cut(part, "\n")
		sec[strings.TrimSpace(name)] = strings.TrimSpace(body)
	}
	// 被读取截止时间截短的回复后面会跟一行 "error:"；
	// 其数字不完整，因此这样的分区不构成证据。
	answered := func(cmd string) bool {
		b := sec[cmd]
		if b == "" || IsNotServing(b) || strings.HasPrefix(b, "skipped") || strings.HasPrefix(b, "(empty reply") {
			return false
		}
		for _, l := range strings.Split(b, "\n") {
			if strings.HasPrefix(l, "error:") {
				return false
			}
		}
		return true
	}
	kv := map[string]string{}
	for _, c := range []struct{ cmd, sep string }{{"lgif", "\t"}, {"mntr", "\t"}, {"conf", "="}, {"dirs", ":"}} {
		if !answered(c.cmd) {
			continue
		}
		for _, l := range strings.Split(sec[c.cmd], "\n") {
			if k, v, ok := strings.Cut(l, c.sep); ok {
				kv[c.cmd+"."+strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	notServing, errLine := false, false
	for _, b := range sec {
		if IsNotServing(b) {
			notServing = true
		}
		for _, l := range strings.Split(b, "\n") {
			if strings.HasPrefix(l, "error:") {
				errLine = true
			}
		}
	}
	switch {
	case notServing:
		m.State = "not serving"
	case answered("srvr") || answered("mntr"):
		m.State = "serving"
	case errLine:
		m.State = "probe failed"
	}
	m.Role = kv["mntr.zk_server_state"]
	if m.Role == "" && answered("srvr") {
		if r := reMode.FindStringSubmatch(sec["srvr"]); r != nil {
			m.Role = r[1]
		}
	}
	m.Version = strings.TrimPrefix(kv["mntr.zk_version"], "v")
	if m.Version == "" && answered("srvr") {
		if v := reSrvrVer.FindStringSubmatch(sec["srvr"]); v != nil {
			m.Version = v[1]
		}
	}
	// mntr 携带构建后缀（26.7.17.7-stable-<git hash>）：只保留数字。
	if v := reVerNum.FindStringSubmatch(m.Version); v != nil {
		m.Version = v[1]
	}
	if v := reVerMM.FindStringSubmatch(m.Version); v != nil {
		maj, _ := strconv.Atoi(v[1])
		min, _ := strconv.Atoi(v[2])
		m.versionMM = [2]int{maj, min}
	}
	num := func(k string) *int64 {
		if n, err := strconv.ParseInt(kv[k], 10, 64); err == nil {
			return &n
		}
		return nil
	}
	m.FirstLogIdx, m.LastLogIdx = num("lgif.first_log_idx"), num("lgif.last_log_idx")
	m.LastCommitted, m.LeaderCommitted = num("lgif.last_committed_log_idx"), num("lgif.leader_committed_log_idx")
	m.LastSnapshotIdx = num("lgif.last_snapshot_idx")
	m.SnapshotSize, m.ZnodeCount = num("mntr.zk_latest_snapshot_size"), num("mntr.zk_znode_count")
	m.ChunkSize, m.Format = num("conf.snapshot_transfer_chunk_size"), num("conf.write_snapshot_version")
	m.SnapDirSize, m.LogDirSize = num("dirs.snapshot_dir_size"), num("dirs.log_dir_size")
	return m
}

// flagMembers 应用预检脚本的规则。跨成员比较在一个已知
// ensemble 内进行；没有 ensemble 行的成员按自身数据判断。
func flagMembers(ms []Member) {
	groups := map[string][]int{}
	for i, m := range ms {
		key := m.Ensemble
		if key == "" {
			key = "\x00" + m.Member // 未知 ensemble：单独判断
		}
		groups[key] = append(groups[key], i)
	}
	for key, idx := range groups {
		known := !strings.HasPrefix(key, "\x00")
		var leader *Member
		var snap int64
		leaders := 0
		for _, i := range idx {
			m := &ms[i]
			if m.Role == "leader" && m.State == "serving" {
				leader = m
				leaders++
			}
			if m.SnapshotSize != nil && *m.SnapshotSize > snap {
				snap = *m.SnapshotSize
			}
		}
		// 恰好一个：选举期间两个成员都可能报告 leader，此时
		// 谁也说不清“该 leader”保留了什么。
		if leaders != 1 {
			leader = nil
		}
		for _, i := range idx {
			m := &ms[i]
			switch m.State {
			case "not serving":
				m.Flags = append(m.Flags, Flag{"warning", "HC-3.18", "answers 'not currently serving requests' — no leader, or still loading its snapshot"})
			case "probe failed":
				m.Flags = append(m.Flags, Flag{"warning", "HC-3.18", "probe failed (collector timeout or refusal) — says nothing about the voters' own links"})
			}
			// 只有 leader 自己的 first_log_idx 能说明它保留了什么。
			if known && leader != nil && m != leader && leader.FirstLogIdx != nil && m.LastLogIdx != nil && *m.LastLogIdx < *leader.FirstLogIdx {
				m.Flags = append(m.Flags, Flag{"critical", "HC-3.19/P-61", "behind the leader's retained log — can rejoin only through a full snapshot"})
			}
			if m.ChunkSize != nil && *m.ChunkSize == 0 && snap > 1<<30 {
				sev, text := "warning", "snapshot over 1 GiB sent unchunked (snapshot_transfer_chunk_size = 0) — fails past 2 GiB"
				if snap > int32Max {
					sev, text = "critical", "snapshot over 2 GiB sent unchunked (snapshot_transfer_chunk_size = 0) — the transfer to a lagging member fails"
				}
				m.Flags = append(m.Flags, Flag{sev, "HC-3.20/P-61", text})
			}
			if m.Format != nil && *m.Format < 7 {
				switch v := m.versionMM; {
				case v[0] > 26 || (v[0] == 26 && v[1] >= 8):
					m.Flags = append(m.Flags, Flag{"warning", "HC-3.20", "snapshot format below 7 pinned below the 26.8 default of 8 — 32-bit sequential counters"})
				case v[0] > 0:
					m.Flags = append(m.Flags, Flag{"info", "HC-3.20", "snapshot format below 7, the default before 26.8 — 32-bit sequential counters"})
				default:
					m.Flags = append(m.Flags, Flag{"info", "HC-3.20", "snapshot format below 7 — Keeper version not reported"})
				}
			}
		}
	}
}

// Leaderless 列出有两个或更多成员自己回答 "not
// serving" 且没有成员在服务的 ensemble——即预检脚本的 HC-3.18
// critical。这是关于 bundle 中成员的陈述，不是整个 ensemble
// 宕机的证明：本服务器不认识的投票成员可能仍持有
// quorum，面板的横幅会说明这一点。采集器侧的失败绝不
// 计入，没有 ensemble 行的成员绝不分组。
func Leaderless(ms []Member) []string {
	type tally struct{ down, serving int }
	byEns := map[string]*tally{}
	var order []string
	for _, m := range ms {
		if m.Ensemble == "" {
			continue
		}
		t, ok := byEns[m.Ensemble]
		if !ok {
			t = &tally{}
			byEns[m.Ensemble] = t
			order = append(order, m.Ensemble)
		}
		switch m.State {
		case "not serving":
			t.down++
		case "serving":
			t.serving++
		}
	}
	var out []string
	for _, e := range order {
		if t := byEns[e]; t.down >= 2 && t.serving == 0 {
			out = append(out, e)
		}
	}
	return out
}
