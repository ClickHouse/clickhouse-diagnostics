package keeper

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	out, err := Collect(dir, []Target{{Host: h, Port: p}}, Options{Now: func() time.Time { return fixed }})
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
	out, err := Collect(dir, []Target{{Host: h, Port: p}}, Options{DialTimeout: time.Second, ReadTimeout: time.Second})
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
	out, err := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{DialTimeout: time.Second, ReadTimeout: 150 * time.Millisecond})
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
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
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
	out, _ := Collect(dir, []Target{{Host: h, Port: p}}, Options{GovSalt: "PR3TESTSALT01"})
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
	want := []Target{{Host: "k1", Port: "9181", Ensemble: "default"}, {Host: "k2", Port: "9181", Ensemble: "default"}}
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
	want := []Target{{Host: "keeper-a.example", Port: "9181", Ensemble: "default"}, {Host: "keeper-b.example", Port: DefaultPort, Ensemble: "default"}, {Host: "keeper-c.example", Port: "2181", Ensemble: "default"}}
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
	merged := MergeTargets([]Target{{Host: "keeper-b.example", Port: DefaultPort, Ensemble: "default"}}, []Target{{Host: "keeper-b.example", Port: DefaultPort, Ensemble: "default"}})
	if len(merged) != 1 {
		t.Errorf("merge must dedupe: %v", merged)
	}
	merged = MergeTargets([]Target{{Host: "keeper-b.example", Port: DefaultPort, Ensemble: "default"}}, want)
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
	out, err := Collect(dir, []Target{{Host: host, Port: port}}, Options{DialTimeout: time.Second, ReadTimeout: 300 * time.Millisecond})
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

// ClickHouse's stock config.xml ships a complete <zookeeper> example inside an
// XML comment (example1 / example2 / example3, port 2181). Reading it as
// configuration made an onprem run against the default -config-dir report four
// Keeper members on a one-member cluster, write three files full of DNS
// failures, and attempt to reach hosts the operator never named. Observed in a
// container built from clickhouse/clickhouse-server:26.7.
func TestTargetsFromConfig_IgnoresCommentedBlocks(t *testing.T) {
	root := t.TempDir()
	cd := filepath.Join(root, "config.d")
	if err := os.Mkdir(cd, 0o755); err != nil {
		t.Fatal(err)
	}
	// The parent config.xml, in the stock shape: the example block is commented.
	if err := os.WriteFile(filepath.Join(root, "config.xml"), []byte(`<clickhouse>
    <!-- ZooKeeper is used to store metadata about replicas, when using Replicated tables.
      -->
    <!--
    <zookeeper>
        <node><host>example1</host><port>2181</port></node>
        <node><host>example2</host><port>2181</port></node>
        <node><host>example3</host><port>2181</port></node>
    </zookeeper>
    -->
</clickhouse>`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The operator's real block, in config.d, uncommented.
	if err := os.WriteFile(filepath.Join(cd, "10-keeper.xml"), []byte(
		`<clickhouse><zookeeper><node><host>keeper-a</host><port>9181</port></node></zookeeper></clickhouse>`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := TargetsFromConfig(cd)
	if len(got) != 1 || got[0] != (Target{Host: "keeper-a", Port: "9181", Ensemble: "default"}) {
		t.Fatalf("targets = %v, want only the uncommented keeper-a:9181", got)
	}
	for _, tg := range got {
		if strings.HasPrefix(tg.Host, "example") {
			t.Errorf("a commented-out example host became a target: %v", tg)
		}
	}
}

// fullReplies is a 26.8 member answering every command the collector sends.
var fullReplies = map[string]string{
	"ruok": "imok",
	"isro": "rw",
	"lgif": "first_log_idx\t9000000000\nfirst_log_term\t18\nlast_log_idx\t9002500000\nlast_log_term\t19\nlast_committed_log_idx\t9002500000\nleader_committed_log_idx\t9002500000\ntarget_committed_log_idx\t9002500000\nlast_snapshot_idx\t9002000000\n",
	"srvr": okReplies["srvr"],
	"mntr": okReplies["mntr"] + "zk_latest_snapshot_size\t2400000000\n",
	"conf": "server_id=2\nreserved_log_items=100000\nsnapshot_distance=1000000\nstale_log_gap=10000\nsnapshot_transfer_chunk_size=0\nwrite_snapshot_version=6\nlog_storage_path=/var/lib/clickhouse-keeper/log\nsnapshot_storage_path=/var/lib/clickhouse-keeper/snapshots\nlog_storage_disk=LocalLogDisk\n",
	"dirs": "snapshot_dir_size: 7200000000\nlog_dir_size: 3372003\n",
}

// recordingKeeper is fakeKeeper that also records the order commands arrive in.
func recordingKeeper(t *testing.T, replies map[string]string, hangOn string) (host, port string, seen func() []string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
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
				mu.Lock()
				got = append(got, string(buf))
				mu.Unlock()
				if string(buf) == hangOn || hangOn == "*" {
					<-done
					return
				}
				io.WriteString(c, replies[string(buf)])
			}(c)
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	return h, p, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), got...) },
		func() { close(done); ln.Close() }
}

