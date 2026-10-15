package internal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A tar entry name is slash-separated by definition. The collector built it
// with filepath.Join, so the Windows build wrote
// `clickhouse_backup_…\system.parts_….jsonl` — not a path inside the archive.
// Every other platform then extracts a single file whose name contains a
// backslash rather than a directory, and the dashboard, the analysis skill and
// plain `tar` all see an empty bundle. A customer bundle collected that way
// cost a support case two round trips, so the transformation is pinned here
// with the Windows-shaped input it has to survive.
//
// Only the collecting platform's separator is converted. On Unix a backslash
// is an ordinary filename character, and configuration/ and logs/ copy the
// server's own files under their real names: `a\b.xml` and `a/b.xml` are two
// files and must stay two entries.
func TestEntryName_ConvertsOnlyTheHostSeparator(t *testing.T) {
	for _, c := range []struct {
		sep             rune
		base, rel, want string
	}{
		// What filepath.Rel returns on Windows:
		{'\\', "clickhouse_backup_20261006_084257", `system.parts_20261006_084257.jsonl`, "clickhouse_backup_20261006_084257/system.parts_20261006_084257.jsonl"},
		{'\\', "clickhouse_backup_20261006_084257", `configuration\config.xml`, "clickhouse_backup_20261006_084257/configuration/config.xml"},
		{'\\', "clickhouse_backup_20261006_084257", `logs\clickhouse-server.err.log`, "clickhouse_backup_20261006_084257/logs/clickhouse-server.err.log"},
		{'\\', "bundle", `a\b\c\deep.jsonl`, "bundle/a/b/c/deep.jsonl"},
		// What it returns everywhere else — unchanged, literal backslash kept:
		{'/', "bundle", "configuration/config.xml", "bundle/configuration/config.xml"},
		{'/', "bundle", `configuration/a\b.xml`, `bundle/configuration/a\b.xml`},
		// The directory entry itself:
		{'\\', "bundle", ".", "bundle"},
		{'/', "bundle", "", "bundle"},
	} {
		if got := entryName(c.base, c.rel, c.sep); got != c.want {
			t.Errorf("entryName(%q, %q, %q) = %q, want %q", c.base, c.rel, c.sep, got, c.want)
		}
	}
	// Production uses this platform's separator.
	if got, want := archiveEntryName("bundle", "x.jsonl"), entryName("bundle", "x.jsonl", filepath.Separator); got != want {
		t.Errorf("archiveEntryName = %q, want %q", got, want)
	}
}

