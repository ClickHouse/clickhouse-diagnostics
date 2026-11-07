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

// TestLogPathsFromConfig 覆盖的正是此采集器的核心：发现——
// 跟随配置的位置，而不是假设打包默认位置。
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
	// 必须被忽略：未解析的变量替换和相对路径。
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
		t.Fatalf("得到 %v，期望 %d 个绝对路径", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("意外的路径 %q（变量替换和相对路径必须跳过）", p)
		}
	}
}

func TestDiscoverDirs_ExplicitDirWins(t *testing.T) {
	dir := t.TempDir()
	res := &Result{}
	got := DiscoverDirs(Options{Dir: dir, ConfigDir: "/nonexistent"}, res)
	if len(got) != 1 || got[0] != filepath.Clean(dir) {
		t.Errorf("显式目录应当优先，得到 %v", got)
	}

	// 错误的显式目录必须产生备注，而不是静默回退——
	// 否则运维人员会以为自己的 flag 被采纳了。
	res2 := &Result{}
	if got := DiscoverDirs(Options{Dir: "/no/such/dir"}, res2); len(got) != 0 {
		t.Errorf("期望没有目录，得到 %v", got)
	}
	if len(res2.Notes) == 0 {
		t.Error("不存在的 -logs-dir 必须被报告")
	}
}

