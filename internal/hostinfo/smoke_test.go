package hostinfo

import "testing"

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
