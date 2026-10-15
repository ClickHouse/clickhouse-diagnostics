package clustercollect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// labRows is system.clusters as a 2-shard x 2-replica lab cluster reports it
// from its first node: the real cluster, a second cluster over the same four
// hosts, and the stock image's `default` cluster, which lists only localhost.
func row(cluster string, shard, replica int, host, address string, local, errors int) Row {
	return Row{Cluster: cluster, ShardNum: shard, ReplicaNum: replica, HostName: host,
		HostAddress: address, IsLocal: local, ErrorsCount: errors}
}

var labRows = []Row{
	row("all_replicas", 1, 1, "ch-s1r1", "172.20.0.5", 1, 0),
	row("all_replicas", 1, 2, "ch-s1r2", "172.20.0.6", 0, 0),
	row("all_replicas", 1, 3, "ch-s2r1", "172.20.0.7", 0, 0),
	row("all_replicas", 1, 4, "ch-s2r2", "172.20.0.8", 0, 0),
	row("default", 1, 1, "localhost", "127.0.0.1", 1, 0),
	row("prod_cluster", 1, 1, "ch-s1r1", "172.20.0.5", 1, 0),
	row("prod_cluster", 1, 2, "ch-s1r2", "172.20.0.6", 0, 0),
	row("prod_cluster", 2, 1, "ch-s2r1", "172.20.0.7", 0, 0),
	row("prod_cluster", 2, 2, "ch-s2r2", "172.20.0.8", 0, 0),
}

