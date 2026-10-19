package keeper

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Member is one Keeper member as the dashboard's Keeper members panel shows
// it: what it reported about itself in keeper/<member>.txt, and the verdicts
// the pre-pass draws from the same files (HC-3.18-3.20). A nil number is a
// value the member did not report — never assumed.
type Member struct {
	Member   string `json:"member"`
	Ensemble string `json:"ensemble"` // "" when the file predates the ensemble line
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

// Flag is one verdict on a member, named after the pre-pass check that
// draws the same conclusion so the dashboard and the skill never disagree.
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

// ReadMembers reads <runDir>/keeper/*.txt; no directory, no rows.
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
	// A reply cut short by the read deadline is followed by an "error:" line;
	// its numbers are incomplete, so such a section is not evidence.
	answered := func(cmd string) bool {
		b := sec[cmd]
		if b == "" || b == notServingMsg || strings.HasPrefix(b, "skipped") || strings.HasPrefix(b, "(empty reply") {
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
		if b == notServingMsg {
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
	// mntr carries the build suffix (26.7.17.7-stable-<git hash>): keep the number.
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

// flagMembers applies the pre-pass's rules. Cross-member comparisons run
// within one known ensemble; a member without the ensemble line is judged
// on its own figures.
func flagMembers(ms []Member) {
	groups := map[string][]int{}
	for i, m := range ms {
		key := m.Ensemble
		if key == "" {
			key = "\x00" + m.Member // unknown ensemble: on its own
		}
		groups[key] = append(groups[key], i)
	}
	for key, idx := range groups {
		known := !strings.HasPrefix(key, "\x00")
		var leader *Member
		var snap int64
		for _, i := range idx {
			m := &ms[i]
			if m.Role == "leader" && m.State == "serving" {
				leader = m
			}
			if m.SnapshotSize != nil && *m.SnapshotSize > snap {
				snap = *m.SnapshotSize
			}
		}
		for _, i := range idx {
			m := &ms[i]
			switch m.State {
			case "not serving":
				m.Flags = append(m.Flags, Flag{"warning", "HC-3.18", "answers 'not currently serving requests' — no leader, or still loading its snapshot"})
			case "probe failed":
				m.Flags = append(m.Flags, Flag{"warning", "HC-3.18", "probe failed (collector timeout or refusal) — says nothing about the voters' own links"})
			}
			// Only the leader's own first_log_idx says what it retains.
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

// Leaderless names the ensembles where two or more members answer "not
// serving" themselves and none is serving — the pre-pass's HC-3.18
// critical. Collector-side failures never count, and a member without the
// ensemble line is never grouped.
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