func TestCollect_SendsTheCommandsThatAnswerWhenNotServingFirst(t *testing.T) {
	// isro and lgif answer even on a member that is not serving (Keeper's
	// own code checks isServerActive() only for srvr, mntr, conf, dirs…),
	// so they go before anything that can hang or refuse.
	h, p, seen, stop := recordingKeeper(t, fullReplies, "")
	defer stop()
	out, err := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(seen(), ","), "ruok,isro,lgif,srvr,mntr,conf,dirs"; got != want {
		t.Errorf("commands sent %s, want %s", got, want)
	}
	if out[0].Status != "ok" {
		t.Errorf("status = %s (%s)", out[0].Status, out[0].Err)
	}
	b, _ := os.ReadFile(out[0].File)
	for _, must := range []string{"## lgif\nfirst_log_idx\t9000000000", "## conf\nserver_id=2", "snapshot_transfer_chunk_size=0", "## dirs\nsnapshot_dir_size: 7200000000", "## isro\nrw"} {
		if !strings.Contains(string(b), must) {
			t.Errorf("file lacks %q", must)
		}
	}
}

func TestCollect_StopsAMemberAfterItsFirstTimeout(t *testing.T) {
	// A real quorum loss: the member accepted connections but never answered.
	h, p, seen, stop := recordingKeeper(t, fullReplies, "srvr")
	defer stop()
	start := time.Now()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{ReadTimeout: 300 * time.Millisecond})
	if el := time.Since(start); el > 600*time.Millisecond {
		t.Errorf("took %s: after one timeout the remaining commands must be skipped, not each waited out", el)
	}
	if got := strings.Join(seen(), ","); got != "ruok,isro,lgif,srvr" {
		t.Errorf("commands sent %s, want ruok,isro,lgif,srvr (then stop)", got)
	}
	b, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(b), "## lgif\nfirst_log_idx") {
		t.Error("lgif, answered before the hang, is missing")
	}
	if !strings.Contains(string(b), "## mntr\nskipped") {
		t.Errorf("the commands after the timeout are not marked skipped:\n%s", b)
	}
	if out[0].Status != "partial" {
		t.Errorf("status = %s, want partial", out[0].Status)
	}
}

func TestCollect_MembersInParallel(t *testing.T) {
	var targets []Target
	for i := 0; i < 3; i++ {
		h, p, stop := fakeKeeper(t, nil, true)
		defer stop()
		targets = append(targets, Target{Host: h, Port: p})
	}
	start := time.Now()
	out, _ := Collect(t.TempDir(), targets, Options{ReadTimeout: 400 * time.Millisecond})
	if el := time.Since(start); el > 900*time.Millisecond {
		t.Errorf("three hung members took %s — they are asked one after another", el)
	}
	for i, o := range out {
		if o.Target != targets[i] || o.Status != "timeout" {
			t.Errorf("outcome %d = %+v; want target %v in order, status timeout", i, o, targets[i])
		}
	}
}