func mustDiscover(t *testing.T, rows []Row, cluster string) []Node {
	t.Helper()
	nodes, err := Discover(rows, cluster)
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func byHost(nodes []Node) map[string]Node {
	m := map[string]Node{}
	for _, n := range nodes {
		m[n.Host] = n
	}
	return m
}

func opts() Options {
	return Options{Mode: "onprem", Protocol: "http", Host: "ch-s1r1", Port: "8123", User: "diag", OutputDir: "/out"}
}

func argValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// ---- discovery ------------------------------------------------------------

func TestDiscover_EveryMachineOnce(t *testing.T) {
	var hosts []string
	for _, n := range mustDiscover(t, labRows, "") {
		hosts = append(hosts, n.Host)
	}
	if got := strings.Join(hosts, ","); got != "ch-s1r1,ch-s1r2,ch-s2r1,ch-s2r2" {
		t.Errorf("hosts = %s", got)
	}
}

func TestDiscover_LocalNodeFoldsItsLoopbackRow(t *testing.T) {
	// 127.0.0.1 in `default` and 172.20.0.5 in prod_cluster are one server.
	var local []Node
	for _, n := range mustDiscover(t, labRows, "") {
		if n.Local {
			local = append(local, n)
		}
	}
	if len(local) != 1 || local[0].Host != "ch-s1r1" {
		t.Errorf("local nodes = %+v, want only ch-s1r1", local)
	}
}

func TestDiscover_LabelsComeFromTheClusterWithMostShards(t *testing.T) {
	want := map[string][2]int{"ch-s1r1": {1, 1}, "ch-s1r2": {1, 2}, "ch-s2r1": {2, 1}, "ch-s2r2": {2, 2}}
	for h, n := range byHost(mustDiscover(t, labRows, "")) {
		if got := [2]int{n.Shard, n.Replica}; got != want[h] {
			t.Errorf("%s = shard/replica %v, want %v", h, got, want[h])
		}
	}
}

func TestDiscover_DefaultClusterIsOnlyThisServer(t *testing.T) {
	if n := mustDiscover(t, labRows, "default"); len(n) != 1 {
		t.Errorf("the stock `default` cluster gave %d hosts, want 1", len(n))
	}
}

func TestDiscover_OtherLoopbackRowsAreDropped(t *testing.T) {
	rows := append(append([]Row{}, labRows...), row("test_cluster_two_shards", 2, 1, "127.0.0.2", "127.0.0.2", 0, 0))
	if n := mustDiscover(t, rows, ""); len(n) != 4 {
		t.Errorf("got %d hosts, want 4", len(n))
	}
}

func TestDiscover_UnknownClusterListsTheRealNames(t *testing.T) {
	_, err := Discover(labRows, "default_cluster")
	if err == nil || !strings.Contains(err.Error(), "prod_cluster") {
		t.Errorf("err = %v, want it to list prod_cluster", err)
	}
}

func TestDiscover_ConnectionErrorsAreKept(t *testing.T) {
	rows := append(append([]Row{}, labRows[:len(labRows)-1]...), row("prod_cluster", 2, 2, "ch-s2r2", "172.20.0.8", 0, 3))
	if n := byHost(mustDiscover(t, rows, ""))["ch-s2r2"]; n.Errors != 3 {
		t.Errorf("ch-s2r2 errors = %d, want 3", n.Errors)
	}
}

func TestLabel_IsPathSafe(t *testing.T) {
	if got := Label(Node{Host: "../etc/x y", Shard: 1, Replica: 2}); got != "s1r2_.._etc_x_y" {
		t.Errorf("Label = %q", got)
	}
}

func TestParseRows_ReadsJSONEachRow(t *testing.T) {
	var buf bytes.Buffer
	for _, r := range labRows[:2] {
		b, _ := json.Marshal(r)
		buf.Write(append(b, '\n'))
	}
	rows, err := ParseRows(buf.String())
	if err != nil || len(rows) != 2 || rows[1].HostName != "ch-s1r2" {
		t.Errorf("ParseRows = %+v, %v", rows, err)
	}
}

// ---- commands -------------------------------------------------------------

func TestHereArgs_RemoteHostsNeverCollectLocalFiles(t *testing.T) {
	o := opts()
	o.LocalFiles = true
	args := HereArgs(byHost(mustDiscover(t, labRows, ""))["ch-s2r1"], o)
	if argValue(args, "-host-info") != "off" || argValue(args, "-logs") != "off" || !has(args, "-skip-config") {
		t.Errorf("remote run reads local files: %v", args)
	}
	if argValue(args, "-host") != "ch-s2r1" {
		t.Errorf("-host = %q", argValue(args, "-host"))
	}
}

func TestHereArgs_LocalNodeCollectsFilesOnlyWhenRunOnIt(t *testing.T) {
	local := byHost(mustDiscover(t, labRows, ""))["ch-s1r1"]
	on, elsewhere := opts(), opts()
	on.LocalFiles = true
	if has(HereArgs(local, on), "-skip-config") {
		t.Error("run on the node itself, but configs are skipped")
	}
	if !has(HereArgs(local, elsewhere), "-skip-config") {
		t.Error("run elsewhere, but this machine's configs would be collected as the node's")
	}
	if argValue(HereArgs(local, on), "-host") != "ch-s1r1" {
		t.Error("the local node must be reached through -host as given")
	}
}

func TestCommands_EveryRunIsSingleNode(t *testing.T) {
	// Without it a SharedMergeTree cluster switches to clusterAllReplicas on
	// EVERY run and collects the whole cluster once per host.
	for _, n := range mustDiscover(t, labRows, "") {
		if !has(HereArgs(n, opts()), "-single-node") || !has(EachNodeArgs(n, opts()), "-single-node") {
			t.Errorf("%s: -single-node missing", n.Host)
		}
	}
}

func TestCommands_NeverCarryAPassword(t *testing.T) {
	for _, n := range mustDiscover(t, labRows, "") {
		if has(HereArgs(n, opts()), "-password") || has(EachNodeArgs(n, opts()), "-password") {
			t.Errorf("%s: -password on a command line", n.Host)
		}
	}
}

func TestHereArgs_EachHostHasItsOwnOutputDir(t *testing.T) {
	dirs := map[string]bool{}
	for _, n := range mustDiscover(t, labRows, "") {
		dirs[argValue(HereArgs(n, opts()), "-output-dir")] = true
	}
	if len(dirs) != 4 {
		t.Errorf("%d distinct output dirs, want 4", len(dirs))
	}
}

func TestEachNodeArgs_RunOnTheHostItself(t *testing.T) {
	if got := argValue(EachNodeArgs(byHost(mustDiscover(t, labRows, ""))["ch-s1r2"], opts()), "-host"); got != "localhost" {
		t.Errorf("-host = %q, want localhost", got)
	}
}

func TestCommands_ExtraArgumentsReachEveryRun(t *testing.T) {
	o := opts()
	o.Extra = []string{"-from", "2026-10-01"}
	n := mustDiscover(t, labRows, "")[1]
	for _, args := range [][]string{HereArgs(n, o), EachNodeArgs(n, o)} {
		if got := strings.Join(args[len(args)-2:], " "); got != "-from 2026-10-01" {
			t.Errorf("tail = %q", got)
		}
	}
}

func TestShown_QuotesAndMasks(t *testing.T) {
	got := Shown("./clickhouse-diagnostic", []string{"-host", "a b", "-password", "s3cret"})
	if strings.Contains(got, "s3cret") || !strings.Contains(got, "'a b'") {
		t.Errorf("Shown = %s", got)
	}
}

// ---- Main, end to end -----------------------------------------------------

// fakeServer answers the discovery query with labRows.
func fakeServer(t *testing.T) (host, port string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "diag" || p != "pw" {
			if r.Header.Get("X-ClickHouse-User") != "diag" || r.Header.Get("X-ClickHouse-Key") != "pw" {
				http.Error(w, "Code: 516. Authentication failed", http.StatusUnauthorized)
				return
			}
		}
		for _, r := range labRows {
			b, _ := json.Marshal(r)
			w.Write(append(b, '\n'))
		}
	}))
	t.Cleanup(srv.Close)
	h, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return h, p
}

