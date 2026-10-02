package keeper

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeKeeper answers four-letter words like Keeper does: read the word,
// write the reply, close. An empty reply for a command models one that is
// not in four_letter_word_white_list. hang makes it accept and never answer.
func fakeKeeper(t *testing.T, replies map[string]string, hang bool) (host, port string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				if hang {
					<-done
					return
				}
				io.WriteString(c, replies[string(buf)])
			}(c)
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	return h, p, func() { close(done); ln.Close() }
}

var okReplies = map[string]string{
	"ruok": "imok",
	"srvr": "ClickHouse Keeper version: v26.7.5.10\nLatency min/avg/max: 0/1/193\nOutstanding: 0\nZxid: 0x77e1\nMode: standalone\nNode count: 121\n",
	"mntr": "zk_version\tv26.7.5.10\nzk_avg_latency\t1\nzk_server_state\tstandalone\nzk_znode_count\t121\n",
}

func TestCollect_NormalReply(t *testing.T) {
	h, p, stop := fakeKeeper(t, okReplies, false)
	defer stop()
	dir := t.TempDir()
	fixed := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	out, err := Collect(dir, []Target{{h, p}}, Options{Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Status != "ok" {
		t.Fatalf("want one ok outcome, got %+v", out)
	}
	want := filepath.Join(dir, DirName, h+"_"+p+".txt")
	if out[0].File != want {
		t.Errorf("file = %s, want %s", out[0].File, want)
	}
	b, _ := os.ReadFile(want)
	s := string(b)
	for _, must := range []string{"# target: " + h + ":" + p, "# collected_at: 2026-10-02T12:00:00Z", "## ruok\nimok", "## srvr\nClickHouse Keeper version", "## mntr\nzk_version", "zk_server_state\tstandalone"} {
		if !strings.Contains(s, must) {
			t.Errorf("file lacks %q:\n%s", must, s)
		}
	}
	if out[0].Bytes != int64(len(b)) {
		t.Errorf("Bytes = %d, file is %d", out[0].Bytes, len(b))
	}
}

func TestCollect_Refused(t *testing.T) {
	// Listen, take the port, close: nothing answers there now.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	dir := t.TempDir()
	out, err := Collect(dir, []Target{{h, p}}, Options{DialTimeout: time.Second, ReadTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != "refused" {
		t.Fatalf("status = %s (%s), want refused", out[0].Status, out[0].Err)
	}
	// The file still exists so the bundle records the attempt.
	b, rerr := os.ReadFile(out[0].File)
	if rerr != nil || !strings.Contains(string(b), "error: connection refused") {
		t.Errorf("attempt not recorded in file: %v %q", rerr, b)
	}
}

func TestCollect_Timeout(t *testing.T) {
	h, p, stop := fakeKeeper(t, nil, true)
	defer stop()
	start := time.Now()
	out, err := Collect(t.TempDir(), []Target{{h, p}}, Options{DialTimeout: time.Second, ReadTimeout: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != "timeout" {
		t.Fatalf("status = %s (%s), want timeout", out[0].Status, out[0].Err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("three commands at 150 ms read timeout took %s — the deadline is not applied", el)
	}
}

func TestCollect_PartialWhenCommandDisabled(t *testing.T) {
	replies := map[string]string{"ruok": "imok", "srvr": okReplies["srvr"], "mntr": ""}
	h, p, stop := fakeKeeper(t, replies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{h, p}}, Options{})
	if out[0].Status != "partial" {
		t.Fatalf("status = %s, want partial", out[0].Status)
	}
	b, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(b), "four_letter_word_white_list") {
		t.Errorf("empty reply not explained:\n%s", b)
	}
}

func TestCollect_GovHashesHostAndFiltersAddresses(t *testing.T) {
	// A reply that DOES carry a client address, to prove the gov filter
	// removes it even though srvr/mntr do not print one today.
	replies := map[string]string{
		"ruok": "imok",
		"srvr": "ClickHouse Keeper version: v26.7.5.10\nClients:\n 10.0.0.7:60155(recved=0,sent=0)\nMode: leader\n",
		"mntr": "zk_version\tv26.7\nzk_server_state\tleader\n",
	}
	h, p, stop := fakeKeeper(t, replies, false)
	defer stop()
	dir := t.TempDir()
	out, _ := Collect(dir, []Target{{h, p}}, Options{GovSalt: "PR3TESTSALT01"})
	// Same form as hex(SHA256(concat(host, salt))) in ClickHouse: for
	// '127.0.0.1' + 'PR3TESTSALT01' the server returns this value.
	if h == "127.0.0.1" {
		const want = "348AB0C01381EBFC9D3E72FA52E6D8A4CE3E6A3D269119D2FA7794C924C532AC"
		if out[0].Label != want {
			t.Errorf("label = %s, want ClickHouse-compatible %s", out[0].Label, want)
		}
	}
	if strings.Contains(filepath.Base(out[0].File), h) {
		t.Errorf("gov file name carries the raw host: %s", out[0].File)
	}
	b, _ := os.ReadFile(out[0].File)
	s := string(b)
	if strings.Contains(s, h) || strings.Contains(s, "10.0.0.7") {
		t.Errorf("gov file carries an address:\n%s", s)
	}
	if !strings.Contains(s, "Mode: leader") || !strings.Contains(s, "zk_server_state\tleader") {
		t.Errorf("gov filter dropped more than the address line:\n%s", s)
	}
}

func TestParseTargets(t *testing.T) {
	got := ParseTargets("k1\t9181\nk2\t9181\nk1\t9181\n\nbroken line\n\t9181\n")
	want := []Target{{"k1", "9181"}, {"k2", "9181"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if ParseTargets("") != nil {
		t.Error("empty input must yield no targets")
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		setting, mode string
		skip, warn    bool
		err           bool
	}{
		{"auto", "onprem", false, false, false},
		{"auto", "gov", false, false, false},
		{"auto", "cloud", true, false, false},
		{"", "onprem", false, false, false},
		{"on", "cloud", false, true, false},
		{"on", "gov", false, false, false},
		{"off", "onprem", true, false, false},
		{"OFF", " GOV ", true, false, false},
		{"maybe", "onprem", true, false, true},
	}
	for _, c := range cases {
		skip, warn, err := Resolve(c.setting, c.mode)
		if skip != c.skip || (warn != "") != c.warn || (err != nil) != c.err {
			t.Errorf("Resolve(%q,%q) = skip %v warn %q err %v; want skip %v warn %v err %v", c.setting, c.mode, skip, warn, err, c.skip, c.warn, c.err)
		}
	}
}

func TestFileName_SafeForIPv6(t *testing.T) {
	if got := fileName("fd00::1", "9181"); strings.Contains(got, ":") {
		t.Errorf("colons survive in %s", got)
	}
}

// The ensemble is declared in the server configuration, not in
// system.zookeeper_connection (which names the connected member only). Both
// config.d/ and the adjacent config.xml are read; a node without <port> gets
// the ZooKeeper default; substitutions are skipped.
func TestTargetsFromConfig(t *testing.T) {
	root := t.TempDir()
	cd := filepath.Join(root, "config.d")
	if err := os.Mkdir(cd, 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(name, body string) {
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(cd, "keeper.xml"), `<clickhouse><zookeeper>
	  <node index="1"><host>keeper-a.example</host><port>9181</port></node>
	  <node><host>keeper-b.example</host></node>
	  <node><host>{env:ZK_HOST}</host><port>9181</port></node>
	</zookeeper></clickhouse>`)
	must(filepath.Join(root, "config.xml"), `<clickhouse><zookeeper><node><host>keeper-c.example</host><port>2181</port></node></zookeeper></clickhouse>`)
	must(filepath.Join(cd, "notes.txt"), `<zookeeper><node><host>ignored.example</host></node></zookeeper>`)

	got := TargetsFromConfig(cd)
	want := []Target{{"keeper-a.example", "9181"}, {"keeper-b.example", DefaultPort}, {"keeper-c.example", "2181"}}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target %d = %v, want %v", i, got[i], want[i])
		}
	}
	// A non-*.d directory must not volunteer its parent.
	if got := TargetsFromConfig(root); len(got) != 1 || got[0].Host != "keeper-c.example" {
		t.Errorf("plain dir: %v, want only the config.xml node", got)
	}
	if TargetsFromConfig("") != nil || TargetsFromConfig(filepath.Join(root, "missing")) != nil {
		t.Error("empty or unreadable dir must yield nil")
	}
	merged := MergeTargets([]Target{{"keeper-b.example", DefaultPort}}, []Target{{"keeper-b.example", DefaultPort}})
	if len(merged) != 1 {
		t.Errorf("merge must dedupe: %v", merged)
	}
	merged = MergeTargets([]Target{{"keeper-b.example", DefaultPort}}, want)
	if len(merged) != 3 || merged[0].Host != "keeper-b.example" {
		t.Errorf("merge keeps connected first and adds the rest: %v", merged)
	}
}

// A member that answers part of mntr and then stalls until the deadline is
// a timeout, not an ok: the partial text is kept but marked truncated.
func TestCollect_PartialReplyThenStallIsTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	stop := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte("zk_version\tv1\nzk_avg_latency\t1\n"))
				<-stop // never finish, never close: the client's deadline must end it
			}(c)
		}
	}()
	defer close(stop)
	host, port, _ := net.SplitHostPort(ln.Addr().String())

	dir := t.TempDir()
	out, err := Collect(dir, []Target{{host, port}}, Options{DialTimeout: time.Second, ReadTimeout: 300 * time.Millisecond})
	if err != nil || len(out) != 1 {
		t.Fatalf("collect: %v %v", out, err)
	}
	if out[0].Status != "timeout" {
		t.Errorf("status = %q, want timeout (partial replies must not count as ok)", out[0].Status)
	}
	body, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(body), "zk_avg_latency\t1") || !strings.Contains(string(body), "reply truncated") {
		t.Errorf("file should keep the partial reply and mark it truncated:\n%s", body)
	}
}

// IPv4-mapped and scoped IPv6 client addresses are addresses too.
func TestGovFilter_IPv6Forms(t *testing.T) {
	in := "zk_version\tv1\n /[::ffff:10.0.0.7]:60155[0](queued=0)\n /[fe80::1%eth0]:2181\n /10.0.0.7:60155\n /[2001:db8::1]:60155\nzk_znode_count\t5\n"
	got := govFilter(in)
	for _, bad := range []string{"::ffff:", "fe80::", "10.0.0.7", "2001:db8"} {
		if strings.Contains(got, bad) {
			t.Errorf("address %q survived the gov filter:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "zk_version") || !strings.Contains(got, "zk_znode_count") {
		t.Errorf("counter lines must survive:\n%s", got)
	}
}
