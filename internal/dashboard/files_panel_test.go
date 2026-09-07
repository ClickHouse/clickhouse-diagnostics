package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectBundleFiles(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string, size int) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("system.parts_2026.jsonl", 2048)
	mk("host_info.json", 512)
	mk("logs/clickhouse-server.log", 3145728)
	mk("configuration/20-keeper.xml", 100)
	mk("dashboard.html", 999) // must not list itself
	mk(".DS_Store", 6148)     // OS turd, not an artifact

	got := collectBundleFiles(dir)
	names := make([]string, len(got))
	for i, f := range got {
		names[i] = f["file"].(string)
	}
	want := []string{
		"host_info.json", "system.parts_2026.jsonl", // bundle root first
		"configuration/20-keeper.xml",
		"logs/clickhouse-server.log",
	}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	// Grouped by folder, then by name, so the list reads like the archive.
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("position %d: got %q, want %q (full: %v)", i, names[i], want[i], names)
		}
	}

	byName := map[string]map[string]interface{}{}
	for _, f := range got {
		byName[f["file"].(string)] = f
	}
	log := byName["logs/clickhouse-server.log"]
	if log["href"] != "logs/clickhouse-server.log" {
		t.Errorf("href must be bundle-relative and slash-separated, got %v", log["href"])
	}
	if log["size"] != "3.0 MiB" {
		t.Errorf("size = %v, want 3.0 MiB", log["size"])
	}
	if log["group"] != "logs" || log["kind"] != "log" {
		t.Errorf("group/kind = %v/%v, want logs/log", log["group"], log["kind"])
	}
	if byName["host_info.json"]["group"] != "" {
		t.Errorf("a root file should have an empty group, got %v", byName["host_info.json"]["group"])
	}
}

func TestCollectBundleFiles_MissingDir(t *testing.T) {
	if got := collectBundleFiles(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Errorf("missing dir should yield no rows, got %v", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{{512, "512 B"}, {2048, "2.0 KiB"}, {3 << 20, "3.0 MiB"}, {5 << 30, "5.0 GiB"}} {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The bundle is indexed, never inlined. Raw server logs are tail-copied at up
// to 50 MiB per file; embedding them would make the page unopenable.
func TestTemplate_FilesPanelLinksAndDoesNotEmbed(t *testing.T) {
	for _, want := range []string{
		`id="sec-files"`, `id="nav-files"`, "const files=DATA.bundle_files||[];",
		"if(!files.length) return;", "window.filesFilter", `target="_blank"`, "f.href.split('/').map(encodeURIComponent).join('/')",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("files panel missing %q", want)
		}
	}
}

// Cell values are customer-controlled — filenames included. Escaping is the
// default; only markup this file builds opts out.
func TestTemplate_TableCellsAreEscaped(t *testing.T) {
	for _, want := range []string{
		"function esc(v){",
		"function renderTable(id, rows, cols, rowClass, htmlCols){",
		"const raw=new Set(htmlCols||[]);",
		`'<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>'`,
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("renderTable escaping missing: %q", want)
		}
	}
	// Two opt-outs: the file link and the log-level badge — both markup this
	// file builds. A third has to be deliberate.
	if got := strings.Count(htmlTemplate, ",null,['"); got != 2 {
		t.Errorf("expected exactly 2 htmlCols opt-outs (file link, level badge), found %d", got)
	}
}