func TestMain_RequiresThePasswordInTheEnvironment(t *testing.T) {
	t.Setenv(PasswordEnvVar, "") // restored after the test
	os.Unsetenv(PasswordEnvVar)
	var out, errOut bytes.Buffer
	if code := Main([]string{"-user", "diag"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), PasswordEnvVar) {
		t.Errorf("stderr does not name %s: %s", PasswordEnvVar, errOut.String())
	}
}

func TestMain_EachNodePrintsOneCommandPerHost(t *testing.T) {
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	var out, errOut bytes.Buffer
	code := Main([]string{"-host", host, "-port", port, "-user", "diag", "-collect-from", "each-node"},
		strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, errOut.String())
	}
	if n := strings.Count(out.String(), "-host localhost"); n != 4 {
		t.Errorf("%d per-host commands, want 4:\n%s", n, out.String())
	}
	if strings.Contains(out.String(), "pw") {
		t.Error("the password appears in the output")
	}
}

// fakeBinary stands in for clickhouse-diagnostic: it records its arguments
// and whether the password arrived through the environment, then writes a
// real archive into -output-dir — except for failHost, which fails like a
// node that is down.
func fakeBinary(t *testing.T, failHost string) string {
	return fakeBinaryWith(t, map[string]string{failHost: "down"})
}

// fakeBinaryWith takes a behaviour per host: "down" writes nothing,
// "truncated" leaves a cut-off archive and exits 0, "exit3" writes a valid
// archive and exits 3.
func fakeBinaryWith(t *testing.T, behaviour map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stand-in")
	}
	var cases strings.Builder
	for h, b := range behaviour {
		if h != "" {
			fmt.Fprintf(&cases, "  %s) mode=%s;;\n", h, b)
		}
	}
	script := `#!/bin/sh
out=""; host=""
while [ $# -gt 0 ]; do
  case "$1" in -output-dir) out="$2"; shift;; -host) host="$2"; shift;; esac; shift
done
mkdir -p "$out"
echo "host=$host env=${CLICKHOUSE_PASSWORD:-unset}" > "$out/seen.txt"
mode=ok
case "$host" in
` + cases.String() + `esac
[ "$mode" = down ] && { echo "dial tcp: no such host"; exit 0; }
tmp=$(mktemp -d); mkdir "$tmp/clickhouse_backup_20260101_000000"
head -c 20000 /dev/urandom > "$tmp/clickhouse_backup_20260101_000000/system.version_20260101_000000.jsonl"
a="$out/clickhouse_backup_20260101_000000.tar.gz"
tar czf "$a" -C "$tmp" clickhouse_backup_20260101_000000
[ "$mode" = truncated ] && { head -c 200 "$a" > "$a.cut"; mv "$a.cut" "$a"; exit 0; }
[ "$mode" = exit3 ] && exit 3
exit 0
`
	p := filepath.Join(t.TempDir(), "clickhouse-diagnostic")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "queries.onprem"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })
	return dir
}

