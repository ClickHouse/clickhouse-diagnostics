// Package clustercollect implements `clickhouse-diagnostic cluster`: it asks
// one node which hosts the cluster has (system.clusters), prints the
// clickhouse-diagnostic run for each, and — after confirmation — runs them
// one host at a time, each as its own process of this same binary. The
// collection itself is untouched: every per-host run is exactly what an
// operator would type by hand.
package clustercollect

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"clickhouse-diagnostic/pkg"
)

// PasswordEnvVar carries the password to every per-host run. The cluster
// command takes no -password flag, so the password never sits on a command
// line where ps would show it.
const PasswordEnvVar = "CLICKHOUSE_PASSWORD"

// portalCapBytes is the support portal's upload limit; each bundle is
// uploaded on its own.
const portalCapBytes = 200 << 20

// DiscoveryQuery lists every host the node knows about.
const DiscoveryQuery = "SELECT cluster, shard_num, replica_num, host_name, host_address, " +
	"is_local, errors_count FROM system.clusters FORMAT JSONEachRow"

// executable, hostname and interfaceAddrs are the OS lookups, replaceable
// in tests.
var (
	executable     = os.Executable
	hostname       = os.Hostname
	interfaceAddrs = net.InterfaceAddrs
)

// ownedFlags are set by the cluster command for every run, so they cannot be
// passed through after --: each one carries a per-host safeguard (-host and
// -output-dir keep the bundles apart, -single-node stops a SharedMergeTree
// fan-out on every run, -host-info/-logs/-skip-config keep this machine's
// files out of remote bundles), the password travels only in the
// environment, and -skip-archive / -dry-run would leave no archive to find.
var ownedFlags = map[string]bool{
	"password": true, "host": true, "port": true, "protocol": true, "user": true, "mode": true,
	"single-node": true, "output-dir": true, "host-info": true, "logs": true, "skip-config": true,
	"skip-archive": true, "dry-run": true,
}

// validateExtra refuses pass-through arguments naming a flag the command
// owns, in any of Go's spellings (-f, --f, -f=v, --f=v).
func validateExtra(extra []string) error {
	for _, a := range extra {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		if !ownedFlags[name] {
			continue
		}
		if name == "password" {
			return fmt.Errorf("-- -password cannot be passed through: set %s instead (the cluster command never puts the password on a command line)", PasswordEnvVar)
		}
		return fmt.Errorf("-- -%s cannot be passed through: the cluster command sets it for every host", name)
	}
	return nil
}

// Row is one system.clusters row.
type Row struct {
	Cluster     string `json:"cluster"`
	ShardNum    int    `json:"shard_num"`
	ReplicaNum  int    `json:"replica_num"`
	HostName    string `json:"host_name"`
	HostAddress string `json:"host_address"`
	IsLocal     int    `json:"is_local"`
	ErrorsCount int    `json:"errors_count"`
}

// Node is one machine to collect from.
type Node struct {
	Host, Address  string
	Shard, Replica int
	Local          bool // the node the cluster was discovered from
	Errors         int  // system.clusters errors_count: the node had trouble reaching it
	MultiShard     bool // labelled from a cluster with more than one shard
}

