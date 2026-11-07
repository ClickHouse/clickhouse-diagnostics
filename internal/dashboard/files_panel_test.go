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
	mk("dashboard.html", 999)    // 不能列出自身
	mk("schema_graph.html", 555) // 它自己的图页面，从Schema标签页打开
	mk(".DS_Store", 6148)        // 操作系统垃圾文件，不是产出物

	got := collectBundleFiles(dir)
	names := make([]string, len(got))
	for i, f := range got {
		names[i] = f["file"].(string)
	}
	want := []string{
		"host_info.json", "system.parts_2026.jsonl", // bundle根目录优先
		"configuration/20-keeper.xml",
		"logs/clickhouse-server.log",
	}
	if len(names) != len(want) {
		t.Fatalf("得到%v，期望%v", names, want)
	}
	// 先按文件夹分组，再按名称排序，使列表读起来像归档包。
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("位置%d：得到%q，期望%q（完整：%v）", i, names[i], want[i], names)
		}
	}

	byName := map[string]map[string]interface{}{}
	for _, f := range got {
		byName[f["file"].(string)] = f
	}
	log := byName["logs/clickhouse-server.log"]
	if log["href"] != "logs/clickhouse-server.log" {
		t.Errorf("href必须相对于bundle并以斜杠分隔，得到%v", log["href"])
	}
	if log["size"] != "3.0 MiB" {
		t.Errorf("size = %v，期望3.0 MiB", log["size"])
	}
	if log["group"] != "logs" || log["kind"] != "log" {
		t.Errorf("group/kind = %v/%v，期望logs/log", log["group"], log["kind"])
	}
	if byName["host_info.json"]["group"] != "" {
		t.Errorf("根目录文件的group应为空，得到%v", byName["host_info.json"]["group"])
	}
}

func TestCollectBundleFiles_MissingDir(t *testing.T) {
	if got := collectBundleFiles(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Errorf("目录缺失时不应产生行，得到%v", got)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, c := range []struct {
		in   int64
		want string
	}{{512, "512 B"}, {2048, "2.0 KiB"}, {3 << 20, "3.0 MiB"}, {5 << 30, "5.0 GiB"}} {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q，期望%q", c.in, got, c.want)
		}
	}
}

// bundle只做索引，绝不内联。原始服务器日志每个文件最多尾部复制50 MiB；
// 嵌入它们会让页面无法打开。
func TestTemplate_FilesPanelLinksAndDoesNotEmbed(t *testing.T) {
	for _, want := range []string{
		`id="sec-files"`, `id="nav-files"`, "const files=DATA.bundle_files||[];",
		"if(!files.length) return;", "window.filesFilter", `target="_blank"`, "f.href.split('/').map(encodeURIComponent).join('/')",
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("文件面板缺少%q", want)
		}
	}
}

// 单元格值由客户控制——包括文件名。转义是默认行为；
// 只有本文件构建的标记可以选择跳过。
func TestTemplate_TableCellsAreEscaped(t *testing.T) {
	for _, want := range []string{
		"function esc(v){",
		"function renderTable(id, rows, cols, rowClass, htmlCols){",
		"const raw=new Set(htmlCols||[]);",
		`'<td title="'+esc(r[k])+'">'+esc(r[k])+'</td>'`,
	} {
		if !strings.Contains(htmlTemplate, want) {
			t.Errorf("renderTable缺少转义：%q", want)
		}
	}
	// 两个例外：文件链接和日志级别徽章——都是本文件构建的标记。
	// 第三个必须是刻意添加的。
	if got := strings.Count(htmlTemplate, ",null,['"); got != 2 {
		t.Errorf("期望恰好2个htmlCols例外（文件链接、级别徽章），实际找到%d", got)
	}
}