func TestMain_HereRunsEveryHostAndReportsTheFailedOne(t *testing.T) {
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	dir := chdirTemp(t)
	prev := executable
	bin := fakeBinary(t, "ch-s2r2")
	executable = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executable = prev })

	var out, errOut bytes.Buffer
	code := Main([]string{"-host", host, "-port", port, "-user", "diag", "-yes", "-output-dir", "bundles"},
		strings.NewReader(""), &out, &errOut)
	if code != 1 {
		t.Errorf("exit = %d, want 1 (one host failed)\n%s%s", code, out.String(), errOut.String())
	}
	summary, err := os.ReadFile(filepath.Join(dir, "bundles", "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(summary), " ok ") != 3 || !strings.Contains(string(summary), "no archive") {
		t.Errorf("summary:\n%s", summary)
	}
	seen, err := os.ReadFile(filepath.Join(dir, "bundles", "s1r2_ch-s1r2", "seen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), "env=pw") {
		t.Errorf("the run did not inherit %s: %s", PasswordEnvVar, seen)
	}
}

func TestMain_AsksBeforeRunning(t *testing.T) {
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	dir := chdirTemp(t)
	prev := executable
	bin := fakeBinary(t, "")
	executable = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executable = prev })

	var out, errOut bytes.Buffer
	code := Main([]string{"-host", host, "-port", port, "-user", "diag", "-output-dir", "bundles"},
		strings.NewReader("n\n"), &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "Nothing run") {
		t.Errorf("exit = %d, out:\n%s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "bundles")); err == nil {
		t.Error("declined, but the output directory was created")
	}
}

// ---- review round: flags the command owns, stale archives, TLS, locality, log permissions

func TestMain_RejectsFlagsTheCommandOwns(t *testing.T) {
	t.Setenv(PasswordEnvVar, "pw")
	for _, extra := range [][]string{
		{"-password", "s3cret"}, {"--password=s3cret"}, {"-password=s3cret"}, {"--password", "s3cret"},
		{"-host", "localhost"}, {"-single-node=false"}, {"-logs", "on"}, {"-host-info=on"},
		{"-skip-config=false"}, {"-output-dir", "/tmp/x"}, {"--user", "other"}, {"-mode", "cloud"},
		{"-port", "9000"}, {"-protocol", "https"}, {"-skip-archive"}, {"-dry-run"},
	} {
		var out, errOut bytes.Buffer
		code := Main(append([]string{"-user", "diag", "--"}, extra...), strings.NewReader(""), &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), "cannot be passed through") {
			t.Errorf("-- %v: exit %d, stderr %q — want it refused before discovery", extra, code, errOut.String())
		}
		if strings.Contains(out.String()+errOut.String(), "s3cret") {
			t.Errorf("-- %v: the password was printed", extra)
		}
		if strings.Contains(extra[0], "password") && !strings.Contains(errOut.String(), PasswordEnvVar) {
			t.Errorf("-- %v: the refusal should point to %s: %s", extra, PasswordEnvVar, errOut.String())
		}
	}
}

func TestValidateExtra_AllowsCollectionOptions(t *testing.T) {
	if err := validateExtra([]string{"-from", "2026-10-01", "-to", "2026-10-02", "-salt", "abc12345",
		"--normalized-query-hash", "123", "-skip-dashboard", "-logs-max-mb=200", "-keeper-mntr", "off"}); err != nil {
		t.Errorf("collection options rejected: %v", err)
	}
}

