package query

import (
	"fmt"
	"strings"
)

// OutputFormat 是把查询结果序列化进支持 bundle 时使用的 ClickHouse 格式。
//
// 历史上的格式是 Native：紧凑、类型精确，且无需 schema 即可重新加载。
// 但它同时也是面向列的二进制格式，因此读取 bundle 意味着先把它加载进
// ClickHouse —— 无法 grep、无法 jq，任何基于 LLM 的工具都无法直接读取。
// 由于归档本身已是 tar.gz（internal/archive.go），文本格式压缩后的体积
// 与 Native 相差约 10% 以内，因此可读性几乎是免费的。
type OutputFormat struct {
	// Name 是命令行上接受的值。
	Name string
	// Clause 是追加到每个查询后面的 ClickHouse FORMAT。
	Clause string
	// Ext 是输出文件扩展名，包含前导点号。
	Ext string
}

var (
	// FormatJSONL 每行一个自描述的 JSON 对象。首选它是因为每一行都带有
	// 列名：system.query_log 约有 60 列，而按位置排列的格式如果不把字段
	// 一路数回表头，根本读不懂一行。
	FormatJSONL = OutputFormat{Name: "jsonl", Clause: "JSONEachRow", Ext: ".jsonl"}

	// FormatNative 是原始的二进制格式。保留它是为了那些会被批量重新加载
	// 进 ClickHouse 的 bundle，在这些场景中，无需目标 schema 的精确类型保真
	// 比可读性更重要。
	FormatNative = OutputFormat{Name: "native", Clause: "Native", Ext: ".native"}

	// FormatTSV 在前两行同时带上名称和类型，因此它是最忠实于重载的文本格式，
	// 也是体积最小的。它按位置排列，所以宽表读起来很吃力。
	FormatTSV = OutputFormat{Name: "tsv", Clause: "TSVWithNamesAndTypes", Ext: ".tsv"}
)

var outputFormats = []OutputFormat{FormatJSONL, FormatNative, FormatTSV}

// DefaultOutputFormat 是未指定 --output-format 时运行的格式。
var DefaultOutputFormat = FormatJSONL

// ParseOutputFormat 把命令行取值解析为一种格式。
func ParseOutputFormat(s string) (OutputFormat, error) {
	want := strings.ToLower(strings.TrimSpace(s))
	if want == "" {
		return DefaultOutputFormat, nil
	}
	for _, f := range outputFormats {
		if f.Name == want {
			return f, nil
		}
	}
	return OutputFormat{}, fmt.Errorf("--output-format 必须是 %s 之一（实际为 %q）",
		OutputFormatNames(), s)
}

// OutputFormatNames 列出可接受的值，用于 flag 帮助和错误信息。
func OutputFormatNames() string {
	names := make([]string, 0, len(outputFormats))
	for _, f := range outputFormats {
		names = append(names, f.Name)
	}
	return strings.Join(names, "|")
}

// IsJSON 报告该格式是否输出 JSON，因而需要 64 位整数的引号保证
// （见 pkg.ClickHouseClient）。
func (f OutputFormat) IsJSON() bool {
	return strings.HasPrefix(f.Clause, "JSON")
}

// Apply 返回把末尾任何 FORMAT 子句替换为该格式之后
// 的 sql。查询文件本身不带 FORMAT，但覆盖文件、模板和手工编辑的文件可能带有，
// 而再追加一个 FORMAT 会造成语法错误。
func (f OutputFormat) Apply(sql string) string {
	return strings.TrimRight(StripTrailingFormat(sql), " \t\r\n") + "\nFORMAT " + f.Clause
}

// StripTrailingFormat 去掉末尾的 `FORMAT <name>` 子句。
func StripTrailingFormat(sql string) string {
	return reFormatClause.ReplaceAllString(strings.TrimRight(sql, " \t\r\n"), "")
}