func TestCollect_CopiesAndTailTruncates(t *testing.T) {
	src := t.TempDir()
	dest := t.TempDir()

	write(t, filepath.Join(src, "clickhouse-server.log"), "line one\nline two\n")
	// 超大文件：4 KiB 带编号的文本行，上限为 1 KiB。
	var big strings.Builder
	for i := 0; big.Len() < 4096; i++ {
		big.WriteString("0123456789 filler line to exceed the cap\n")
	}
	write(t, filepath.Join(src, "big.log"), big.String())
	// 非日志文件和归档默认必须跳过。
	write(t, filepath.Join(src, "notes.txt"), "ignore me")
	write(t, filepath.Join(src, "old.log.gz"), "gzip-ish")

	res, err := Collect(dest, Options{Dir: src, MaxBytesPerFile: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Copied) != 2 {
		t.Fatalf("复制了 %v，期望恰好两个 .log 文件", res.Copied)
	}
	if len(res.Truncated) != 1 || res.Truncated[0] != "big.log" {
		t.Errorf("truncated = %v，期望 [big.log]", res.Truncated)
	}

	small, err := os.ReadFile(filepath.Join(dest, "logs", "clickhouse-server.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(small) != "line one\nline two\n" {
		t.Errorf("小文件应原样复制，得到 %q", small)
	}

	bigOut, err := os.ReadFile(filepath.Join(dest, "logs", "big.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(bigOut), "### support-diagnostic: TRUNCATED") {
		t.Error("被截断的文件必须以截断标记开头，以免有人" +
			"把第一条幸存的行当作日志的开头")
	}
	// 尾部语义：原始文件的末尾必须保留。
	if !strings.HasSuffix(string(bigOut), "filler line to exceed the cap\n") {
		t.Error("截断必须保留尾部，而不是头部")
	}
	// 而且必须从行边界恢复，而不是从消息中间。
	body := strings.SplitN(string(bigOut), "\n", 2)[1]
	if body != "" && !strings.HasPrefix(body, "0123456789") {
		t.Errorf("期望标记之后是干净的行边界，得到 %.40q", body)
	}
}

func TestCollect_MissingDirIsNoteNotError(t *testing.T) {
	// 远程运行是常态；日志目录缺失绝不能导致运行失败。
	res, err := Collect(t.TempDir(), Options{Dir: "/definitely/not/here"})
	if err != nil {
		t.Fatalf("目录缺失不应是错误，得到 %v", err)
	}
	if len(res.Copied) != 0 || len(res.Notes) == 0 {
		t.Errorf("期望复制 0 个并带说明性备注，得到 %+v", res)
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
		t.Errorf("启用 IncludeArchives 后两个文件都应被复制，得到 %v", res.Copied)
	}
}

// TestSymlinksNotFollowed：日志目录通常可由 clickhouse 服务账号写入，
// 而诊断工具常以 root 运行。一个植入的 `evil.log -> /etc/shadow`
// 绝不能进入与支持人员共享的 bundle。
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
		t.Skipf("无法创建符号链接：%v", err)
	}

	res, err := Collect(outDir, Options{Dir: logDir, MaxBytesPerFile: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range res.Copied {
		if strings.Contains(name, "evil") {
			t.Fatalf("符号链接被跟随并复制了：%v", res.Copied)
		}
	}
	if len(res.Copied) != 1 || !strings.Contains(res.Copied[0], "real") {
		t.Errorf("期望恰好只有真实日志，得到 %v", res.Copied)
	}
	found := false
	for _, n := range res.Notes {
		if strings.Contains(n, "evil.log") {
			found = true
		}
	}
	if !found {
		t.Error("被跳过的符号链接未在 Notes 中报告；静默遗漏" +
			"会被读作日志缺失，而不是拒绝")
	}
}

// TestTruncationHeaderIsExact 固定住头部在 copilot-review 修复后做出的
// 两项声明：“kept last N bytes”数字必须等于其后实际的有效载荷
// （行对齐最多会从名义上限削掉一行），以及 Result.TotalBytes
// 必须统计写入 bundle 的内容——包括头部——而不是从源
// 读取的内容。
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
		t.Fatalf("%q 中没有截断头部", string(out)[:80])
	}
	m := regexp.MustCompile(`original (\d+) bytes, kept last (\d+) bytes`).FindStringSubmatch(head)
	if m == nil {
		t.Fatalf("头部形状变了：%q", head)
	}
	original, _ := strconv.Atoi(m[1])
	kept, _ := strconv.Atoi(m[2])

	if original != big.Len() {
		t.Errorf("头部声称原始 %d 字节，源文件是 %d", original, big.Len())
	}
	if kept != len(payload) {
		t.Errorf("头部声称保留 %d 字节，有效载荷实际是 %d", kept, len(payload))
	}
	if kept > 1024 {
		t.Errorf("保留的 %d 字节超过了 1024 字节上限", kept)
	}
	if !strings.HasPrefix(payload, "0123456789") {
		t.Errorf("有效载荷未从行边界开始：%q", payload[:40])
	}
	if res.TotalBytes != int64(len(out)) {
		t.Errorf("TotalBytes = %d，bundle 文件是 %d 字节（头部必须计入）",
			res.TotalBytes, len(out))
	}
}

// TestConfigParentScanOnlyForDotD 固定住评审发现 #8：使用
// -config-dir /etc/clickhouse-server 时父目录是 /etc，任何无关的
// /etc/*.xml 只要带有 <log> 元素，就会提供一个目录，其中的
// 文件随后会进入 bundle。只有当给定目录是 *.d overlay
// （它确实位于 config.xml 旁边）时才扫描父目录。
func TestConfigParentScanOnlyForDotD(t *testing.T) {
	parent := t.TempDir()
	confDir := filepath.Join(parent, "clickhouse-server")
	write(t, filepath.Join(confDir, "config.xml"), "<clickhouse><logger><log>/data/ch/a.log</log></logger></clickhouse>")
	// PARENT 中一个无关文件（可以想象 /etc/foo.xml），带有日志路径。
	write(t, filepath.Join(parent, "unrelated.xml"), "<clickhouse><logger><log>/tmp/stolen.log</log></logger></clickhouse>")

	got := LogPathsFromConfig(confDir)
	for _, p := range got {
		if p == "/tmp/stolen.log" {
			t.Fatalf("扫描了非 .d 配置目录的父目录：%v", got)
		}
	}
	if len(got) != 1 || got[0] != "/data/ch/a.log" {
		t.Errorf("期望只有配置目录自身的路径，得到 %v", got)
	}

	// 但对于 config.d 目录，仍必须扫描父目录（其中存放
	// config.xml）——主日志路径通常就在那里。
	confD := filepath.Join(confDir, "config.d")
	write(t, filepath.Join(confD, "override.xml"), "<clickhouse><logger><log>/data/ch/b.log</log></logger></clickhouse>")
	got = LogPathsFromConfig(confD)
	want := map[string]bool{"/data/ch/a.log": true, "/data/ch/b.log": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("config.d 也必须扫描其父目录，得到 %v", got)
	}
}
