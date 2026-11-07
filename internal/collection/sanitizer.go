package collection

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sanitizer 在 ClickHouse XML / YAML 配置文件被打包进支持归档之前
// 从中移除凭据。它结合了：
//
//  1. 结构化脱敏——正确的 XML / YAML 解析，遍历语法树，
//     替换标签 / 键名匹配敏感列表的值。这能处理多行值、
//     嵌套元素，以及任何名称包含已识别片段的未来
//     凭据标签（例如 `<my_custom_password>`）。
//
//  2. 启发式脱敏——对凭据形态的字节序列做模式匹配
//     （AWS access key、JWT、PEM 块、长十六进制 /
//     base64）。应用于结构化处理无法触及的注释和自由文本，
//     因此粘贴到 XML 注释或非敏感标签中的凭据仍会被擦除。
//
// 解析出错时脱敏器失败即关闭——它把错误返回给调用方，
// 调用方随后必须决定不提交该文件。这正是此前仅靠正则的实现
// 所缺少的安全属性。
type Sanitizer struct{}

// NewSanitizer 返回一个 Sanitizer。该结构体是无状态的；
// 构造函数的存在是为了与代码库其余部分保持对称。
func NewSanitizer() *Sanitizer { return &Sanitizer{} }

// SanitizeXMLContent 使用结构化脱敏 + 启发式规则从 ClickHouse XML 配置中
// 移除凭据。返回（脱敏后的内容、执行的脱敏次数、错误）。解析出错时
// 原内容会原样返回，以便调用方记录；调用方
// 仍必须将该错误视为硬性跳过（不要提交文件）。
func (s *Sanitizer) SanitizeXMLContent(content []byte) ([]byte, int, error) {
	out, n, err := redactXML(content)
	if err != nil {
		return content, 0, err
	}
	// 纵深防御：扫描脱敏后的输出，查找结构化处理无法触及的
	// 凭据形态值（注释、非敏感标签下的自由文本、
	// 我们未识别的标签的属性值）。
	final, extra := RedactCredentialsInText(string(out))
	return []byte(final), n + extra, nil
}

// SanitizeYAMLContent 使用 yaml.Node 树遍历 + 启发式规则从 YAML 配置中
// 移除凭据。失败即关闭的契约与 SanitizeXMLContent 相同。
func (s *Sanitizer) SanitizeYAMLContent(content []byte) ([]byte, int, error) {
	out, n, err := redactYAML(content)
	if err != nil {
		return content, 0, err
	}
	final, extra := RedactCredentialsInText(string(out))
	return []byte(final), n + extra, nil
}

// IsXMLFile 报告 path/content 是否应被当作 XML 处理。
// 先看文件扩展名，再看 `<?xml` 前导，最后做
// 尽力而为的解析探测（可捕获没有扩展名的 include 文件）。
func (s *Sanitizer) IsXMLFile(path string, content []byte) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xml", ".xsd", ".svg", ".config":
		return true
	}
	if bytes.HasPrefix(bytes.TrimSpace(content), []byte("<?xml")) {
		return true
	}
	// 廉价的解析探测——接受任何带可识别根标签的内容。
	dec := xml.NewDecoder(bytes.NewReader(content))
	dec.Strict = false
	for i := 0; i < 8; i++ {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if _, ok := tok.(xml.StartElement); ok {
			return true
		}
	}
	return false
}

// IsYAMLFile 按扩展名报告 path 是否为 YAML 文件。
func (s *Sanitizer) IsYAMLFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

// ShouldSanitize 决定文件是否值得脱敏。
// keepPasswords 会直接短路为 false，使调用方可以为
// 自审流程选择跳过。
func (s *Sanitizer) ShouldSanitize(path string, content []byte, keepPasswords bool) bool {
	if keepPasswords {
		return false
	}
	return s.IsXMLFile(path, content) || s.IsYAMLFile(path)
}

// ── XML ──────────────────────────────────────────────────────────────

// redactXML 流式解析 content，找出文本内容属于敏感标签的
// 字节范围并重写这些范围。属性名敏感的属性值
// 会就地脱敏。返回重写后的字节、脱敏次数和任何解析错误。
func redactXML(content []byte) ([]byte, int, error) {
	dec := xml.NewDecoder(bytes.NewReader(content))
	dec.Strict = false

	type span struct {
		start, end int
	}
	var (
		redactSpans []span
		stack       []string
		prev        int64
		count       int
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("XML 解析失败：%w", err)
		}
		cur := dec.InputOffset()

		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)

			// 属性脱敏：重写原始起始标签字节
			// [prev, cur)，把敏感属性的值替换为 REMOVED。这样
			// 无需依赖标签顺序即可处理 password="..."
			// 和 password='...'。
			if hasSensitiveAttr(t.Attr) {
				rewritten := redactAttrs(content[prev:cur])
				if !bytes.Equal(rewritten, content[prev:cur]) {
					// 将重写结果拼入 content。我们内联
					// 执行，而不是通过 span 列表，因为
					// 这重写是等长或更短的替换，且对
					// 后续 token 保持偏移稳定——
					// `xml.Decoder` 继续从原始字节读取，
					// 因此我们只在这里修补 OUTPUT。
					redactSpans = append(redactSpans, span{int(prev), int(cur)})
					attrRewrites[span{int(prev), int(cur)}] = rewritten
					count++
				}
			}

		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}

		case xml.CharData:
			if len(stack) == 0 {
				break
			}
			top := stack[len(stack)-1]
			if !isSensitiveName(top) {
				break
			}
			// 跳过纯空白片段，以保持缩进不变。
			if len(bytes.TrimSpace(t)) == 0 {
				break
			}
			redactSpans = append(redactSpans, span{int(prev), int(cur)})
			count++
		}
		prev = cur
	}

	if len(redactSpans) == 0 {
		return content, 0, nil
	}

	// 对 span 排序并去重（按我们的遍历不应重叠，但以防万一）。
	sort.Slice(redactSpans, func(i, j int) bool { return redactSpans[i].start < redactSpans[j].start })
	var out bytes.Buffer
	last := 0
	for _, sp := range redactSpans {
		if sp.start < last {
			continue
		}
		out.Write(content[last:sp.start])
		if rw, ok := attrRewrites[sp]; ok {
			out.Write(rw)
		} else {
			// 对于元素文本：保留周边空白以保持缩进，
			// 只把非空白片段替换为脱敏哨兵值。
			orig := content[sp.start:sp.end]
			out.WriteString(replaceContentPreserveWS(string(orig)))
		}
		last = sp.end
	}
	out.Write(content[last:])
	// 为下一次调用重置属性重写映射。
	for k := range attrRewrites {
		delete(attrRewrites, k)
	}
	return out.Bytes(), count, nil
}