// ParseRows reads the JSONEachRow answer to DiscoveryQuery.
func ParseRows(body string) ([]Row, error) {
	var rows []Row
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Row
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("unexpected system.clusters row %q: %w", line, err)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func isLoopback(address string) bool {
	if ip := net.ParseIP(address); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(address, "localhost")
}

// Discover turns system.clusters rows into one Node per machine.
//
//   - Every is_local = 1 row is the node that was asked, whatever address it
//     uses: a stock config's `default` cluster lists it as 127.0.0.1 next to
//     its real address in the real cluster.
//   - Other loopback rows are test clusters pointing back at that server.
//   - A machine appears once per cluster it belongs to. It is kept once,
//     labelled from the cluster with the most shards, the one that carries
//     the topology. A cluster named `default` is never preferred by name —
//     on a stock install it is this one server.
//
// cluster, when not empty, keeps only that cluster; an unknown name is an
// error that lists the names that do exist.
func Discover(rows []Row, cluster string) ([]Node, error) {
	if cluster != "" {
		var kept []Row
		names := map[string]bool{}
		for _, r := range rows {
			names[r.Cluster] = true
			if r.Cluster == cluster {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("cluster %q not found in system.clusters; available: %s", cluster, strings.Join(sortedKeys(names), ", "))
		}
		rows = kept
	}

	shards := map[string]map[int]bool{}
	for _, r := range rows {
		if shards[r.Cluster] == nil {
			shards[r.Cluster] = map[int]bool{}
		}
		shards[r.Cluster][r.ShardNum] = true
	}
	type ranked struct {
		Node
		shards     int
		notDefault bool
	}
	better := func(a, b ranked) bool { // a outranks b
		if a.shards != b.shards {
			return a.shards > b.shards
		}
		return a.notDefault && !b.notDefault
	}

	nodes := map[string]*ranked{}
	for _, r := range rows {
		local := r.IsLocal == 1
		if !local && isLoopback(r.HostAddress) {
			continue
		}
		key := r.HostAddress
		if local {
			key = "\x00local"
		}
		cand := ranked{
			Node: Node{Host: r.HostName, Address: r.HostAddress, Shard: r.ShardNum, Replica: r.ReplicaNum,
				Local: local, Errors: r.ErrorsCount, MultiShard: len(shards[r.Cluster]) > 1},
			shards: len(shards[r.Cluster]), notDefault: r.Cluster != "default",
		}
		cur, seen := nodes[key]
		if !seen {
			nodes[key] = &cand
			continue
		}
		errs := max(cur.Errors, cand.Errors)
		if better(cand, *cur) {
			*cur = cand
		}
		cur.Errors = errs
	}

	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Node)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Shard != out[j].Shard {
			return out[i].Shard < out[j].Shard
		}
		if out[i].Replica != out[j].Replica {
			return out[i].Replica < out[j].Replica
		}
		return out[i].Host < out[j].Host
	})
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var unsafePath = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Label is the folder name for one host: s<shard>r<replica>_<host>, safe as
// a single path segment.
func Label(n Node) string {
	host := unsafePath.ReplaceAllString(n.Host, "_")
	if host == "" {
		host = "host"
	}
	return fmt.Sprintf("s%dr%d_%s", n.Shard, n.Replica, host)
}

// Options are the settings shared by every per-host run.
type Options struct {
	Mode, Protocol, Host, Port, User string
	OutputDir                        string
	Extra                            []string // passed to every run
	LocalFiles                       bool     // this machine is the discovery node
}

// HereArgs is the clickhouse-diagnostic run, from this machine, for one node.
func HereArgs(n Node, o Options) []string {
	host := n.Host
	if n.Local {
		host = o.Host
	}
	args := []string{"-mode", o.Mode, "-protocol", o.Protocol, "-host", host, "-port", o.Port,
		"-user", o.User, "-single-node", "-output-dir", filepath.Join(o.OutputDir, Label(n))}
	if !(n.Local && o.LocalFiles) {
		// Host facts, logs and configs are read from the disk of the machine
		// running the binary: for any other node they would describe THIS
		// machine under that node's name.
		args = append(args, "-host-info", "off", "-logs", "off", "-skip-config")
	}
	return append(args, o.Extra...)
}

// EachNodeArgs is the clickhouse-diagnostic run to execute ON the node
// itself, which also collects its log files and configs. It carries no
// password: the binary reads CLICKHOUSE_PASSWORD or asks for it there.
//
// Over https it connects by the node's name rather than localhost: the
// server's certificate is normally issued for that name.
func EachNodeArgs(n Node, o Options) []string {
	host := "localhost"
	if o.Protocol == "https" {
		host = n.Host
		if n.Local {
			host = o.Host
		}
	}
	args := []string{"-mode", o.Mode, "-protocol", o.Protocol, "-host", host, "-port", o.Port,
		"-user", o.User, "-single-node", "-output-dir", "./clickhouse_results/" + Label(n)}
	return append(args, withoutSalt(o.Extra)...)
}

