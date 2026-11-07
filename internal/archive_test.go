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

// tar 条目名称按定义就是以斜杠分隔的。采集器用 filepath.Join 构建它，
// 因此 Windows 版本写出的是
// `clickhouse_backup_…\system.parts_….jsonl`——而不是归档内部的路径。
// 其他平台随后会把它解压成一个名称包含反斜杠的单一文件，而不是一个
// 目录，于是 dashboard、分析 skill 和普通 `tar` 看到的都是一个空 bundle。
// 一个这样采集的客户 bundle 让支持案例多花了两个来回，因此这里用它
// 必须扛住的 Windows 形态输入把这个转换固定下来。
//
// 只转换采集平台自身的分隔符。在 Unix 上反斜杠是普通的文件名字符，
// 而 configuration/ 和 logs/ 以真实名称复制服务器自己的文件：
// `a\b.xml` 和 `a/b.xml` 是两个文件，必须保持为两个条目。
func TestEntryName_ConvertsOnlyTheHostSeparator(t *testing.T) {
	for _, c := range []struct {
		sep             rune
		base, rel, want string
	}{
		// filepath.Rel 在 Windows 上返回的内容：
		{'\\', "clickhouse_backup_20261006_084257", `system.parts_20261006_084257.jsonl`, "clickhouse_backup_20261006_084257/system.parts_20261006_084257.jsonl"},
		{'\\', "clickhouse_backup_20261006_084257", `configuration\config.xml`, "clickhouse_backup_20261006_084257/configuration/config.xml"},
		{'\\', "clickhouse_backup_20261006_084257", `logs\clickhouse-server.err.log`, "clickhouse_backup_20261006_084257/logs/clickhouse-server.err.log"},
		{'\\', "bundle", `a\b\c\deep.jsonl`, "bundle/a/b/c/deep.jsonl"},
		// 在其他任何地方返回的内容——保持不变，保留字面反斜杠：
		{'/', "bundle", "configuration/config.xml", "bundle/configuration/config.xml"},
		{'/', "bundle", `configuration/a\b.xml`, `bundle/configuration/a\b.xml`},
		// 目录条目本身：
		{'\\', "bundle", ".", "bundle"},
		{'/', "bundle", "", "bundle"},
	} {
		if got := entryName(c.base, c.rel, c.sep); got != c.want {
			t.Errorf("entryName(%q, %q, %q) = %q，期望 %q", c.base, c.rel, c.sep, got, c.want)
		}
	}
	// 生产代码使用本平台的分隔符。
	if got, want := archiveEntryName("bundle", "x.jsonl"), entryName("bundle", "x.jsonl", filepath.Separator); got != want {
		t.Errorf("archiveEntryName = %q，期望 %q", got, want)
	}
}

// 端到端：宿主分隔符绝不会进入归档，带字面反斜杠的 Unix 文件名会保留它，
// 并且结构在往返后依然完好。
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
		// Unix 上的两个不同文件：configuration/a\b.xml 和 configuration/a/b.xml。
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
		t.Fatalf("CreateArchive：%v", err)
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
			t.Errorf("条目 %q 包含反斜杠——tar 名称必须以斜杠分隔", h.Name)
		}
	}
	joined := strings.Join(names, "\n")
	for _, want := range []string{
		"clickhouse_backup_20260101_000000/system.version_20260101_000000.jsonl",
		"clickhouse_backup_20260101_000000/configuration/config.xml",
		"clickhouse_backup_20260101_000000/logs/clickhouse-server.log",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("归档缺少 %q；其内容为：\n%s", want, joined)
		}
	}
	if literal != "" {
		for _, want := range []string{literal, "clickhouse_backup_20260101_000000/configuration/a/b.xml"} {
			if !strings.Contains(joined+"\n", want+"\n") {
				t.Errorf("a\\b.xml 和 a/b.xml 必须是两个条目；缺少 %q，内容为：\n%s", want, joined)
			}
		}
	}
}

// 归档过去会直接写到最终文件名，丢弃 tar、gzip 和文件 Close 的错误，
// 并跳过遍历中途失败的目录。磁盘写满或文件不可读时，就会留下一个
// 截断或写了一半的 clickhouse_backup_*.tar.gz，看起来仍像 bundle，
// 而且还是在 "Archive created successfully" 之后。现在它先写入临时文件，
// 只有所有写入和关闭都成功后才重命名为正式文件。

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

// leftovers 列出 root 中除运行目录和 want 之外的内容。
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
		t.Skip("需要一个此用户无法读取的文件")
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
		t.Errorf("残留：%v", extra)
	}
	fi, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("归档未创建：%v", err)
	}
	// 在 Windows 上 Chmod 只设置只读属性，Stat 会把可写文件报告为 0666，
	// 因此精确权限是一项 POSIX 检查。
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("归档权限 %v，期望 0600", fi.Mode().Perm())
	}
}

func TestCreateArchive_FailureLeavesNoArchive(t *testing.T) {
	root, src := runDir(t)
	unreadable(t, src)
	archive := filepath.Join(root, "bundle.tar.gz")
	if err := CreateArchive(archive, src); err == nil {
		t.Error("不可读文件被跳过，归档还被报告为已创建")
	}
	if _, err := os.Stat(archive); err == nil {
		t.Error("失败的运行留下了看起来像 bundle 的归档")
	}
	if extra := leftovers(t, root); len(extra) != 0 {
		t.Errorf("残留临时文件：%v", extra)
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
		t.Error("期望一个错误")
	}
	if got, _ := os.ReadFile(archive); string(got) != "previous run\n" {
		t.Errorf("之前的归档被失败的归档替换了：%q", got)
	}
}

// failingWriter 接受 n 个字节，然后像磁盘写满一样失败。
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
	// 压缩后，两个小文件在 Close 刷新 gzip 尾部之前远小于 100 字节——
	// 而那就是过去被丢弃的错误。
	for _, room := range []int{0, 10, 60} {
		if _, err := writeArchive(&failingWriter{n: room}, []string{src}); err == nil {
			t.Errorf("写入器在 %d 字节后失败：没有错误", room)
		}
	}
	var buf bytes.Buffer
	if n, err := writeArchive(&buf, []string{src}); err != nil || n != 2 {
		t.Errorf("writeArchive = %d, %v；期望 2 个文件、无错误", n, err)
	}
}
