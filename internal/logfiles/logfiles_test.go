package logfiles

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestLogPathsFromConfig covers the discovery that is the whole point of
// this collector: following the configured location rather than assuming
// the packaged one.
func TestLogPathsFromConfig(t *testing.T) {
	root := t.TempDir()
	confD := filepath.Join(root, "config.d")
	write(t, filepath.Join(root, "config.xml"), `
<clickhouse>
  <logger>
    <level>debug</level>
    <log>/data/ch/logs/clickhouse-server.log</log>
    <errorlog>/data/ch/logs/clickhouse-server.err.log</errorlog>
  </logger>
</clickhouse>`)
	write(t, filepath.Join(confD, "override.xml"), `
<clickhouse><logger>
  <log>/mnt/big/other.log</log>
</logger></clickhouse>`)
	// Must be ignored: unresolved substitution and a relative path.
	write(t, filepath.Join(confD, "weird.xml"), `
<clickhouse><logger>
  <log>{some_macro}/x.log</log>
  <errorlog>relative/path.log</errorlog>
</logger></clickhouse>`)

	got := LogPathsFromConfig(confD)
	want := map[string]bool{
		"/data/ch/logs/clickhouse-server.log":     true,
		"/data/ch/logs/clickhouse-server.err.log": true,
		"/mnt/big/other.log":                      true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d absolute paths", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q (substitutions and relative paths must be skipped)", p)
		}
	}
}

func TestDiscoverDirs_ExplicitDirWins(t *testing.T) {
	dir := t.TempDir()
	res := &Result{}
	got := DiscoverDirs(Options{Dir: dir, ConfigDir: "/nonexistent"}, res)
	if len(got) != 1 || got[0] != filepath.Clean(dir) {
		t.Errorf("explicit dir should win, got %v", got)
	}

	// A bad explicit dir must produce a note, not silently fall back —
	// otherwise the operator thinks their flag was honoured.
	res2 := &Result{}
	if got := DiscoverDirs(Options{Dir: "/no/such/dir"}, res2); len(got) != 0 {
		t.Errorf("expected no dirs, got %v", got)
	}
	if len(res2.Notes) == 0 {
		t.Error("a non-existent -logs-dir must be reported")
	}
}

