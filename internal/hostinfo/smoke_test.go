package hostinfo

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCollect_DegradesGracefully 是可移植性守卫：在非 Linux
// 开发机上 /proc 不存在，每一节都必须报告不可用
// 并附带说明，绝不能 panic，也绝不能声称拥有它没有的数据。
func TestCollect_DegradesGracefully(t *testing.T) {
	r := Collect()
	if r.CollectedAt == "" {
		t.Error("CollectedAt 未设置")
	}
	if r.OS.GoOS == "" || r.OS.Arch == "" {
		t.Error("GoOS/Arch 应始终设置——它们来自运行时")
	}
	// 标记为不可用的节必须说明原因。
	if !r.OS.Available && len(r.Notes) == 0 {
		t.Error("OS 不可用却没有说明原因")
	}
	t.Logf("摘要：\n%s", r.Summary())
	t.Logf("disks=%d procs=%d notes=%v", len(r.Disks), len(r.Processes), r.Notes)
}

func TestParseProcStat_CommWithSpacesAndParens(t *testing.T) {
	// comm 字段可能包含空格和圆括号，因此解析器
	// 以最后一个 ')' 为锚点，而不是按空白切分。
	raw := "1234 (my weird (proc)) S 1 0 0 0 -1 4194304 100 0 0 0 5 6 0 0 20 0 7 0 900 123456 2048 1844"
	p, ok := parseProcStat(raw, 1234, 4096)
	if !ok {
		t.Fatal("未能解析 comm 中含圆括号的 stat 行")
	}
	if p.PID != 1234 || p.PPID != 1 || p.State != "S" {
		t.Errorf("得到 pid=%d ppid=%d state=%q", p.PID, p.PPID, p.State)
	}
	if p.Threads != 7 {
		t.Errorf("threads = %d，期望 7", p.Threads)
	}
	if p.RSSBytes != 2048*4096 {
		t.Errorf("rss = %d，期望 %d", p.RSSBytes, 2048*4096)
	}
}

func TestBracketed(t *testing.T) {
	for in, want := range map[string]string{
		"always [madvise] never": "madvise",
		"[always] madvise never": "always",
		"60\n":                   "60",
		"":                       "",
	} {
		if got := bracketed(in); got != want {
			t.Errorf("bracketed(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestIsClickHouseServer 固定住评审发现 #4：旧的子串匹配
// （"clickhouse"）会命中本诊断工具自身、keeper 和客户端——
// 然后把它们的限制当成服务器的报告出来，而 JSON 中
// 没有任何说明。
func TestIsClickHouseServer(t *testing.T) {
	self := 999
	cases := []struct {
		cmd  string
		pid  int
		want bool
	}{
		{"/usr/bin/clickhouse-server --config-file=/etc/clickhouse-server/config.xml", 1, true},
		{"clickhouse-server", 1, true},
		{"/usr/bin/clickhouse server", 1, true}, // 多工具形式
		{"clickhouse server --config-file=x", 1, true},
		{"/usr/bin/clickhouse-server --daemon", self, false}, // 绝不是自身
		{"./clickhouse-diagnostic -mode onprem", 1, false},   // 本工具
		{"/usr/bin/clickhouse-keeper --config x", 1, false},
		{"clickhouse-client --password secret", 1, false},
		{"clickhouse client", 1, false},
		{"/usr/bin/clickhouse local", 1, false},
		{"grep clickhouse-server", 1, false},
		{"", 1, false},
	}
	for _, c := range cases {
		p := Process{PID: c.pid, Command: c.cmd}
		if got := isClickHouseServer(p, self); got != c.want {
			t.Errorf("isClickHouseServer(%q, pid=%d) = %v，期望 %v", c.cmd, c.pid, got, c.want)
		}
	}
}

// TestRedactArgs 固定住评审发现 #6：argv 常常携带机密，
// 而 bundle 会与支持人员共享。
func TestRedactArgs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"clickhouse-server --password=hunter2 --port 9000",
			"clickhouse-server --password=[redacted] --port 9000"},
		{"x --secret-token abc123 --other ok",
			"x --secret-token [redacted] --other ok"},
		{"x --access_key=AKIA123 --api-key k",
			"x --access_key=[redacted] --api-key [redacted]"},
		{"clickhouse-server --config-file=/etc/clickhouse-server/config.xml",
			"clickhouse-server --config-file=/etc/clickhouse-server/config.xml"},
	}
	for _, c := range cases {
		if got := redactArgs(c.in); got != c.want {
			t.Errorf("redactArgs(%q)\n 得到 %q\n期望 %q", c.in, got, c.want)
		}
	}
}

// TestReadCmdlineRuneBoundary 固定住评审发现 #11：300 字节的截断可能
// 撕裂 UTF-8 序列，encoding/json 随后会将其替换为 U+FFFD。
func TestReadCmdlineRuneBoundary(t *testing.T) {
	dir := t.TempDir()
	// 299 个 ASCII 字节，然后是一个跨越 300 截断点的 3 字节 rune。
	long := strings.Repeat("a", 299) + "€€€"
	path := dir + "/cmdline"
	if err := os.WriteFile(path, []byte(long+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readCmdline(path)
	if !utf8.ValidString(got) {
		t.Fatalf("readCmdline 产生了无效 UTF-8：%q", got[len(got)-8:])
	}
}
