package query

import (
	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/version"
)

// Selector 负责查询的选择与优先级排序
type Selector struct {
	parser   *version.Parser
	comparer *version.Comparer
}

// NewSelector 创建一个新的查询选择器
func NewSelector() *Selector {
	return &Selector{
		parser:   version.NewParser(),
		comparer: version.NewComparer(),
	}
}

// SelectHighestPriorityQueries 选择优先级最高的查询（目录名最大者）
func (s *Selector) SelectHighestPriorityQueries(allQueries []internal.QueryFile) map[string]internal.QueryFile {
	// 映射：每个唯一文件名对应的最佳查询
	bestQueries := make(map[string]internal.QueryFile)

	for _, query := range allQueries {
		// 检查是否已经见过这个文件名
		existing, exists := bestQueries[query.Name]

		if !exists {
			// 第一次见到这个文件名
			bestQueries[query.Name] = query
		} else {
			// 判断哪个查询的优先级更高
			if s.hasHigherPriority(query, existing) {
				bestQueries[query.Name] = query
			}
		}
	}

	return bestQueries
}

// hasHigherPriority 判断 query1 的优先级是否高于 query2
func (s *Selector) hasHigherPriority(query1, query2 internal.QueryFile) bool {
	// 若一个来自根目录、另一个来自版本目录，优先版本目录中的那个
	if query2.DirName == "" && query1.DirName != "" {
		return true
	}
	if query1.DirName == "" && query2.DirName != "" {
		return false
	}

	// 两者都来自根目录，或都来自版本目录
	if query1.DirName == "" && query2.DirName == "" {
		// 两者都来自根目录，保留已有的那个（任意选择）
		return false
	}

	// 两者都来自版本目录，比较版本
	version1, err1 := s.parser.ParseVersionFromDirName(query1.DirName)
	version2, err2 := s.parser.ParseVersionFromDirName(query2.DirName)

	// 如果无法解析版本，优先第一个
	if err1 != nil || err2 != nil {
		return false
	}

	// 选择版本最高的那个
	return s.comparer.IsGreater(version1, version2)
}

// GroupQueriesByType 按类型或类别对查询分组
func (s *Selector) GroupQueriesByType(queries map[string]internal.QueryFile) map[string][]internal.QueryFile {
	groups := make(map[string][]internal.QueryFile)

	for _, query := range queries {
		var group string
		if query.DirName == "" {
			group = "root"
		} else {
			group = query.DirName
		}

		groups[group] = append(groups[group], query)
	}

	return groups
}