func TestCollect_NotServing(t *testing.T) {
	const notActive = "This instance is not currently serving requests"
	replies := map[string]string{"ruok": "imok", "isro": "rw", "lgif": fullReplies["lgif"],
		"srvr": notActive, "mntr": notActive, "conf": notActive, "dirs": notActive}
	h, p, stop := fakeKeeper(t, replies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if out[0].Status != "not serving" {
		t.Errorf("status = %s, want not serving — the member answers but has no quorum", out[0].Status)
	}
}

func TestCollect_OlderKeeperWithoutTheNewCommands(t *testing.T) {
	// okReplies answers only ruok/srvr/mntr: a 22.8 Keeper has no lgif.
	h, p, stop := fakeKeeper(t, okReplies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if out[0].Status != "ok" {
		t.Errorf("status = %s; a member answering ruok/srvr/mntr is ok even without the newer commands", out[0].Status)
	}
	b, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(b), "## lgif\n(empty reply") {
		t.Errorf("an absent newer command is not recorded:\n%s", b)
	}
}

func TestCollect_GovRemovesConfPaths(t *testing.T) {
	h, p, stop := fakeKeeper(t, fullReplies, false)
	defer stop()
	gov, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{GovSalt: "PR3TESTSALT01"})
	b, _ := os.ReadFile(gov[0].File)
	s := string(b)
	if strings.Contains(s, "/var/lib/clickhouse-keeper") || !strings.Contains(s, "log_storage_path=REMOVED") || !strings.Contains(s, "snapshot_transfer_chunk_size=0") {
		t.Errorf("gov conf must drop path values and keep the settings:\n%s", s)
	}
	plain, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if b, _ := os.ReadFile(plain[0].File); !strings.Contains(string(b), "log_storage_path=/var/lib/clickhouse-keeper/log") {
		t.Error("outside gov the path is kept")
	}
}

// system.zookeeper_connection lists the default connection AND every
// auxiliary one, each a different ensemble; the name keeps them apart so
// the pre-pass never compares one ensemble's Raft log with another's.
func TestParseTargets_KeepsTheEnsemble(t *testing.T) {
	got := ParseTargets("default\tk1\t9181\naux_zk\tz1\t2181\n")
	want := []Target{{Host: "k1", Port: "9181", Ensemble: "default"}, {Host: "z1", Port: "2181", Ensemble: "aux_zk"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ParseTargets = %+v, want %+v", got, want)
	}
	if !strings.Contains(TargetsQuery, "name") {
		t.Error("TargetsQuery must select the connection name")
	}
}

func TestCollect_WritesTheEnsemble(t *testing.T) {
	h, p, stop := fakeKeeper(t, fullReplies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p, Ensemble: "aux_zk"}}, Options{})
	if b, _ := os.ReadFile(out[0].File); !strings.Contains(string(b), "# ensemble: aux_zk\n") {
		t.Errorf("no ensemble line:\n%s", b)
	}
	gov, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p, Ensemble: "aux_zk"}}, Options{GovSalt: "PR3TESTSALT01"})
	if b, _ := os.ReadFile(gov[0].File); strings.Contains(string(b), "aux_zk") || !strings.Contains(string(b), "# ensemble: ") {
		t.Errorf("gov must hash an auxiliary ensemble's name:\n%s", b)
	}
}

// ---- review round 2: gov identifiers, shared endpoints, auxiliary blocks