func TestMain_RetryDoesNotReuseAPreviousArchive(t *testing.T) {
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	dir := chdirTemp(t)
	prev := executable
	t.Cleanup(func() { executable = prev })
	args := []string{"-host", host, "-port", port, "-user", "diag", "-yes", "-output-dir", "bundles"}

	ok := fakeBinary(t, "")
	executable = func() (string, error) { return ok, nil }
	var out, errOut bytes.Buffer
	if code := Main(args, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("first run: exit %d\n%s%s", code, out.String(), errOut.String())
	}

	// Retry into the same directory with ch-s2r2 now down: its folder still
	// holds the first run's archive, which must not count for this run.
	down := fakeBinary(t, "ch-s2r2")
	executable = func() (string, error) { return down, nil }
	out.Reset()
	if code := Main(args, strings.NewReader(""), &out, &errOut); code != 1 {
		t.Errorf("retry: exit %d, want 1 — a stale archive was accepted\n%s", code, out.String())
	}
	summary, _ := os.ReadFile(filepath.Join(dir, "bundles", "summary.txt"))
	for _, line := range strings.Split(string(summary), "\n") {
		if strings.HasPrefix(line, "ch-s2r2 ") && !strings.Contains(line, "no archive") {
			t.Errorf("ch-s2r2 reported as %q", line)
		}
	}
}

func TestEachNodeArgs_HTTPSConnectsByTheNodeName(t *testing.T) {
	o := opts()
	o.Protocol = "https"
	nodes := byHost(mustDiscover(t, labRows, ""))
	if got := argValue(EachNodeArgs(nodes["ch-s1r2"], o), "-host"); got != "ch-s1r2" {
		t.Errorf("https -host = %q, want the node's name (its certificate does not cover localhost)", got)
	}
	if got := argValue(EachNodeArgs(nodes["ch-s1r1"], o), "-host"); got != o.Host {
		t.Errorf("https -host for the discovery node = %q, want %q", got, o.Host)
	}
	if got := argValue(EachNodeArgs(nodes["ch-s1r2"], opts()), "-host"); got != "localhost" {
		t.Errorf("http -host = %q, want localhost", got)
	}
}

func TestIsThisMachine(t *testing.T) {
	prevName, prevAddrs := hostname, interfaceAddrs
	t.Cleanup(func() { hostname, interfaceAddrs = prevName, prevAddrs })
	hostname = func() (string, error) { return "ch-01.site-a", nil }
	interfaceAddrs = func() ([]net.Addr, error) {
		_, n, _ := net.ParseCIDR("10.0.0.5/24")
		n.IP = net.ParseIP("10.0.0.5")
		return []net.Addr{n}, nil
	}
	for host, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "::1": true,
		"10.0.0.5": true, // this machine's own address
		"10.0.0.6": false,
		"ch-01":    true, "CH-01.site-a": true,
		"ch-01.site-b": false, // another site's ch-01: both qualified, so exact match only
		"ch-02":        false,
	} {
		if got := isThisMachine(host); got != want {
			t.Errorf("isThisMachine(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestMain_RunLogIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	dir := chdirTemp(t)
	prev := executable
	bin := fakeBinary(t, "")
	executable = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executable = prev })

	// A run.log left world-readable by an earlier run must be tightened too.
	stale := filepath.Join(dir, "bundles", "s1r2_ch-s1r2", "run.log")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	Main([]string{"-host", host, "-port", port, "-user", "diag", "-yes", "-output-dir", "bundles"}, strings.NewReader(""), &out, &errOut)
	logs, _ := filepath.Glob(filepath.Join(dir, "bundles", "*", "run.log"))
	if len(logs) != 4 {
		t.Fatalf("%d run.log files, want 4", len(logs))
	}
	for _, l := range logs {
		fi, err := os.Stat(l)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is %v — gov runs print raw database/table names into it", l, fi.Mode().Perm())
		}
	}
}

// ---- review round 2: salt, unusable archives, literal paths

