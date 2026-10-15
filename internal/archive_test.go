package internal

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
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