func TestCollect_GovConfKeepsOnlyValuesThatIdentifyNothing(t *testing.T) {
	keeperConf := "server_id=2\nsnapshot_distance=100000\nsnapshot_transfer_chunk_size=0\ncompress_snapshots_with_zstd_format=true\nraft_logs_level=information\n" +
		"log_storage_path=/data/coordination/log\nlog_storage_disk=acme_keeper_disk\nsnapshot_storage_disk=s3_plain_acme\n"
	zkConf := "clientPort=2181\nclientPortAddress=0.0.0.0/0.0.0.0:2181\ndataDir=/var/lib/acme-zk/data\ndataLogDir=/var/lib/acme-zk/log\n" +
		"tickTime=2000\nmaxClientCnxns=60\nserver.1=zk1.acme.internal:2888:3888:participant\nserver.2=10.20.30.41:2888:3888:participant\nserver.3=acmezk3\nsecureClientPortAddress=zk-acme.internal:2281\n"
	for name, conf := range map[string]string{"keeper": keeperConf, "zookeeper": zkConf} {
		replies := map[string]string{"ruok": "imok", "srvr": okReplies["srvr"], "mntr": okReplies["mntr"], "conf": conf}
		h, p, stop := fakeKeeper(t, replies, false)
		out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{GovSalt: "PR3TESTSALT01"})
		stop()
		b, _ := os.ReadFile(out[0].File)
		s := string(b)
		for _, leak := range []string{"/data/", "acme", "zk1.", "10.20.30.41", "0.0.0.0"} {
			if strings.Contains(s, leak) {
				t.Errorf("%s: gov conf carries %q:\n%s", name, leak, s)
			}
		}
		for _, keep := range []string{"snapshot_distance=100000", "snapshot_transfer_chunk_size=0", "compress_snapshots_with_zstd_format=true", "raft_logs_level=information", "tickTime=2000", "maxClientCnxns=60"} {
			if strings.Contains(conf, keep) && !strings.Contains(s, keep) {
				t.Errorf("%s: gov dropped the setting %q", name, keep)
			}
		}
	}
}

func TestCollect_SharedEndpointAcrossEnsemblesGetsTwoFiles(t *testing.T) {
	// Default and auxiliary connections to one host:port (different chroots).
	h, p, stop := fakeKeeper(t, fullReplies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p, Ensemble: "default"}, {Host: h, Port: p, Ensemble: "aux_zk"}}, Options{})
	if out[0].File == out[1].File {
		t.Fatalf("both ensembles wrote %s — one overwrites the other", out[0].File)
	}
	for i, ens := range []string{"default", "aux_zk"} {
		if b, _ := os.ReadFile(out[i].File); !strings.Contains(string(b), "# ensemble: "+ens+"\n") {
			t.Errorf("file %d lost its ensemble %q", i, ens)
		}
	}
	if filepath.Base(out[0].File) != h+"_"+p+".txt" {
		t.Errorf("a default member keeps the existing name, got %s", filepath.Base(out[0].File))
	}
}

func TestTargetsFromConfig_AuxiliaryBlockNamedZookeeperIsNotDefault(t *testing.T) {
	dir := t.TempDir()
	cfg := `<clickhouse>
  <zookeeper><node><host>keeper-main.example</host><port>9181</port></node></zookeeper>
  <auxiliary_zookeepers>
    <zookeeper><node><host>zk-aux.example</host><port>2181</port></node></zookeeper>
  </auxiliary_zookeepers>
</clickhouse>`
	if err := os.WriteFile(filepath.Join(dir, "zk.xml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got := TargetsFromConfig(dir)
	if len(got) != 1 || got[0].Host != "keeper-main.example" {
		t.Errorf("TargetsFromConfig = %+v; the auxiliary block must not be read as the default ensemble", got)
	}
}

// Apache ZooKeeper words it differently ("This ZooKeeper instance is not
// currently serving requests"); a member answering that is not serving
// either, and must not be reported ok.
func TestCollect_ZooKeeperNotServing(t *testing.T) {
	const zk = "This ZooKeeper instance is not currently serving requests"
	h, p, stop := fakeKeeper(t, map[string]string{"ruok": "imok", "srvr": zk, "mntr": zk}, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if out[0].Status != "not serving" {
		t.Errorf("status = %s, want not serving for ZooKeeper's wording", out[0].Status)
	}
}
