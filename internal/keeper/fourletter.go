// Package keeper collects what ClickHouse Keeper (or ZooKeeper) reports
// about itself through its four-letter commands — the server-side view
// that no system.* table carries. system.zookeeper_connection says which
// Keeper member this server talks to and how old the session is;
// metric_log says how long requests waited. Only Keeper itself can say
// whether it is the leader, how many requests are queued, its own latency
// and node count, and which version it runs — the facts a Keeper-latency
// or session-loss finding is settled with.
//
// Seven commands are sent, each on its own TCP connection because Keeper
// answers one four-letter word and closes. The first three answer even on a
// member that is not serving (Keeper checks isServerActive() only for the
// others), so during a quorum loss they are what still comes back:
//
//	ruok  liveness — "imok"
//	isro  "ro" for a read-only member or observer, "rw" otherwise
//	lgif  the member's Raft log: first/last index and term, committed vs
//	      leader-committed, last snapshot index — a member whose last index
//	      is below the leader's first can only rejoin through a full
//	      snapshot
//	srvr  version, latency min/avg/max, received/sent, connections,
//	      outstanding requests, zxid, mode (leader/follower/standalone),
//	      node count
//	mntr  the same as machine-readable zk_* counters plus followers,
//	      synced followers, znode / watch / ephemeral counts, memory,
//	      latest snapshot size
//	conf  the coordination settings in force: snapshot_distance,
//	      reserved_log_items, snapshot_transfer_chunk_size,
//	      write_snapshot_version … (storage paths removed in gov)
//	dirs  bytes on disk in the snapshot and log directories
//
// Every one of them is read-only and none goes through Raft or grows with
// the data size: measured at ~0.3 ms each on 26.7, under 3 KB per reply.
// Commands with side effects (csnp, rqld, ydld, rcvr, srst, crst, rclc,
// clrs) are never sent, nor stat, cons, dump, wchc or wchp, which list
// client addresses or sessions the bundle must not carry.
//
// The members are asked in parallel, and a member is asked nothing more
// after its first network failure: a member that accepts connections but
// never answers (seen in a real quorum loss) would otherwise cost the full
// read timeout once per command.
//
// The targets are the (host, port) pairs of system.zookeeper_connection —
// the members THIS server is configured with — so a three-member ensemble
// yields one file per member when the server lists all three, and one
// file when it lists only its own.
package keeper

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"clickhouse-diagnostic/internal/config"
)

// DirName is the bundle subdirectory the files land in.
const DirName = "keeper"

// Commands sent, in order: ruok first, so a member that is up but refuses
// four-letter words (four_letter_word_white_list) is told apart from one
// that is down; then the two that answer even when the member is not
// serving; then the rest.
var Commands = []string{"ruok", "isro", "lgif", "srvr", "mntr", "conf", "dirs"}

// coreCommands decide a member's status. The others add facts but are
// absent on older Keepers (lgif is not in 22.8) or may be left out of a
// custom allow-list, and that alone does not make a member partial.
var coreCommands = map[string]bool{"ruok": true, "srvr": true, "mntr": true}

// notServingMsg is Keeper's reply to srvr, mntr, conf, dirs … on a member
// that is up but not serving — no quorum, still loading its snapshot.
// Apache ZooKeeper says "This ZooKeeper instance is not currently serving
// requests"; IsNotServing recognises both.
const notServingMsg = "This instance is not currently serving requests"

var reNotServing = regexp.MustCompile(`^This (ZooKeeper )?instance is not currently serving requests$`)

// IsNotServing reports whether a four-letter reply is the not-serving
// answer of ClickHouse Keeper or Apache ZooKeeper.
func IsNotServing(reply string) bool { return reNotServing.MatchString(strings.TrimSpace(reply)) }

// Target is one Keeper member as system.zookeeper_connection names it.
// Ensemble is that connection's name — "default" for the <zookeeper> block,
// the auxiliary ZooKeeper's name otherwise — so members of different
// ensembles are never compared with each other.
type Target struct {
	Host     string
	Port     string
	Ensemble string
}

