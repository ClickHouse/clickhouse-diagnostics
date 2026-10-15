package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-pass repairs bundles packed by Windows builds that wrote backslash
// separators into tar entry names. The repair must touch only those legacy
// entries — a correct Unix archive may hold a file whose name contains a
// literal backslash next to a real subdirectory of the same spelling — and an
// already-extracted Windows bundle cannot be repaired without modifying the
// evidence, so it is refused with a pointer to the original archive.

const sepRun = "clickhouse_backup_20260101_000000"

func prepassScript(t *testing.T) (py, script string) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script = filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("pre-pass script not present: %v", err)
	}
	return py, script
}

var minimalBundle = map[string]string{
	"system.version_20260101_000000.jsonl":  `{"version":"26.7.1.1"}` + "\n",
	"system.settings_20260101_000000.jsonl": `{"name":"max_threads","value":"8","changed":1}` + "\n",
}

// writeTarGz writes the given member name → content pairs, names verbatim.
func writeTarGz(t *testing.T, path string, members [][2]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		if err := tw.WriteHeader(&tar.Header{Name: m[0], Mode: 0o644, Size: int64(len(m[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runPrepass(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	py, script := prepassScript(t)
	cmd := exec.Command(py, append([]string{"-I", "-B", script}, args...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

func notesOf(t *testing.T, out string) []string {
	t.Helper()
	var r struct {
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("pre-pass did not emit JSON: %v\n%s", err, out)
	}
	return r.Notes
}

func mentionsWindows(notes []string) bool {
	for _, n := range notes {
		if strings.Contains(n, "Windows build") {
			return true
		}
	}
	return false
}

func TestPrepass_RepairsLegacyWindowsArchive(t *testing.T) {
	dir := t.TempDir()
	var members [][2]string
	for name, body := range minimalBundle {
		members = append(members, [2]string{sepRun + `\` + name, body})
	}
	members = append(members, [2]string{sepRun + `\configuration\config.xml`, "<clickhouse/>\n"})
	archive := filepath.Join(dir, sepRun+".tar.gz")
	writeTarGz(t, archive, members)

	dest := filepath.Join(dir, "x")
	out, errOut, code := runPrepass(t, "--json", "--extract-to", dest, archive)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !mentionsWindows(notesOf(t, out)) {
		t.Error("repaired a Windows archive without saying so")
	}
	if _, err := os.Stat(filepath.Join(dest, sepRun, "configuration", "config.xml")); err != nil {
		t.Errorf("configuration/config.xml not restored as a directory tree: %v", err)
	}
}

func TestPrepass_KeepsLiteralBackslashInUnixArchive(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("a backslash cannot be a filename character here")
	}
	dir := t.TempDir()
	var members [][2]string
	for name, body := range minimalBundle {
		members = append(members, [2]string{sepRun + "/" + name, body})
	}
	members = append(members,
		[2]string{sepRun + `/configuration/a\b.xml`, "literal\n"},
		[2]string{sepRun + "/configuration/a/b.xml", "nested\n"})
	archive := filepath.Join(dir, sepRun+".tar.gz")
	writeTarGz(t, archive, members)

	dest := filepath.Join(dir, "x")
	out, errOut, code := runPrepass(t, "--json", "--extract-to", dest, archive)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if mentionsWindows(notesOf(t, out)) {
		t.Error("a correct Unix archive was reported as a Windows one")
	}
	for name, want := range map[string]string{`a\b.xml`: "literal\n", filepath.Join("a", "b.xml"): "nested\n"} {
		got, err := os.ReadFile(filepath.Join(dest, sepRun, "configuration", name))
		if err != nil || string(got) != want {
			t.Errorf("configuration/%s = %q, %v; want %q — the two files were merged", name, got, err, want)
		}
	}
}

func TestPrepass_RefusesExtractedWindowsFolderWithoutTouchingIt(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("on Windows these names are real paths")
	}
	dir := t.TempDir()
	// Plain tar also creates the archive's top-level directory entry, empty:
	// the input that used to be picked as the run directory and accepted.
	if err := os.Mkdir(filepath.Join(dir, sepRun), 0o755); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name, body := range minimalBundle {
		flat := sepRun + `\` + name // what plain tar leaves on Unix
		names = append(names, flat)
		if err := os.WriteFile(filepath.Join(dir, flat), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, errOut, code := runPrepass(t, "--json", dir)
	if code != 2 {
		t.Errorf("exit %d, want 2 (refused)", code)
	}
	if !strings.Contains(errOut, ".tar.gz") {
		t.Errorf("the refusal must point to the original archive: %s", errOut)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("evidence modified: %s: %v", n, err)
		}
	}
}
