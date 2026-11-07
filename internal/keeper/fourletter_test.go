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

// fakeKeeper 像 Keeper 一样应答四字命令：读取命令，写入回复，关闭。
// 某条命令的空回复模拟它不在 four_letter_word_white_list 中。
// hang 让它接受连接但从不回答。
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
		t.Fatalf("期望一个 ok 结果，得到 %+v", out)
	}
	want := filepath.Join(dir, DirName, h+"_"+p+".txt")
	if out[0].File != want {
		t.Errorf("file = %s，期望 %s", out[0].File, want)
	}
	b, _ := os.ReadFile(want)
	s := string(b)
	for _, must := range []string{"# target: " + h + ":" + p, "# collected_at: 2026-10-02T12:00:00Z", "## ruok\nimok", "## srvr\nClickHouse Keeper version", "## mntr\nzk_version", "zk_server_state\tstandalone"} {
		if !strings.Contains(s, must) {
			t.Errorf("文件缺少 %q：\n%s", must, s)
		}
	}
	if out[0].Bytes != int64(len(b)) {
		t.Errorf("Bytes = %d，文件是 %d", out[0].Bytes, len(b))
	}
}

func TestCollect_Refused(t *testing.T) {
	// 监听、取走端口、关闭：现在那里没有东西应答。
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
		t.Fatalf("status = %s（%s），期望 refused", out[0].Status, out[0].Err)
	}
	// 文件仍然存在，以便 bundle 记录这次尝试。
	b, rerr := os.ReadFile(out[0].File)
	if rerr != nil || !strings.Contains(string(b), "error: 连接被拒绝") {
		t.Errorf("尝试未记录在文件中：%v %q", rerr, b)
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
		t.Fatalf("status = %s（%s），期望 timeout", out[0].Status, out[0].Err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("三条命令在 150 ms 读取超时下花了 %s——截止时间未生效", el)
	}
}

func TestCollect_PartialWhenCommandDisabled(t *testing.T) {
	replies := map[string]string{"ruok": "imok", "srvr": okReplies["srvr"], "mntr": ""}
	h, p, stop := fakeKeeper(t, replies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if out[0].Status != "partial" {
		t.Fatalf("status = %s，期望 partial", out[0].Status)
	}
	b, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(b), "four_letter_word_white_list") {
		t.Errorf("空回复未得到解释：\n%s", b)
	}
}

func TestCollect_GovHashesHostAndFiltersAddresses(t *testing.T) {
	// 一条确实携带客户端地址的回复，用以证明 gov 过滤器会
	// 移除它，即使 srvr/mntr 目前不会打印这种地址。
	replies := map[string]string{
		"ruok": "imok",
		"srvr": "ClickHouse Keeper version: v26.7.5.10\nClients:\n 10.0.0.7:60155(recved=0,sent=0)\nMode: leader\n",
		"mntr": "zk_version\tv26.7\nzk_server_state\tleader\n",
	}
	h, p, stop := fakeKeeper(t, replies, false)
	defer stop()
	dir := t.TempDir()
	out, _ := Collect(dir, []Target{{Host: h, Port: p}}, Options{GovSalt: "PR3TESTSALT01"})
	// 与 ClickHouse 中 hex(SHA256(concat(host, salt))) 相同的形式：
	// 对于 '127.0.0.1' + 'PR3TESTSALT01'，服务器返回此值。
	if h == "127.0.0.1" {
		const want = "348AB0C01381EBFC9D3E72FA52E6D8A4CE3E6A3D269119D2FA7794C924C532AC"
		if out[0].Label != want {
			t.Errorf("label = %s，期望与 ClickHouse 兼容的 %s", out[0].Label, want)
		}
	}
	if strings.Contains(filepath.Base(out[0].File), h) {
		t.Errorf("gov 文件名携带了原始主机：%s", out[0].File)
	}
	b, _ := os.ReadFile(out[0].File)
	s := string(b)
	if strings.Contains(s, h) || strings.Contains(s, "10.0.0.7") {
		t.Errorf("gov 文件携带了地址：\n%s", s)
	}
	if !strings.Contains(s, "Mode: leader") || !strings.Contains(s, "zk_server_state\tleader") {
		t.Errorf("gov 过滤器丢弃了地址行之外的内容：\n%s", s)
	}
}