func TestShown_MasksSaltAndPasswordInEverySpelling(t *testing.T) {
	got := Shown("./clickhouse-diagnostic", []string{"-salt", "abc12345", "--salt=def67890", "-password=pw1", "--password", "pw2"})
	for _, secret := range []string{"abc12345", "def67890", "pw1", "pw2"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q shown in %s", secret, got)
		}
	}
}

func TestEachNodeArgs_LeavesTheSaltOut(t *testing.T) {
	o := opts()
	o.Mode = "gov"
	o.Extra = []string{"-from", "2026-10-01", "-salt", "abc12345", "--salt=def67890"}
	args := EachNodeArgs(mustDiscover(t, labRows, "")[1], o)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "salt") || strings.Contains(joined, "abc12345") || strings.Contains(joined, "def67890") {
		t.Errorf("salt in a command to run on a host: %s", joined)
	}
	if !strings.Contains(joined, "-from 2026-10-01") {
		t.Errorf("other pass-through arguments lost: %s", joined)
	}
	// The run from this machine has no terminal to ask on, so it keeps it.
	if !has(HereArgs(mustDiscover(t, labRows, "")[1], o), "abc12345") {
		t.Error("the here-run needs the salt")
	}
}

func TestMain_GovNeverPrintsTheSalt(t *testing.T) {
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	chdirTemp(t)
	if err := os.MkdirAll("queries.gov", 0o755); err != nil {
		t.Fatal(err)
	}
	prev := executable
	bin := fakeBinary(t, "")
	executable = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executable = prev })
	for _, from := range []string{"each-node", "here"} {
		var out, errOut bytes.Buffer
		Main([]string{"-host", host, "-port", port, "-user", "diag", "-mode", "gov", "-yes", "-collect-from", from,
			"-output-dir", "b-" + from, "--", "-salt", "abc12345"}, strings.NewReader(""), &out, &errOut)
		if strings.Contains(out.String()+errOut.String(), "abc12345") {
			t.Errorf("%s: the salt was printed:\n%s", from, out.String())
		}
		if strings.Contains(out.String(), "log files and configs") {
			t.Errorf("%s: gov never collects log files or configs, but the output offers them", from)
		}
	}
}

func TestMain_UnusableArchivesAreNotOK(t *testing.T) {
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	dir := chdirTemp(t)
	prev := executable
	bin := fakeBinaryWith(t, map[string]string{"ch-s1r2": "truncated", "ch-s2r1": "exit3"})
	executable = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executable = prev })

	var out, errOut bytes.Buffer
	code := Main([]string{"-host", host, "-port", port, "-user", "diag", "-yes", "-output-dir", "bundles"},
		strings.NewReader(""), &out, &errOut)
	if code != 1 {
		t.Errorf("exit = %d, want 1\n%s", code, out.String())
	}
	summary, _ := os.ReadFile(filepath.Join(dir, "bundles", "summary.txt"))
	for _, h := range []string{"ch-s1r2", "ch-s2r1"} {
		for _, line := range strings.Split(string(summary), "\n") {
			if strings.HasPrefix(line, h+" ") && strings.Contains(line, " ok ") {
				t.Errorf("%s reported ok: %q", h, line)
			}
		}
	}
	if strings.Count(string(summary), " ok ") != 2 {
		t.Errorf("want the 2 good hosts ok:\n%s", summary)
	}
}

func TestMain_OutputDirWithGlobCharacters(t *testing.T) {
	host, port := fakeServer(t)
	t.Setenv(PasswordEnvVar, "pw")
	chdirTemp(t)
	prev := executable
	bin := fakeBinary(t, "")
	executable = func() (string, error) { return bin, nil }
	t.Cleanup(func() { executable = prev })
	var out, errOut bytes.Buffer
	if code := Main([]string{"-host", host, "-port", port, "-user", "diag", "-yes", "-output-dir", "support[123]"},
		strings.NewReader(""), &out, &errOut); code != 0 {
		t.Errorf("exit = %d with -output-dir 'support[123]'\n%s", code, out.String())
	}
}
