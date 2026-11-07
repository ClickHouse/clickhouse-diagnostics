package pkg

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// reSystemTable 匹配 SQL 字符串中任意位置直接引用的 `system.<table>`。
// 末尾的单词边界可避免部分匹配到 `system.parts_something`——
// `[a-z_]+` 是贪婪匹配。
var reSystemTable = regexp.MustCompile(`(?i)\bsystem\.[a-z_][a-z0-9_]*\b`)

// reMergeTable 匹配 `merge(<db>, '<pattern>')` 表函数，
// 少数查询会用它在带版本的表上聚合
// （例如 clusterAllReplicas 包裹 `merge(system, '^asynchronous_insert_log')`）。
// 表名本身就是一个正则表达式，因此这里原样报告——
// 阅读 dry-run 输出的运维人员知道该读什么。
var reMergeTable = regexp.MustCompile(`(?i)merge\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*,\s*'([^']+)'\s*\)`)

// ExtractTables 返回在 sql 中找到的 system 表引用的去重排序列表。
// 覆盖范围：
//
//   - 直接引用：           system.parts、system.query_log、……
//   - clusterAllReplicas 包裹：通过内层的 system.X 提取
//     （这个正则不关心中间的调用，它直接匹配内层引用）。
//   - merge() 模式：            "<db>.* matching '<pattern>'"
//
// 供 --dry-run 为每条打印的查询标注它会访问的 system 表。
// 尽力而为——对于本工具生成的查询，手写 SQL 解析器过于小题大做。
func ExtractTables(sql string) []string {
	seen := map[string]struct{}{}
	for _, m := range reSystemTable.FindAllString(sql, -1) {
		seen[strings.ToLower(m)] = struct{}{}
	}
	for _, m := range reMergeTable.FindAllStringSubmatch(sql, -1) {
		seen[fmt.Sprintf("%s.* matching '%s'", strings.ToLower(m[1]), m[2])] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