func TestParseTargets(t *testing.T) {
	got := ParseTargets("k1\t9181\nk2\t9181\nk1\t9181\n\nbroken line\n\t9181\n")
	want := []Target{{Host: "k1", Port: "9181", Ensemble: "default"}, {Host: "k2", Port: "9181", Ensemble: "default"}}
	if len(got) != len(want) {
		t.Fatalf("得到 %+v，期望 %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v，期望 %+v", i, got[i], want[i])
		}
	}
	if ParseTargets("") != nil {
		t.Error("空输入必须不产生任何目标")
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
			t.Errorf("Resolve(%q,%q) = skip %v warn %q err %v；期望 skip %v warn %v err %v", c.setting, c.mode, skip, warn, err, c.skip, c.warn, c.err)
		}
	}
}

func TestFileName_SafeForIPv6(t *testing.T) {
	if got := fileName("fd00::1", "9181"); strings.Contains(got, ":") {
		t.Errorf("%s 中冒号仍然存在", got)
	}
}

// ensemble 在服务器配置中声明，而不是在
// system.zookeeper_connection 中（后者只命名已连接的成员）。config.d/
// 和相邻的 config.xml 都会被读取；没有 <port> 的节点使用
// ZooKeeper 默认值；变量替换会被跳过。
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
		t.Fatalf("targets = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target %d = %v，期望 %v", i, got[i], want[i])
		}
	}
	// 非 *.d 目录绝不能提供其父目录。
	if got := TargetsFromConfig(root); len(got) != 1 || got[0].Host != "keeper-c.example" {
		t.Errorf("普通目录：%v，期望只有 config.xml 中的节点", got)
	}
	if TargetsFromConfig("") != nil || TargetsFromConfig(filepath.Join(root, "missing")) != nil {
		t.Error("空目录或不可读目录必须返回 nil")
	}
	merged := MergeTargets([]Target{{Host: "keeper-b.example", Port: DefaultPort, Ensemble: "default"}}, []Target{{Host: "keeper-b.example", Port: DefaultPort, Ensemble: "default"}})
	if len(merged) != 1 {
		t.Errorf("合并必须去重：%v", merged)
	}
	merged = MergeTargets([]Target{{Host: "keeper-b.example", Port: DefaultPort, Ensemble: "default"}}, want)
	if len(merged) != 3 || merged[0].Host != "keeper-b.example" {
		t.Errorf("合并保留已连接者在前并加入其余：%v", merged)
	}
}

// 回答部分 mntr 然后停顿到截止时间的成员属于 timeout，
// 而不是 ok：部分文本会保留，但标记为已截断。
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
				<-stop // 永不结束、永不关闭：必须由客户端的截止时间终止它
			}(c)
		}
	}()
	defer close(stop)
	host, port, _ := net.SplitHostPort(ln.Addr().String())

	dir := t.TempDir()
	out, err := Collect(dir, []Target{{Host: host, Port: port}}, Options{DialTimeout: time.Second, ReadTimeout: 300 * time.Millisecond})
	if err != nil || len(out) != 1 {
		t.Fatalf("collect：%v %v", out, err)
	}
	if out[0].Status != "timeout" {
		t.Errorf("status = %q，期望 timeout（部分回复绝不能计为 ok）", out[0].Status)
	}
	body, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(body), "zk_avg_latency\t1") || !strings.Contains(string(body), "回复已截断") {
		t.Errorf("文件应保留部分回复并标记为已截断：\n%s", body)
	}
}