func TestCollect_CopiesAndTailTruncates(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	write(t, filepath.Join(src, "clickhouse-server.log"), "line one\nline two\n")
	// Oversized file: 4 KiB of numbered lines, capped to 1 KiB.
	var big strings.Builder
	for i := 0; big.Len() < 4096; i++ {
		big.WriteString("0123456789 filler line to exceed the cap\n")
	}
	write(t, filepath.Join(src, "big.log"), big.String())
	// Non-log files and archives must be skipped by default.
	write(t, filepath.Join(src, "notes.txt"), "ignore me")
	write(t, filepath.Join(src, "old.log.gz"), "gzip-ish")

	res, err := Collect(dest, Options{Dir: src, MaxBytesPerFile: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Copied) != 2 {
		t.Fatalf("copied %v, want exactly the two .log files", res.Copied)
	}
	if len(res.Truncated) != 1 || res.Truncated[0] != "big.log" {
		t.Errorf("truncated = %v, want [big.log]", res.Truncated)
	}

	small, err := os.ReadFile(filepath.Join(dest, "logs", "clickhouse-server.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(small) != "line one\nline two\n" {
		t.Errorf("small file should be copied verbatim, got %q", small)
	}

	bigOut, err := os.ReadFile(filepath.Join(dest, "logs", "big.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(bigOut), "### support-diagnostic: TRUNCATED") {
		t.Error("truncated file must start with the truncation marker so nobody " +
			"reads the first surviving line as the start of the log")
	}
	// Tail semantics: the END of the original must survive.
	if !strings.HasSuffix(string(bigOut), "filler line to exceed the cap\n") {
		t.Error("truncation must keep the tail, not the head")
	}
	// And it must resume on a line boundary, not mid-message.
	body := strings.SplitN(string(bigOut), "\n", 2)[1]
	if body != "" && !strings.HasPrefix(body, "0123456789") {
		t.Errorf("expected a clean line boundary after the marker, got %.40q", body)
	}
}

func TestCollect_MissingDirIsNoteNotError(t *testing.T) {
	// Running remotely is normal; a missing log dir must not fail the run.
	res, err := Collect(t.TempDir(), Options{Dir: "/definitely/not/here"})
	if err != nil {
		t.Fatalf("missing dir should not be an error, got %v", err)
	}
	if len(res.Copied) != 0 || len(res.Notes) == 0 {
		t.Errorf("expected 0 copied and an explanatory note, got %+v", res)
	}
}

func TestCollect_IncludeArchives(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "a.log"), "x")
	write(t, filepath.Join(src, "a.log.gz"), "y")
	res, err := Collect(dest, Options{Dir: src, IncludeArchives: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Copied) != 2 {
		t.Errorf("with IncludeArchives both files should be copied, got %v", res.Copied)
	}
}

// TestSymlinksNotFollowed: the log directory is typically writable by the
// clickhouse service account while the diagnostic often runs as root. A
// planted `evil.log -> /etc/shadow` must not be copied into a bundle that
// gets shared with support.
func TestSymlinksNotFollowed(t *testing.T) {
	logDir := t.TempDir()
	outDir := t.TempDir()

	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("root:$6$hash"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "real.log"), []byte("2026.08.20 ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(logDir, "evil.log")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	res, err := Collect(outDir, Options{Dir: logDir, MaxBytesPerFile: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range res.Copied {
		if strings.Contains(name, "evil") {
			t.Fatalf("symlink was followed and copied: %v", res.Copied)
		}
	}
	if len(res.Copied) != 1 || !strings.Contains(res.Copied[0], "real") {
		t.Errorf("expected exactly the real log, got %v", res.Copied)
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "evil.log") {
			found = true
		}
	}
	if !found {
		t.Error("skipped symlink was not reported in Notes; silent omission " +
			"reads as a missing log rather than a refusal")
	}
}

// TestTruncationHeaderIsExact pins the two claims the header makes after
// the copilot-review fix: the "kept last N bytes" figure must equal the
// actual payload that follows it (line alignment shaves up to one line off
// the nominal cap), and Result.TotalBytes must count what was written to
// the bundle — header included — not what was read from the source.
func TestTruncationHeaderIsExact(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	var big strings.Builder
	for big.Len() < 4096 {
		big.WriteString("0123456789 filler line to exceed the cap\n")
	}
	write(t, filepath.Join(src, "big.log"), big.String())

	res, err := Collect(dest, Options{Dir: src, MaxBytesPerFile: 1024})
	if err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(filepath.Join(dest, "logs", "big.log"))
	if err != nil {
		t.Fatal(err)
	}
	head, payload, found := strings.Cut(string(out), "###\n")
	if !found {
		t.Fatalf("no truncation header in %q", string(out)[:80])
	}
	m := regexp.MustCompile(`original (\d+) bytes, kept last (\d+) bytes`).FindStringSubmatch(head)
	if m == nil {
		t.Fatalf("header shape changed: %q", head)
	}
	original, _ := strconv.Atoi(m[1])
	kept, _ := strconv.Atoi(m[2])

	if original != big.Len() {
		t.Errorf("header says original %d bytes, source was %d", original, big.Len())
	}
	if kept != len(payload) {
		t.Errorf("header says kept %d bytes, payload is actually %d", kept, len(payload))
	}
	if kept > 1024 {
		t.Errorf("kept %d exceeds the 1024-byte cap", kept)
	}
	if !strings.HasPrefix(payload, "0123456789") {
		t.Errorf("payload does not start on a line boundary: %q", payload[:40])
	}
	if res.TotalBytes != int64(len(out)) {
		t.Errorf("TotalBytes = %d, bundle file is %d bytes (header must be counted)",
			res.TotalBytes, len(out))
	}
}

// TestConfigParentScanOnlyForDotD pins review finding #8: with
// -config-dir /etc/clickhouse-server the parent is /etc, and any unrelated
// /etc/*.xml carrying a <log> element would volunteer a directory whose
// files then land in the bundle. The parent is only scanned when the given
// directory is a *.d overlay (which legitimately sits beside config.xml).
func TestConfigParentScanOnlyForDotD(t *testing.T) {
	parent := t.TempDir()
	confDir := filepath.Join(parent, "clickhouse-server")
	write(t, filepath.Join(confDir, "config.xml"), "<clickhouse><logger><log>/data/ch/a.log</log></logger></clickhouse>")
	// An unrelated file in the PARENT (think /etc/foo.xml) with a log path.
	write(t, filepath.Join(parent, "unrelated.xml"), "<clickhouse><logger><log>/tmp/stolen.log</log></logger></clickhouse>")

	got := LogPathsFromConfig(confDir)
	for _, p := range got {
		if p == "/tmp/stolen.log" {
			t.Fatalf("parent of a non-.d config dir was scanned: %v", got)
		}
	}
	if len(got) != 1 || got[0] != "/data/ch/a.log" {
		t.Errorf("expected only the config dir's own path, got %v", got)
	}

	// But for a config.d directory, the parent (which holds config.xml)
	// must still be scanned — that is where the main log path usually is.
	confD := filepath.Join(confDir, "config.d")
	write(t, filepath.Join(confD, "override.xml"), "<clickhouse><logger><log>/data/ch/b.log</log></logger></clickhouse>")
	got = LogPathsFromConfig(confD)
	want := map[string]bool{"/data/ch/a.log": true, "/data/ch/b.log": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("config.d must scan its parent too, got %v", got)
	}
}
