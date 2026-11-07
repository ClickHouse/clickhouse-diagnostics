package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realRepoSQLFiles 遍历随包发布的查询目录，沿用 gov_leak_test.go
// 使用的 ../../ 约定。
func realRepoSQLFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{
		"../../queries.onprem", "../../queries.cloud",
		"../../queries.gov", "../../queries.query_analysis",
	} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.EqualFold(filepath.Ext(p), ".sql") {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s：%v", dir, err)
		}
	}
	return out
}

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s：%v", path, err)
	}
	return string(b)
}

func TestParseOutputFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    OutputFormat
		wantErr bool
	}{
		{"", DefaultOutputFormat, false},
		{"jsonl", FormatJSONL, false},
		{"JSONL", FormatJSONL, false},
		{"  native  ", FormatNative, false},
		{"tsv", FormatTSV, false},
		{"json", OutputFormat{}, true},        // 近似值不得悄悄解析成功
		{"JSONEachRow", OutputFormat{}, true}, // 该子句不是 flag 取值
		{"parquet", OutputFormat{}, true},
	}
	for _, c := range cases {
		got, err := ParseOutputFormat(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseOutputFormat(%q) err = %v，wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("ParseOutputFormat(%q) = %+v，期望 %+v", c.in, got, c.want)
		}
	}
}

// TestDefaultIsReadable 守护这一层存在的理由：默认格式必须是运维人员
// （或工具）无需先把 bundle 加载进 ClickHouse 就能阅读的文本格式。
func TestDefaultIsReadable(t *testing.T) {
	if DefaultOutputFormat.Clause == "Native" {
		t.Fatal("默认输出格式是 Native（二进制）；该 flag 的意义在于 " +
			"bundle 无需 ClickHouse 加载即可阅读")
	}
	if !DefaultOutputFormat.IsJSON() {
		t.Errorf("default = %q；期望 JSON 格式，以便每一行都带有"+
			"列名", DefaultOutputFormat.Clause)
	}
}

func TestApplyReplacesExistingFormat(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"no format", "SELECT 1 FROM system.parts"},
		{"trailing native", "SELECT 1 FROM system.parts\nFORMAT Native"},
		{"lowercase", "SELECT 1 FROM system.parts\nformat native"},
		{"trailing whitespace", "SELECT 1 FROM system.parts\nFORMAT Native\n\n  "},
		{"other format", "SELECT 1 FROM system.parts FORMAT JSONCompact"},
	}
	for _, c := range cases {
		got := FormatJSONL.Apply(c.sql)
		if n := strings.Count(strings.ToUpper(got), "FORMAT "); n != 1 {
			t.Errorf("%s: 得到 %d 个 FORMAT 子句，期望恰好 1 个：\n%s", c.name, n, got)
		}
		if !strings.HasSuffix(got, "\nFORMAT JSONEachRow") {
			t.Errorf("%s: 结尾不是所应用的格式：\n%q", c.name, got)
		}
		// 无论我们产出什么，都必须仍能通过安全检查器，
		// 它在执行前正是应用在这段文本上。
		if err := ValidateQueryContent(got); err != nil {
			t.Errorf("%s: 应用后的 SQL 未通过校验：%v", c.name, err)
		}
	}
}

// TestApplyPreservesFormatInsideQuery 确保剥离操作锚定在结尾。
// `message_format_string` 之类的列包含 "format"，如果在第一个匹配处截断查询，
// 会悄悄丢掉列。
func TestApplyPreservesFormatInsideQuery(t *testing.T) {
	sql := "SELECT message_format_string, formatReadableSize(bytes) FROM system.text_log\nFORMAT Native"
	got := FormatJSONL.Apply(sql)
	for _, want := range []string{"message_format_string", "formatReadableSize(bytes)"} {
		if !strings.Contains(got, want) {
			t.Errorf("Apply 从查询体中丢掉了 %q：\n%s", want, got)
		}
	}
}

// TestNoQueryFileCarriesFormat 固定住“输出格式由 Go 掌控”这一不变量。
// 查询文件中多余的 FORMAT 会被 Apply 悄悄替换，因此这关乎让文件保持诚实，
// 而不是正确性。
func TestNoQueryFileCarriesFormat(t *testing.T) {
	files := realRepoSQLFiles(t)
	if len(files) == 0 {
		t.Skip("未找到查询文件（在仓库外运行）")
	}
	for _, f := range files {
		body := readFileForTest(t, f)
		if reFormatClause.MatchString(strings.TrimRight(body, " \t\r\n")) {
			t.Errorf("%s 以 FORMAT 子句结尾；输出格式由 Go 通过 --output-format "+
				"选择，因此查询文件不得固定格式", f)
		}
	}
}