// IPv4 映射地址和带作用域的 IPv6 客户端地址也是地址。
func TestGovFilter_IPv6Forms(t *testing.T) {
	in := "zk_version\tv1\n /[::ffff:10.0.0.7]:60155[0](queued=0)\n /[fe80::1%eth0]:2181\n /10.0.0.7:60155\n /[2001:db8::1]:60155\nzk_znode_count\t5\n"
	got := govFilter(in)
	for _, bad := range []string{"::ffff:", "fe80::", "10.0.0.7", "2001:db8"} {
		if strings.Contains(got, bad) {
			t.Errorf("地址 %q 在 gov 过滤器中幸存：\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "zk_version") || !strings.Contains(got, "zk_znode_count") {
		t.Errorf("计数器行必须保留：\n%s", got)
	}
}

// ClickHouse 自带的 config.xml 在 XML 注释中带有完整的
// <zookeeper> 示例（example1 / example2 / example3，端口 2181）。
// 把它当作配置读取，会让针对默认 -config-dir 的 onprem 运行在
// 单成员集群上报告四个 Keeper 成员、写出三个满是 DNS 失败的
// 文件，并尝试连接运维人员从未指定的主机。该问题在由
// clickhouse/clickhouse-server:26.7 构建的容器中观察到。
func TestTargetsFromConfig_IgnoresCommentedBlocks(t *testing.T) {
	root := t.TempDir()
	cd := filepath.Join(root, "config.d")
	if err := os.Mkdir(cd, 0o755); err != nil {
		t.Fatal(err)
	}
	// 父目录的 config.xml，采用自带配置的形态：示例块被注释掉。
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
	// 运维人员的真实配置块，位于 config.d，未注释。
	if err := os.WriteFile(filepath.Join(cd, "10-keeper.xml"), []byte(
		`<clickhouse><zookeeper><node><host>keeper-a</host><port>9181</port></node></zookeeper></clickhouse>`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := TargetsFromConfig(cd)
	if len(got) != 1 || got[0] != (Target{Host: "keeper-a", Port: "9181", Ensemble: "default"}) {
		t.Fatalf("targets = %v，期望只有未注释的 keeper-a:9181", got)
	}
	for _, tg := range got {
		if strings.HasPrefix(tg.Host, "example") {
			t.Errorf("被注释掉的示例主机变成了目标：%v", tg)
		}
	}
}

// fullReplies 是一个 26.8 成员对各条命令的完整应答，
// 覆盖采集器发送的所有命令。
var fullReplies = map[string]string{
	"ruok": "imok",
	"isro": "rw",
	"lgif": "first_log_idx\t9000000000\nfirst_log_term\t18\nlast_log_idx\t9002500000\nlast_log_term\t19\nlast_committed_log_idx\t9002500000\nleader_committed_log_idx\t9002500000\ntarget_committed_log_idx\t9002500000\nlast_snapshot_idx\t9002000000\n",
	"srvr": okReplies["srvr"],
	"mntr": okReplies["mntr"] + "zk_latest_snapshot_size\t2400000000\n",
	"conf": "server_id=2\nreserved_log_items=100000\nsnapshot_distance=1000000\nstale_log_gap=10000\nsnapshot_transfer_chunk_size=0\nwrite_snapshot_version=6\nlog_storage_path=/var/lib/clickhouse-keeper/log\nsnapshot_storage_path=/var/lib/clickhouse-keeper/snapshots\nlog_storage_disk=LocalLogDisk\n",
	"dirs": "snapshot_dir_size: 7200000000\nlog_dir_size: 3372003\n",
}

// recordingKeeper 是同时记录命令到达顺序的 fakeKeeper。
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
	// isro 和 lgif 即使在不提供服务的成员上也会应答（Keeper
	// 自己的代码只对 srvr、mntr、conf、dirs……检查 isServerActive()），
	// 因此它们排在任何可能挂起或拒绝的命令之前。
	h, p, seen, stop := recordingKeeper(t, fullReplies, "")
	defer stop()
	out, err := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(seen(), ","), "ruok,isro,lgif,srvr,mntr,conf,dirs"; got != want {
		t.Errorf("发送的命令 %s，期望 %s", got, want)
	}
	if out[0].Status != "ok" {
		t.Errorf("status = %s（%s）", out[0].Status, out[0].Err)
	}
	b, _ := os.ReadFile(out[0].File)
	for _, must := range []string{"## lgif\nfirst_log_idx\t9000000000", "## conf\nserver_id=2", "snapshot_transfer_chunk_size=0", "## dirs\nsnapshot_dir_size: 7200000000", "## isro\nrw"} {
		if !strings.Contains(string(b), must) {
			t.Errorf("文件缺少 %q", must)
		}
	}
}

func TestCollect_StopsAMemberAfterItsFirstTimeout(t *testing.T) {
	// 真实的 quorum 丢失：成员接受了连接但从不回答。
	h, p, seen, stop := recordingKeeper(t, fullReplies, "srvr")
	defer stop()
	start := time.Now()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{ReadTimeout: 300 * time.Millisecond})
	if el := time.Since(start); el > 600*time.Millisecond {
		t.Errorf("耗时 %s：一次超时后其余命令必须跳过，而不是逐个等满超时", el)
	}
	if got := strings.Join(seen(), ","); got != "ruok,isro,lgif,srvr" {
		t.Errorf("发送的命令 %s，期望 ruok,isro,lgif,srvr（然后停止）", got)
	}
	b, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(b), "## lgif\nfirst_log_idx") {
		t.Error("在挂起之前应答的 lgif 缺失")
	}
	if !strings.Contains(string(b), "## mntr\nskipped") {
		t.Errorf("超时之后的命令未被标记为 skipped：\n%s", b)
	}
	if out[0].Status != "partial" {
		t.Errorf("status = %s，期望 partial", out[0].Status)
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
		t.Errorf("三个挂起成员耗时 %s——它们被逐个询问", el)
	}
	for i, o := range out {
		if o.Target != targets[i] || o.Status != "timeout" {
			t.Errorf("结果 %d = %+v；期望按顺序对应目标 %v，status 为 timeout", i, o, targets[i])
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
		t.Errorf("status = %s，期望 not serving——成员有应答但没有 quorum", out[0].Status)
	}
}

func TestCollect_OlderKeeperWithoutTheNewCommands(t *testing.T) {
	// okReplies 只应答 ruok/srvr/mntr：22.8 的 Keeper 没有 lgif。
	h, p, stop := fakeKeeper(t, okReplies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if out[0].Status != "ok" {
		t.Errorf("status = %s；应答 ruok/srvr/mntr 的成员即使没有较新命令也是 ok", out[0].Status)
	}
	b, _ := os.ReadFile(out[0].File)
	if !strings.Contains(string(b), "## lgif\n(empty reply") {
		t.Errorf("缺失的较新命令未被记录：\n%s", b)
	}
}

func TestCollect_GovRemovesConfPaths(t *testing.T) {
	h, p, stop := fakeKeeper(t, fullReplies, false)
	defer stop()
	gov, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{GovSalt: "PR3TESTSALT01"})
	b, _ := os.ReadFile(gov[0].File)
	s := string(b)
	if strings.Contains(s, "/var/lib/clickhouse-keeper") || !strings.Contains(s, "log_storage_path=REMOVED") || !strings.Contains(s, "snapshot_transfer_chunk_size=0") {
		t.Errorf("gov conf 必须丢弃路径值并保留设置：\n%s", s)
	}
	plain, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if b, _ := os.ReadFile(plain[0].File); !strings.Contains(string(b), "log_storage_path=/var/lib/clickhouse-keeper/log") {
		t.Error("gov 之外路径会保留")
	}
}

// system.zookeeper_connection 列出默认连接以及每个
// 辅助连接，各自属于不同的 ensemble；名称将它们区分开，
// 使预检脚本绝不会把一个 ensemble 的 Raft 日志与另一个 ensemble 的
// 相比较。
func TestParseTargets_KeepsTheEnsemble(t *testing.T) {
	got := ParseTargets("default\tk1\t9181\naux_zk\tz1\t2181\n")
	want := []Target{{Host: "k1", Port: "9181", Ensemble: "default"}, {Host: "z1", Port: "2181", Ensemble: "aux_zk"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ParseTargets = %+v，期望 %+v", got, want)
	}
	if !strings.Contains(TargetsQuery, "name") {
		t.Error("TargetsQuery 必须选出连接名称")
	}
}

func TestCollect_WritesTheEnsemble(t *testing.T) {
	h, p, stop := fakeKeeper(t, fullReplies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p, Ensemble: "aux_zk"}}, Options{})
	if b, _ := os.ReadFile(out[0].File); !strings.Contains(string(b), "# ensemble: aux_zk\n") {
		t.Errorf("没有 ensemble 行：\n%s", b)
	}
	gov, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p, Ensemble: "aux_zk"}}, Options{GovSalt: "PR3TESTSALT01"})
	if b, _ := os.ReadFile(gov[0].File); strings.Contains(string(b), "aux_zk") || !strings.Contains(string(b), "# ensemble: ") {
		t.Errorf("gov 必须哈希辅助 ensemble 的名称：\n%s", b)
	}
}