// End to end: the host separator never reaches the archive, a Unix filename
// with a literal backslash keeps it, and the structure survives a round trip.
func TestCreateArchive_NoBackslashInEntryNames(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "clickhouse_backup_20260101_000000")
	for _, d := range []string{"configuration", "logs"} {
		if err := os.MkdirAll(filepath.Join(src, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{
		"system.version_20260101_000000.jsonl",
		filepath.Join("configuration", "config.xml"),
		filepath.Join("logs", "clickhouse-server.log"),
	}
	literal := ""
	if filepath.Separator == '/' {
		// Two distinct files on Unix: configuration/a\b.xml and configuration/a/b.xml.
		if err := os.MkdirAll(filepath.Join(src, "configuration", "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		literal = "clickhouse_backup_20260101_000000/configuration/a\\b.xml"
		files = append(files, filepath.Join("configuration", `a\b.xml`), filepath.Join("configuration", "a", "b.xml"))
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(src, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	archive := filepath.Join(root, "bundle.tar.gz")
	if err := CreateArchive(archive, src); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}

	fh, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	gz, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if strings.Contains(h.Name, `\`) && h.Name != literal {
			t.Errorf("entry %q contains a backslash — tar names must be slash-separated", h.Name)
		}
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{
		"clickhouse_backup_20260101_000000/system.version_20260101_000000.jsonl",
		"clickhouse_backup_20260101_000000/configuration/config.xml",
		"clickhouse_backup_20260101_000000/logs/clickhouse-server.log",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("archive is missing %q; it holds:\n%s", want, joined)
		}
	}
	if literal != "" {
		for _, want := range []string{literal, "clickhouse_backup_20260101_000000/configuration/a/b.xml"} {
			if !strings.Contains(joined+"\n", want+"\n") {
				t.Errorf("a\\b.xml and a/b.xml must be two entries; missing %q in:\n%s", want, joined)
			}
		}
	}
}

// The archive used to be written straight to its final name, with the tar,
// gzip and file Close errors discarded and a directory that failed mid-walk
// skipped. A full disk or an unreadable file then left a truncated or
// half-written clickhouse_backup_*.tar.gz that looked like a bundle, after
// "Archive created successfully". It is now written to a temporary file and
// renamed into place only after every write and close succeeded.

func runDir(t *testing.T) (root, src string) {
	t.Helper()
	root = t.TempDir()
	src = filepath.Join(root, "clickhouse_backup_20260101_000000")
	if err := os.MkdirAll(filepath.Join(src, "configuration"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"system.version_20260101_000000.jsonl", filepath.Join("configuration", "config.xml")} {
		if err := os.WriteFile(filepath.Join(src, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, src
}

// leftovers lists what is in root besides the run directory and want.
func leftovers(t *testing.T, root string, want ...string) []string {
	t.Helper()
	keep := map[string]bool{"clickhouse_backup_20260101_000000": true}
	for _, w := range want {
		keep[w] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var extra []string
	for _, e := range entries {
		if !keep[e.Name()] {
			extra = append(extra, e.Name())
		}
	}
	return extra
}

func unreadable(t *testing.T, src string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file this user cannot read")
	}
	f := filepath.Join(src, "configuration", "config.xml")
	if err := os.Chmod(f, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(f, 0o644) })
}

func TestCreateArchive_SuccessLeavesOnlyTheArchive(t *testing.T) {
	root, src := runDir(t)
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := CreateArchive(archive, src); err != nil {
		t.Fatal(err)
	}
	if extra := leftovers(t, root, "bundle.tar.gz"); len(extra) != 0 {
		t.Errorf("left behind: %v", extra)
	}
	fi, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("archive not created: %v", err)
	}
	// On Windows Chmod only sets the read-only attribute and Stat reports a
	// writable file as 0666, so the exact mode is a POSIX check.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("archive mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestCreateArchive_FailureLeavesNoArchive(t *testing.T) {
	root, src := runDir(t)
	unreadable(t, src)
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := CreateArchive(archive, src); err == nil {
		t.Error("an unreadable file was skipped and the archive reported as created")
	}
	if _, err := os.Stat(archive); err == nil {
		t.Error("a failed run left an archive that looks like a bundle")
	}
	if extra := leftovers(t, root); len(extra) != 0 {
		t.Errorf("temporary files left behind: %v", extra)
	}
}

func TestCreateArchive_FailureKeepsThePreviousArchive(t *testing.T) {
	root, src := runDir(t)
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := os.WriteFile(archive, []byte("previous run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable(t, src)
	if err := CreateArchive(archive, src); err == nil {
		t.Error("want an error")
	}
	if got, _ := os.ReadFile(archive); string(got) != "previous run\n" {
		t.Errorf("the previous archive was replaced by a failed one: %q", got)
	}
}

// failingWriter accepts n bytes, then fails like a full disk.
type failingWriter struct{ n int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		k := w.n
		w.n = 0
		return k, errors.New("no space left on device")
	}
	w.n -= len(p)
	return len(p), nil
}

func TestWriteArchive_ReportsWriteAndCloseErrors(t *testing.T) {
	_, src := runDir(t)
	// Compressed, two small files fit in well under 100 bytes until the
	// gzip footer is flushed on Close — the error that used to be dropped.
	for _, room := range []int{0, 10, 60} {
		if _, err := writeArchive(&failingWriter{n: room}, []string{src}); err == nil {
			t.Errorf("writer failing after %d bytes: no error", room)
		}
	}
	var buf bytes.Buffer
	if n, err := writeArchive(&buf, []string{src}); err != nil || n != 2 {
		t.Errorf("writeArchive = %d, %v; want 2 files, no error", n, err)
	}
}
