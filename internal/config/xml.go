package config

import "regexp"

// xmlComment 匹配 XML 注释，包括跨行的注释。
var xmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// StripComments 移除 ClickHouse 配置文件中的 XML 注释，
// 避免其中被注释掉的示例被当作配置读取。
//
// 这一点很重要，因为 ClickHouse 自带的 config.xml 会把整段示例块放在注释里——
// 命名 example1 / example2 / example3 的 `<zookeeper>` 块出现在每次默认安装中——
// 而 ClickHouse 的解析器会忽略它们，所以任何按模式扫描这些文件的代码也必须忽略它们，
// 否则就会读到运维人员从未配置过的主机和路径。
//
// 这是词法剥离，而非解析：属性或文本节点里的字面量 "<!--" 会干扰它。
// ClickHouse 配置里没有这种情况，而替代方案——
// 对合法包含变量替换和重复键的文件做完整 XML 解析——风险更大。
func StripComments(b []byte) []byte {
	return xmlComment.ReplaceAll(b, nil)
}