// ---- 评审第二轮：gov 标识符、共享端点、辅助配置块

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
				t.Errorf("%s：gov conf 携带了 %q：\n%s", name, leak, s)
			}
		}
		for _, keep := range []string{"snapshot_distance=100000", "snapshot_transfer_chunk_size=0", "compress_snapshots_with_zstd_format=true", "raft_logs_level=information", "tickTime=2000", "maxClientCnxns=60"} {
			if strings.Contains(conf, keep) && !strings.Contains(s, keep) {
				t.Errorf("%s：gov 丢弃了设置 %q", name, keep)
			}
		}
	}
}

func TestCollect_SharedEndpointAcrossEnsemblesGetsTwoFiles(t *testing.T) {
	// 默认连接和辅助连接指向同一个 host:port（不同的 chroot）。
	h, p, stop := fakeKeeper(t, fullReplies, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p, Ensemble: "default"}, {Host: h, Port: p, Ensemble: "aux_zk"}}, Options{})
	if out[0].File == out[1].File {
		t.Fatalf("两个 ensemble 都写入了 %s——一个覆盖了另一个", out[0].File)
	}
	for i, ens := range []string{"default", "aux_zk"} {
		if b, _ := os.ReadFile(out[i].File); !strings.Contains(string(b), "# ensemble: "+ens+"\n") {
			t.Errorf("文件 %d 丢失了其 ensemble %q", i, ens)
		}
	}
	if filepath.Base(out[0].File) != h+"_"+p+".txt" {
		t.Errorf("默认成员保留既有名称，得到 %s", filepath.Base(out[0].File))
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
		t.Errorf("TargetsFromConfig = %+v；辅助配置块绝不能被当作默认 ensemble 读取", got)
	}
}

// Apache ZooKeeper 的措辞不同（"This ZooKeeper instance is not
// currently serving requests"）；这样回答的成员同样不在服务，
// 绝不能报告为 ok。
func TestCollect_ZooKeeperNotServing(t *testing.T) {
	const zk = "This ZooKeeper instance is not currently serving requests"
	h, p, stop := fakeKeeper(t, map[string]string{"ruok": "imok", "srvr": zk, "mntr": zk}, false)
	defer stop()
	out, _ := Collect(t.TempDir(), []Target{{Host: h, Port: p}}, Options{})
	if out[0].Status != "not serving" {
		t.Errorf("status = %s，ZooKeeper 的措辞应判为 not serving", out[0].Status)
	}
}