// Options controls one collection.
type Options struct {
	// GovSalt, when non-empty, hashes the host in the file name and the
	// file header exactly like the SQL collectors hash identifiers:
	// hex(SHA256(host || salt)), upper-case — so the file joins with the
	// hashed host in system.zookeeper_connection of the same bundle.
	GovSalt     string
	DialTimeout time.Duration
	ReadTimeout time.Duration
	// Now stamps the file header; nil = time.Now.
	Now func() time.Time
}

// Outcome is what happened for one target.
type Outcome struct {
	Target
	// Label is the host as written into the bundle (hashed in gov).
	Label string
	// File is the path written, "" when nothing could be collected.
	File string
	// Status: ok (ruok, srvr and mntr answered) | not serving (up, but
	// answers "not currently serving requests") | partial (some answered) |
	// refused | timeout | failed (nothing answered for another reason).
	Status   string
	Duration time.Duration
	Bytes    int64
	Err      string
}

// Resolve applies the -keeper-mntr flag to the mode: off always skips;
// on always collects (cloud gets a warning — a managed service's Keeper
// is not reachable from outside, so on only makes sense for a self-hosted
// cluster collected in cloud mode); auto collects for onprem and gov,
// where the tool runs next to the cluster, and skips for cloud. The user's
// -mode is the input, not the effective collection mode: a self-hosted
// SharedMergeTree cluster switches its queries to the cloud set but its
// Keeper is as reachable as any other on-prem Keeper.
func Resolve(setting, mode string) (skip bool, warning string, err error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "off":
		return true, "", nil
	case "on":
		if mode == "cloud" {
			return false, "Warning: -keeper-mntr=on in cloud mode — a managed service's Keeper is not " +
				"reachable from outside; this only yields files when the tool runs beside a " +
				"self-managed cluster.", nil
		}
		return false, "", nil
	case "auto", "":
		return mode == "cloud", "", nil
	default:
		return true, "", fmt.Errorf("-keeper-mntr must be auto, on or off (got %q)", setting)
	}
}

// TargetsQuery lists the member each configured Keeper connection is on
// right now; FORMAT TSV so ParseTargets can read it. system.zookeeper_connection
// has one row per connection (the default <zookeeper> block plus every
// auxiliary one), each naming the member that connection is CONNECTED to —
// not the whole ensemble. The remaining members come from the server
// configuration (TargetsFromConfig); without it only the connected member
// is probed, which the docs say.
const TargetsQuery = "SELECT DISTINCT name, host, toString(port) FROM system.zookeeper_connection WHERE host != '' ORDER BY name, host, port FORMAT TSV"

// DefaultPort is the <zookeeper><node><port> default when a node omits it.
const DefaultPort = "2181"

var (
	reZKBlock = regexp.MustCompile(`(?is)<zookeeper\b[^>]*>(.*?)</zookeeper>`)
	// <auxiliary_zookeepers> holds other ensembles, one tag per name — and a
	// name may itself be "zookeeper"; removed before the default block is read.
	reAuxZK  = regexp.MustCompile(`(?is)<auxiliary_zookeepers\b[^>]*>.*?</auxiliary_zookeepers>`)
	reZKNode = regexp.MustCompile(`(?is)<node\b[^>]*>(.*?)</node>`)
	reZKHost = regexp.MustCompile(`(?is)<host>\s*([^<]*?)\s*</host>`)
	reZKPort = regexp.MustCompile(`(?is)<port>\s*([^<]*?)\s*</port>`)
)