// attrRewrites 是每次调用的临时映射，在 redactXML 内填充
// （顺序、单 goroutine 调用点），用于把重写后的
// 起始标签字节范围拼入输出。以 span 为键，把记录的
// 脱敏映射回其重写后的替换内容。
var attrRewrites = map[struct{ start, end int }]([]byte){}

// replaceContentPreserveWS 用脱敏哨兵值替换文本中的非空白部分，
// 保留开头和结尾的空白片段（使多行、带缩进的标签体
// 在脱敏后仍可阅读）。
func replaceContentPreserveWS(s string) string {
	leading := s[:len(s)-len(strings.TrimLeft(s, " \t\r\n"))]
	trailing := s[len(strings.TrimRight(s, " \t\r\n")):]
	return leading + redacted + trailing
}

// hasSensitiveAttr 在 attrs 中任一属性的名称敏感时返回 true。
func hasSensitiveAttr(attrs []xml.Attr) bool {
	for _, a := range attrs {
		if isSensitiveName(a.Name.Local) {
			return true
		}
	}
	return false
}

// reXMLAttr 匹配起始标签内的 `name="value"` 或 `name='value'`。
// 捕获组：1=name，2=开引号，3=value，4=闭引号。
// 捕获闭引号是为了防止替换时将其
// 再次引入。
var reXMLAttr = regexp.MustCompile(`(\w[\w.\-]*)=(["'])([^"']*)(["'])`)

// redactAttrs 扫描单个 XML 起始标签字节范围，把敏感属性的值
// 替换为脱敏哨兵值。
func redactAttrs(tag []byte) []byte {
	return reXMLAttr.ReplaceAllFunc(tag, func(m []byte) []byte {
		sub := reXMLAttr.FindSubmatch(m)
		if len(sub) != 5 {
			return m
		}
		if !isSensitiveName(string(sub[1])) {
			return m
		}
		// name="value"  →  name="REMOVED"
		out := make([]byte, 0, len(sub[1])+len(redacted)+4)
		out = append(out, sub[1]...)
		out = append(out, '=')
		out = append(out, sub[2]...)
		out = append(out, []byte(redacted)...)
		out = append(out, sub[4]...)
		return out
	})
}

// ── YAML ─────────────────────────────────────────────────────────────

// redactYAML 将 content 解析为 YAML 并遍历生成的节点树，
// 替换键名敏感的标量值。注释由启发式处理擦除。
// 返回序列化后的输出、脱敏次数和任何解析 / 序列化错误。
func redactYAML(content []byte) ([]byte, int, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, 0, fmt.Errorf("YAML 解析失败：%w", err)
	}
	count := 0
	walkYAMLNode(&root, false, &count)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, 0, fmt.Errorf("YAML 序列化失败：%w", err)
	}
	enc.Close()
	return buf.Bytes(), count, nil
}

// walkYAMLNode 深入 n。当节点的映射键敏感时，
// parentIsSensitive 为 true——在这样的节点下，每个嵌套标量
// 都会被脱敏。
//
// 每个节点上的注释字段也会经过启发式处理：粘贴到 YAML 注释中的值
// （`# token: abcdef...`）没有结构化遍历能看到的键，
// 但启发式规则能捕获它。
func walkYAMLNode(n *yaml.Node, parentIsSensitive bool, count *int) {
	if n == nil {
		return
	}
	scrubCommentsOn(n, count)

	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkYAMLNode(c, false, count)
		}

	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			val := n.Content[i+1]
			scrubCommentsOn(key, count)
			isSensitive := isSensitiveName(key.Value)
			if isSensitive && val.Kind == yaml.ScalarNode {
				if val.Value != redacted {
					val.Value = redacted
					val.Tag = "!!str"
					val.Style = 0
					*count++
				}
				continue
			}
			walkYAMLNode(val, isSensitive, count)
		}

	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkYAMLNode(c, parentIsSensitive, count)
		}

	case yaml.ScalarNode:
		if parentIsSensitive && n.Value != redacted {
			n.Value = redacted
			n.Tag = "!!str"
			n.Style = 0
			*count++
		}
	}
}

func scrubCommentsOn(n *yaml.Node, count *int) {
	if n == nil {
		return
	}
	if n.LineComment != "" {
		s, c := RedactCredentialsInText(n.LineComment)
		n.LineComment = s
		*count += c
	}
	if n.HeadComment != "" {
		s, c := RedactCredentialsInText(n.HeadComment)
		n.HeadComment = s
		*count += c
	}
	if n.FootComment != "" {
		s, c := RedactCredentialsInText(n.FootComment)
		n.FootComment = s
		*count += c
	}
}
