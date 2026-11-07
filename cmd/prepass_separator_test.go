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

// pre-pass 会修复由 Windows 构建打包的 bundle——那些构建把反斜杠分隔符
// 写进了 tar 条目名。修复只能触碰这些遗留条目——正确的 Unix 归档中，
// 文件名可能包含字面反斜杠，旁边还可能有拼写相同的真实子目录——而已解压的
// Windows bundle 无法在不修改证据的情况下修复，因此会拒绝处理并指向
// 原始归档。

const sepRun = "clickhouse_backup_20260101_000000"

func prepassScript(t *testing.T) (py, script string) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script = filepath.Join("..", "skills", "clickhouse-diagnostic", "scripts", "inspect_bundle.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("pre-pass 脚本不存在：%v", err)
	}
	return py, script
}

var minimalBundle = map[string]string{
	"system.version_20260101_000000.jsonl":  `{"version":"26.7.1.1"}` + "\n",
	"system.settings_20260101_000000.jsonl": `{"name":"max_threads","value":"8","changed":1}` + "\n",
}

// writeTarGz 按原样写入给定的成员名 → 内容对，名称不做改动。
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
		t.Fatalf("pre-pass 未输出 JSON：%v\n%s", err, out)
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
		t.Fatalf("退出码 %d：%s", code, errOut)
	}
	if !mentionsWindows(notesOf(t, out)) {
		t.Error("修复了 Windows 归档却没有说明")
	}
	if _, err := os.Stat(filepath.Join(dest, sepRun, "configuration", "config.xml")); err != nil {
		t.Errorf("configuration/config.xml 未恢复为目录树：%v", err)
	}
}

func TestPrepass_KeepsLiteralBackslashInUnixArchive(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("此处反斜杠不能作为文件名字符")
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
		t.Fatalf("退出码 %d：%s", code, errOut)
	}
	if mentionsWindows(notesOf(t, out)) {
		t.Error("正确的 Unix 归档被报告为 Windows 归档")
	}
	for name, want := range map[string]string{`a\b.xml`: "literal\n", filepath.Join("a", "b.xml"): "nested\n"} {
		got, err := os.ReadFile(filepath.Join(dest, sepRun, "configuration", name))
		if err != nil || string(got) != want {
			t.Errorf("configuration/%s = %q, %v；期望 %q——两个文件被合并了", name, got, err, want)
		}
	}
}

func TestPrepass_RefusesExtractedWindowsFolderWithoutTouchingIt(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("在 Windows 上这些名称是真实路径")
	}
	dir := t.TempDir()
	// 普通 tar 也会创建归档的顶层目录条目，且为空：
	// 过去它会被当作运行目录选中并接受。
	if err := os.Mkdir(filepath.Join(dir, sepRun), 0o755); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name, body := range minimalBundle {
		flat := sepRun + `\` + name // 普通 tar 在 Unix 上留下的样子
		names = append(names, flat)
		if err := os.WriteFile(filepath.Join(dir, flat), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, errOut, code := runPrepass(t, "--json", dir)
	if code != 2 {
		t.Errorf("退出码 %d，期望 2（拒绝）", code)
	}
	if !strings.Contains(errOut, ".tar.gz") {
		t.Errorf("拒绝信息必须指向原始归档：%s", errOut)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("证据被修改：%s：%v", n, err)
		}
	}
}