// TargetsFromConfig reads every <zookeeper><node> from the server
// configuration — configDir and, when configDir is a *.d directory, the
// adjacent config.xml, the same two places logfiles.LogPathsFromConfig
// scans. This is where the ENSEMBLE is declared; system.zookeeper_connection
// only names the member this server is on, so a failed follower never
// appears there. Unresolved substitutions ({…}, from_env) and empty hosts
// are skipped; a node without <port> gets DefaultPort. Nothing is returned
// when the directory is unreadable — the tool is then not on the server.
func TargetsFromConfig(configDir string) []Target {
	if configDir == "" {
		return nil
	}
	roots := []string{configDir}
	if base := filepath.Base(filepath.Clean(configDir)); strings.HasSuffix(base, ".d") {
		if parent := filepath.Dir(filepath.Clean(configDir)); parent != "." {
			roots = append(roots, parent)
		}
	}
	seen := map[Target]bool{}
	var out []Target
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := strings.ToLower(e.Name())
			if e.IsDir() || (!strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".conf")) {
				continue
			}
			blob, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			// Comments first: ClickHouse's stock config.xml carries a whole
			// <zookeeper> example inside one (example1 / example2 / example3),
			// and reading it as configuration made the tool report four
			// members on a one-member cluster and try to reach three hosts
			// the operator never named. ClickHouse's own parser ignores
			// comments; so must this.
			cfg := reAuxZK.ReplaceAllString(string(config.StripComments(blob)), "")
			for _, block := range reZKBlock.FindAllStringSubmatch(cfg, -1) {
				for _, node := range reZKNode.FindAllStringSubmatch(block[1], -1) {
					h := reZKHost.FindStringSubmatch(node[1])
					if h == nil || h[1] == "" || strings.Contains(h[1], "{") {
						continue
					}
					port := DefaultPort
					if pm := reZKPort.FindStringSubmatch(node[1]); pm != nil && pm[1] != "" && !strings.Contains(pm[1], "{") {
						port = pm[1]
					}
					// Only the <zookeeper> block is read here: the default ensemble.
					t := Target{Host: h[1], Port: port, Ensemble: "default"}
					if !seen[t] {
						seen[t] = true
						out = append(out, t)
					}
				}
			}
		}
	}
	return out
}