// withoutSalt drops -salt and its value: a command meant to be read, copied
// and run on another host must not carry it. Run without -salt, the binary
// asks for it there, without echo.
func withoutSalt(extra []string) []string {
	var out []string
	for i := 0; i < len(extra); i++ {
		if name, attached := flagName(extra[i]); name == "salt" {
			if !attached {
				i++ // its value
			}
			continue
		}
		out = append(out, extra[i])
	}
	return out
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// flagName is the name in a -f, --f, -f=v or --f=v argument, with whether
// the value is attached; "" for an argument that is not a flag.
func flagName(a string) (name string, attached bool) {
	if !strings.HasPrefix(a, "-") {
		return "", false
	}
	name = strings.TrimLeft(a, "-")
	if i := strings.IndexByte(name, '='); i >= 0 {
		return name[:i], true
	}
	return name, false
}

// secret flags are masked wherever a command is printed. The salt is as
// local as the password: the binary reads it without echo so it stays out of
// scrollback, recordings and ticket attachments.
var secret = map[string]bool{"password": true, "salt": true}

// Shown renders a command for printing, shell-quoted, with every -password
// and -salt value masked, in any spelling.
func Shown(binary string, args []string) string {
	parts := []string{binary}
	for i, a := range args {
		if name, attached := flagName(a); secret[name] && attached {
			a = a[:strings.IndexByte(a, '=')+1] + "****"
		} else if i > 0 {
			if prev, attached := flagName(args[i-1]); secret[prev] && !attached {
				a = "****"
			}
		}
		if !shellSafe.MatchString(a) {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// archivesIn maps each clickhouse_backup_*.tar.gz in dir to its mtime.
// The directory is listed literally rather than globbed: -output-dir may
// hold pattern characters ('support[123]'), which filepath.Glob would read
// as a pattern and so find nothing.
func archivesIn(dir string) map[string]time.Time {
	seen := map[string]time.Time{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "clickhouse_backup_") || !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		if fi, err := e.Info(); err == nil {
			seen[filepath.Join(dir, name)] = fi.ModTime()
		}
	}
	return seen
}

// verifyArchive reads the archive to its end: a cut-off or corrupt file —
// a full disk, a killed process — fails here instead of reaching the
// summary as ok.
func verifyArchive(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	entries := 0
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
		entries++
	}
	if entries == 0 {
		return errors.New("archive is empty")
	}
	return gz.Close()
}

// newArchive is the newest archive in dir that is not in before, or whose
// mtime changed since; "" when the run produced none.
func newArchive(dir string, before map[string]time.Time) string {
	newest := ""
	for path, mtime := range archivesIn(dir) {
		if old, existed := before[path]; existed && old.Equal(mtime) {
			continue
		}
		if newest == "" || path > newest {
			newest = path
		}
	}
	return newest
}

// isThisMachine says whether host — the discovery node as the operator named
// it — is the machine running the command, so its files may be collected.
// An address is compared with this machine's interfaces. Names follow
// cmd/node_identity.go's sameMachine: two qualified names must be equal
// outright ("ch-01.site-a" and "ch-01.site-b" are two machines); only when one
// side is bare does the first label decide.
func isThisMachine(host string) bool {
	if isLoopback(host) {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		addrs, err := interfaceAddrs()
		if err != nil {
			return false
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return true
			}
		}
		return false
	}
	name, err := hostname()
	if err != nil {
		return false
	}
	h, n := strings.ToLower(strings.TrimSpace(host)), strings.ToLower(strings.TrimSpace(name))
	if h == n {
		return true
	}
	if strings.Contains(h, ".") && strings.Contains(n, ".") {
		return false
	}
	return strings.SplitN(h, ".", 2)[0] == strings.SplitN(n, ".", 2)[0]
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

const usage = `Usage: clickhouse-diagnostic cluster [flags] [-- flags for every run]

Runs clickhouse-diagnostic on every host of a self-managed cluster. Needs
%s in the environment (the password is never taken as a flag).

  -collect-from here       run every collection from this machine (default).
                           Other hosts: system tables only, incl. text_log and
                           query_log; no server log files, configs or host facts.
  -collect-from each-node  only print the command to run ON each host, which
                           also collects its log files and configs.

Flags:
`

type result struct {
	node    Node
	status  string
	elapsed time.Duration
	archive string
	size    int64
}

// Main runs the cluster command and returns the process exit code:
// 0 all hosts collected (or nothing run), 1 some host failed, 2 usage or
// discovery error.
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cluster", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, usage, PasswordEnvVar)
		fs.PrintDefaults()
	}
	host := fs.String("host", "localhost", "node to discover the cluster from")
	port := fs.String("port", "", "HTTP port, the same on every host (default 8123, 8443 with https)")
	protocol := fs.String("protocol", "http", "http or https")
	user := fs.String("user", "default", "ClickHouse user")
	cluster := fs.String("cluster", "", "only this cluster from system.clusters (default: every host listed)")
	collectFrom := fs.String("collect-from", "here", "here | each-node")
	mode := fs.String("mode", "onprem", "clickhouse-diagnostic -mode: onprem | gov (gov needs -- -salt <salt>)")
	outputDir := fs.String("output-dir", "", "where the bundles go (default ./cluster_bundles_<timestamp>)")
	timeout := fs.Duration("timeout", 30*time.Minute, "time allowed per host")
	yes := fs.Bool("yes", false, "run without asking for confirmation")
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if err := validateExtra(fs.Args()); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *collectFrom != "here" && *collectFrom != "each-node" {
		fmt.Fprintf(stderr, "-collect-from must be here or each-node, not %q\n", *collectFrom)
		return 2
	}
	if *mode != "onprem" && *mode != "gov" {
		fmt.Fprintf(stderr, "-mode must be onprem or gov (cloud already collects every replica in one run)\n")
		return 2
	}
	if *protocol != "http" && *protocol != "https" {
		fmt.Fprintf(stderr, "-protocol must be http or https\n")
		return 2
	}
	if *port == "" {
		*port = "8123"
		if *protocol == "https" {
			*port = "8443"
		}
	}
	password, ok := os.LookupEnv(PasswordEnvVar)
	if !ok {
		fmt.Fprintf(stderr, "Set the password first:  export %s='…'\n"+
			"(the cluster command never takes it as a flag, so it is not visible in ps)\n", PasswordEnvVar)
		return 2
	}

	client := pkg.NewClickHouseClient(*protocol, *host, *port, *user, password)
	body, err := client.ExecuteQueryReal(DiscoveryQuery)
	if err != nil {
		fmt.Fprintf(stderr, "Cannot read system.clusters from %s:%s — %v\n", *host, *port, err)
		return 2
	}
	rows, err := ParseRows(body)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	nodes, err := Discover(rows, *cluster)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(nodes) == 0 {
		fmt.Fprintln(stderr, "system.clusters lists no hosts.")
		return 2
	}

	clusters := map[string]bool{}
	for _, r := range rows {
		if *cluster == "" || r.Cluster == *cluster {
			clusters[r.Cluster] = true
		}
	}
	fmt.Fprintf(stdout, "Discovered %d host(s) from %s (clusters: %s)\n\n", len(nodes), *host, strings.Join(sortedKeys(clusters), ", "))
	for _, n := range nodes {
		where := fmt.Sprintf("replica %d", n.Replica)
		if n.MultiShard {
			where = fmt.Sprintf("shard %d replica %d", n.Shard, n.Replica)
		}
		if n.Local {
			where += " (asked for the cluster)"
		}
		if n.Errors > 0 {
			where += fmt.Sprintf("  ! %d connection errors", n.Errors)
		}
		fmt.Fprintf(stdout, "  %-22s %-15s %s\n", n.Host, n.Address, where)
	}
	fmt.Fprintln(stdout)

	o := Options{Mode: *mode, Protocol: *protocol, Host: *host, Port: *port, User: *user, Extra: fs.Args()}
	if *collectFrom == "each-node" {
		fmt.Fprintf(stdout, "Run this ON each host (with %s set, or it asks for the password),\n"+
			"then send back the clickhouse_backup_*.tar.gz from ./clickhouse_results/<folder>/:\n\n", PasswordEnvVar)
		if *mode == "gov" {
			fmt.Fprintln(stdout, "Each run asks for the salt: type the same one on every host, or the hashes\n"+
				"will not match across the bundles.")
			fmt.Fprintln(stdout)
		}
		for _, n := range nodes {
			fmt.Fprintf(stdout, "# %s\n%s\n\n", n.Host, Shown("./clickhouse-diagnostic", EachNodeArgs(n, o)))
		}
		return 0
	}

	if _, err := os.Stat("queries." + *mode); err != nil {
		fmt.Fprintf(stderr, "queries.%s/ not found in the current directory: run from the unpacked clickhouse-diagnostic release.\n", *mode)
		return 2
	}
	bin, err := executable()
	if err != nil {
		fmt.Fprintf(stderr, "Cannot locate this binary: %v\n", err)
		return 2
	}
	dir := *outputDir
	if dir == "" {
		dir = "cluster_bundles_" + time.Now().Format("20060102_150405")
	}
	if o.OutputDir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	o.LocalFiles = isThisMachine(*host)

	fmt.Fprintln(stdout, "Will run, one host at a time:")
	fmt.Fprintln(stdout)
	for _, n := range nodes {
		fmt.Fprintf(stdout, "%s\n\n", Shown(bin, HereArgs(n, o)))
	}
	if *mode == "gov" {
		fmt.Fprintln(stdout, "gov mode collects system tables only, on every host: no log files, configs or\n"+
			"host facts, wherever it runs.")
	} else {
		fmt.Fprintln(stdout, "Other hosts: system tables only (incl. text_log, query_log) — no server log\n"+
			"files, configs or host facts. Use -collect-from each-node for those.")
	}
	fmt.Fprintln(stdout)
	if !*yes {
		fmt.Fprintf(stdout, "Run %d collection(s)? [y/N] ", len(nodes))
		answer, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "Nothing run.")
			return 0
		}
	}

	var results []result
	for _, n := range nodes {
		results = append(results, runOne(bin, n, o, *timeout, stdout))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-22s %-11s %6s %9s  %s\n", "host", "status", "time", "size", "archive")
	failed := 0
	for _, r := range results {
		size, where, note := "-", "see "+filepath.Join(Label(r.node), "run.log"), ""
		if r.archive != "" {
			size = human(r.size)
			where, _ = filepath.Rel(o.OutputDir, r.archive)
			if r.size > portalCapBytes {
				note = "  over the 200 MB portal limit"
			}
		}
		if r.status != "ok" {
			failed++
		}
		fmt.Fprintf(&sb, "%-22s %-11s %5.0fs %9s  %s%s\n", r.node.Host, r.status, r.elapsed.Seconds(), size, where, note)
	}
	if err := os.WriteFile(filepath.Join(o.OutputDir, "summary.txt"), []byte(sb.String()), 0o644); err != nil {
		fmt.Fprintf(stderr, "Warning: summary.txt not written: %v\n", err)
	}
	fmt.Fprintf(stdout, "\n%s\nBundles in %s\n", sb.String(), o.OutputDir)

	// gov never collects log files or configs, so there is nothing to add.
	if *mode != "gov" {
		fmt.Fprintln(stdout, "\nServer log files and configs are only read on the host itself. To add them,\nrun this ON each host:")
		fmt.Fprintln(stdout)
		for _, n := range nodes {
			if !(n.Local && o.LocalFiles) {
				fmt.Fprintln(stdout, Shown("./clickhouse-diagnostic", EachNodeArgs(n, o)))
			}
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// runOne collects one node: a separate process of this binary, its output in
// <folder>/run.log. Success is an archive in the folder — the binary exits 0
// even when it could not reach the server or write the archive.
func runOne(bin string, n Node, o Options, timeout time.Duration, stdout io.Writer) result {
	folder := filepath.Join(o.OutputDir, Label(n))
	r := result{node: n, status: "ok"}
	start := time.Now()
	fmt.Fprintf(stdout, "→ %s ... ", n.Host)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		r.status = "failed"
		fmt.Fprintf(stdout, "failed: %v\n", err)
		return r
	}
	// Owner-only: in gov mode the run prints the real database and table
	// names next to their hashes. Chmod as well, for a log left by an
	// earlier run, which OpenFile does not re-permission.
	logPath := filepath.Join(folder, "run.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		err = os.Chmod(logPath, 0o600)
	}
	if err != nil {
		if logFile != nil {
			logFile.Close()
		}
		r.status = "failed"
		fmt.Fprintf(stdout, "failed: %v\n", err)
		return r
	}
	// A reused -output-dir still holds earlier archives; only one this run
	// created or replaced counts, or a host that is down now would report
	// the previous run's bundle as ok.
	before := archivesIn(folder)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, bin, HereArgs(n, o)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile // the environment, CLICKHOUSE_PASSWORD included, is inherited
	runErr := cmd.Run()
	cancel()
	logFile.Close()
	r.elapsed = time.Since(start)

	// ok needs all three: the run exited 0, it left a new archive, and the
	// archive reads to its end.
	switch r.archive = newArchive(folder, before); {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.status = "timed out"
	case runErr != nil:
		r.status = "failed"
	case r.archive == "":
		r.status = "no archive"
	case verifyArchive(r.archive) != nil:
		r.status = "bad archive"
	}
	if r.archive != "" {
		if fi, err := os.Stat(r.archive); err == nil {
			r.size = fi.Size()
		}
	}
	if r.status != "ok" {
		r.archive = "" // not a bundle to send
		fmt.Fprintf(stdout, "%s, %.0fs, see %s\n", r.status, r.elapsed.Seconds(), filepath.Join(Label(n), "run.log"))
		return r
	}
	note := ""
	if r.size > portalCapBytes {
		note = ", over the 200 MB portal limit"
	}
	fmt.Fprintf(stdout, "ok, %.0fs, %s%s\n", r.elapsed.Seconds(), human(r.size), note)
	return r
}
