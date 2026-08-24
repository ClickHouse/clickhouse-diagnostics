package hostinfo

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCollect_DegradesGracefully is the portability guard: on a non-Linux
// dev machine /proc is absent and every section must report unavailable
// with a note, never panic and never claim data it doesn't have.
func TestCollect_DegradesGracefully(t *testing.T) {
	r := Collect()
	if r.CollectedAt == "" {
		t.Error("CollectedAt not set")
	}
	if r.OS.GoOS == "" || r.OS.Arch == "" {
		t.Error("GoOS/Arch should always be set — they come from the runtime")
	}
	// A section marked unavailable must say why.
	if !r.OS.Available && len(r.Notes) == 0 {
		t.Error("OS unavailable but no note explaining why")
	}
	t.Logf("summary:\n%s", r.Summary())
	t.Logf("disks=%d procs=%d notes=%v", len(r.Disks), len(r.Processes), r.Notes)
}

func TestParseProcStat_CommWithSpacesAndParens(t *testing.T) {
	// The comm field can contain spaces and parentheses, which is why the
	// parser anchors on the LAST ')' instead of splitting on whitespace.
	raw := "1234 (my weird (proc)) S 1 0 0 0 -1 4194304 100 0 0 0 5 6 0 0 20 0 7 0 900 123456 2048 1844"
	p, ok := parseProcStat(raw, 1234, 4096)
	if !ok {
		t.Fatal("failed to parse a stat line with parens in comm")
	}
	if p.PID != 1234 || p.PPID != 1 || p.State != "S" {
		t.Errorf("got pid=%d ppid=%d state=%q", p.PID, p.PPID, p.State)
	}
	if p.Threads != 7 {
		t.Errorf("threads = %d, want 7", p.Threads)
	}
	if p.RSSBytes != 2048*4096 {
		t.Errorf("rss = %d, want %d", p.RSSBytes, 2048*4096)
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
			t.Errorf("bracketed(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsClickHouseServer pins review finding #4: the old substring match
// ("clickhouse") caught this diagnostic tool itself, keeper, and clients —
// and then reported THEIR limits as the server's, with nothing in the JSON
// to say so.
func TestIsClickHouseServer(t *testing.T) {
	self := 999
	cases := []struct {
		cmd  string
		pid  int
		want bool
	}{
		{"/usr/bin/clickhouse-server --config-file=/etc/clickhouse-server/config.xml", 1, true},
		{"clickhouse-server", 1, true},
		{"/usr/bin/clickhouse server", 1, true}, // multi-tool form
		{"clickhouse server --config-file=x", 1, true},
		{"/usr/bin/clickhouse-server --daemon", self, false}, // never self
		{"./clickhouse-diagnostic -mode onprem", 1, false},   // this tool
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
			t.Errorf("isClickHouseServer(%q, pid=%d) = %v, want %v", c.cmd, c.pid, got, c.want)
		}
	}
}

// TestRedactArgs pins review finding #6: argv routinely carries secrets and
// the bundle is shared with support.
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
			t.Errorf("redactArgs(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// TestReadCmdlineRuneBoundary pins review finding #11: a 300-byte cut can
// tear a UTF-8 sequence, which encoding/json then replaces with U+FFFD.
func TestReadCmdlineRuneBoundary(t *testing.T) {
	dir := t.TempDir()
	// 299 ASCII bytes then a 3-byte rune spanning the 300 cut.
	long := strings.Repeat("a", 299) + "€€€"
	path := dir + "/cmdline"
	if err := os.WriteFile(path, []byte(long+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readCmdline(path)
	if !utf8.ValidString(got) {
		t.Fatalf("readCmdline produced invalid UTF-8: %q", got[len(got)-8:])
	}
}