// MergeTargets unions the connected member(s) with the configured ensemble,
// connected first, without duplicates.
func MergeTargets(connected, configured []Target) []Target {
	seen := map[Target]bool{}
	var out []Target
	for _, list := range [][]Target{connected, configured} {
		for _, t := range list {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// ParseTargets turns the TSV of TargetsQuery into targets, dropping
// malformed lines and duplicates.
func ParseTargets(tsv string) []Target {
	var out []Target
	seen := map[Target]bool{}
	for _, line := range strings.Split(strings.TrimSpace(tsv), "\n") {
		cols := strings.Split(strings.TrimSpace(line), "\t")
		if len(cols) == 2 { // name, host, port since TargetsQuery selects the name
			cols = append([]string{"default"}, cols...)
		}
		if len(cols) != 3 || cols[1] == "" || cols[2] == "" {
			continue
		}
		if cols[0] == "" {
			cols[0] = "default"
		}
		t := Target{Host: cols[1], Port: cols[2], Ensemble: cols[0]}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// FourLetter sends one command and returns the reply. Keeper closes the
// connection after answering, so the reply is read to EOF; readTimeout
// bounds a member that accepts but never answers. A reply cut short by the
// deadline is returned TOGETHER with the timeout error, so the caller can
// keep what arrived and still record the member as timed out — a stalled
// mntr must not pass for a complete one.
func FourLetter(host, port, cmd string, dialTimeout, readTimeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), dialTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(readTimeout)); err != nil {
		return "", err
	}
	if _, err := io.WriteString(conn, cmd); err != nil {
		return "", err
	}
	var b strings.Builder
	r := bufio.NewReader(conn)
	if _, err := io.Copy(&b, r); err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			// Deadline hit: whatever arrived is partial. Hand it back with
			// the error so the file shows it marked as truncated.
			return b.String(), err
		}
		// A reply followed by a reset is still a reply.
		if b.Len() == 0 {
			return "", err
		}
	}
	return b.String(), nil
}

// reClientAddr matches an ip:port or [v6]:port token — the bracketed form
// including IPv4-mapped addresses ([::ffff:10.0.0.7]:60155) and scoped ones
// ([fe80::1%eth0]:2181). srvr and mntr do not print client addresses today;
// in gov the filter guards the file against a future Keeper that does,
// because a hashed bundle must not carry one.
var reClientAddr = regexp.MustCompile(`(\d{1,3}\.){3}\d{1,3}:\d+|\[[0-9a-fA-F:.%A-Za-z0-9_-]+\]:\d+`)

// govFilter drops any reply line carrying a client address.
func govFilter(reply string) string {
	lines := strings.Split(reply, "\n")
	out := lines[:0]
	for _, l := range lines {
		if reClientAddr.MatchString(l) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// govConf keeps, in a gov bundle, only conf values that cannot identify the
// customer's infrastructure: numbers, booleans and plain words. Paths, URLs,
// host names and addresses are removed whatever the key, and so are keys that
// name a location or a customer-chosen object even when the value looks
// plain — Keeper's *_path and *_disk, Apache ZooKeeper's dataDir, dataLogDir
// and server.N (an ensemble member's host and ports). The same treatment as
// the gov system.disks and server_settings collectors: the setting stays,
// its value says REMOVED.
var (
	reConfPlain = regexp.MustCompile(`^(-?[0-9]+(\.[0-9]+)?|true|false|[A-Za-z_][A-Za-z0-9_,*]*)$`)
	confIDKeys  = map[string]bool{"dataDir": true, "dataLogDir": true}
	reConfLabel = regexp.MustCompile(`^[A-Za-z_ ]*:?\s*$`)
)

func govConf(reply string) string {
	lines := strings.Split(reply, "\n")
	out := lines[:0]
	for _, l := range lines {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			// ZooKeeper prints section labels ("membership: "); anything else
			// without a key could be anything.
			if reConfLabel.MatchString(l) {
				out = append(out, l)
			}
			continue
		}
		k = strings.TrimSpace(k)
		if confIDKeys[k] || strings.HasSuffix(k, "_path") || strings.HasSuffix(k, "_disk") ||
			strings.HasPrefix(k, "server.") || !reConfPlain.MatchString(strings.TrimSpace(v)) {
			out = append(out, k+"=REMOVED")
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Label is the host as it appears in the bundle: the host itself, or in
// gov its salted hash in the same form the SQL collectors produce.
func Label(host, govSalt string) string {
	if govSalt == "" {
		return host
	}
	sum := sha256.Sum256([]byte(host + govSalt))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// fileName makes "<label>_<port>.txt" safe for every filesystem: a label
// is a hostname, an IP (v6 has colons) or a hash.
func fileName(label, port string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, label)
	return safe + "_" + port + ".txt"
}

// Collect probes every target and writes one file per target under
// destDir/keeper/. It never returns an error for an unreachable member —
// that is an outcome, recorded per target — only for a destination that
// cannot be written.
func Collect(destDir string, targets []Target, opts Options) ([]Outcome, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 3 * time.Second
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	dir := filepath.Join(destDir, DirName)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	// One goroutine per member, results kept in target order: the total is
	// the slowest member's time, not the sum.
	outcomes := make([]Outcome, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			outcomes[i] = collectOne(dir, t, opts)
		}(i, t)
	}
	wg.Wait()
	return outcomes, nil
}

// collectOne asks one member every command and writes its file.
func collectOne(dir string, t Target, opts Options) Outcome {
	start := time.Now()
	o := Outcome{Target: t, Label: Label(t.Host, opts.GovSalt)}
	var b strings.Builder
	fmt.Fprintf(&b, "# clickhouse-diagnostic — Keeper four-letter commands\n")
	fmt.Fprintf(&b, "# target: %s:%s\n", o.Label, t.Port)
	// Which ensemble this member belongs to; an auxiliary ZooKeeper's name is
	// configuration chosen by the customer, so gov hashes it like a host.
	ens := t.Ensemble
	if ens == "" {
		ens = "default"
	}
	if ens != "default" {
		ens = Label(ens, opts.GovSalt)
	}
	fmt.Fprintf(&b, "# ensemble: %s\n", ens)
	fmt.Fprintf(&b, "# collected_at: %s\n", opts.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "# commands: %s (stat/cons are never sent — they list client addresses)\n", strings.Join(Commands, ", "))
	coreOK, answered, refused, timedOut, notServing := 0, 0, 0, 0, 0
	var lastErr, stoppedBy string
	for _, cmd := range Commands {
		fmt.Fprintf(&b, "\n## %s\n", cmd)
		if stoppedBy != "" {
			// One network failure means the member is down, unreachable or
			// hung; asking the rest would only wait out the same timeout.
			fmt.Fprintf(&b, "skipped — %s\n", stoppedBy)
			continue
		}
		reply, err := FourLetter(t.Host, t.Port, cmd, opts.DialTimeout, opts.ReadTimeout)
		if err != nil {
			lastErr = err.Error()
			// A timeout can carry a partial reply: keep it, but mark it
			// truncated and count the command as timed out, never ok.
			truncated := ""
			if reply != "" {
				reply = filtered(cmd, reply, opts.GovSalt)
				if reply = strings.TrimRight(reply, "\n"); reply != "" {
					fmt.Fprintf(&b, "%s\n", reply)
					truncated = " — reply truncated"
				}
			}
			var ne net.Error
			switch {
			case errors.As(err, &ne) && ne.Timeout():
				timedOut++
				fmt.Fprintf(&b, "error: timeout after %s%s\n", opts.ReadTimeout, truncated)
				stoppedBy = cmd + " timed out"
			case strings.Contains(err.Error(), "connection refused"):
				refused++
				fmt.Fprintf(&b, "error: connection refused\n")
				stoppedBy = "connection refused"
			default:
				if opts.GovSalt != "" {
					fmt.Fprintf(&b, "error: (message withheld in gov mode)\n")
				} else {
					fmt.Fprintf(&b, "error: %s\n", err)
				}
				stoppedBy = cmd + " failed"
			}
			continue
		}
		reply = strings.TrimRight(filtered(cmd, reply, opts.GovSalt), "\n")
		if reply == "" {
			// Not in four_letter_word_white_list, or a command this Keeper
			// version does not have (lgif before 23.x).
			fmt.Fprintf(&b, "(empty reply — command not enabled in four_letter_word_white_list, or not known to this Keeper version)\n")
			continue
		}
		fmt.Fprintf(&b, "%s\n", reply)
		if IsNotServing(reply) {
			notServing++
			continue
		}
		answered++
		if coreCommands[cmd] {
			coreOK++
		}
	}
	o.Duration = time.Since(start)
	switch {
	case notServing > 0:
		o.Status = "not serving"
	case coreOK == len(coreCommands):
		o.Status = "ok"
	case answered > 0:
		o.Status = "partial"
	case refused > 0:
		o.Status = "refused"
	case timedOut > 0:
		o.Status = "timeout"
	default:
		o.Status = "failed"
	}
	if o.Status != "ok" {
		o.Err = lastErr
		if o.Status == "not serving" {
			o.Err = notServingMsg
		}
		if opts.GovSalt != "" && o.Err != "" {
			o.Err = o.Status
		}
	}
	// Default and auxiliary connections can share a host:port (different
	// chroots): an auxiliary member's file name carries its ensemble, so the
	// two never write the same file. A default member keeps the plain name.
	name := fileName(o.Label, t.Port)
	if ens != "default" {
		name = fileName(ens+"__"+o.Label, t.Port)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		o.Status = "failed"
		o.Err = fmt.Sprintf("write %s: %v", filepath.Base(path), err)
	} else {
		o.File = path
		o.Bytes = int64(b.Len())
	}
	return o
}

// filtered applies the gov filters to one reply: client addresses out of
// every reply, path values out of conf.
func filtered(cmd, reply, govSalt string) string {
	if govSalt == "" {
		return reply
	}
	reply = govFilter(reply)
	if cmd == "conf" {
		reply = govConf(reply)
	}
	return reply
}

// Summary is the operator-facing line per outcome.
func Summary(outcomes []Outcome) string {
	if len(outcomes) == 0 {
		return "  no Keeper members listed in system.zookeeper_connection\n"
	}
	var b strings.Builder
	for _, o := range outcomes {
		line := fmt.Sprintf("  %s:%s — %s", o.Label, o.Port, o.Status)
		if o.File != "" {
			line += fmt.Sprintf(" (%s/%s)", DirName, filepath.Base(o.File))
		}
		if o.Err != "" && o.Status != "ok" {
			line += " — " + o.Err
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}
