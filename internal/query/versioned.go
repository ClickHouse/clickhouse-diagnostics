package query

import (
	"sort"
	"strings"

	"clickhouse-diagnostic/internal"
)

// FindVersionedFiles 在 dir 中发现具有给定扩展名的文件，采用与各模式查询目录
// （queries.cloud/onprem/gov）相同的版本目录约定：dir 根目录下的文件是默认值，
// 名为 ClickHouse 版本（例如 "25.4.1.0"）的子目录在所连接的服务器版本
// >= 该目录版本时覆盖同名的根文件。在多个兼容的版本目录中，版本最高者胜出。
//
// ext 不区分大小写匹配，且必须包含点号（".sql"、".yaml"）。结果按文件名排序，
// 以保证执行顺序确定。
func FindVersionedFiles(dir string, serverVersion internal.Version, ext string) ([]internal.QueryFile, error) {
	all, err := NewFinder().FindCompatibleQueries(dir, serverVersion)
	if err != nil {
		return nil, err
	}

	ext = strings.ToLower(ext)
	filtered := make([]internal.QueryFile, 0, len(all))
	for _, f := range all {
		if strings.HasSuffix(strings.ToLower(f.Name), ext) {
			filtered = append(filtered, f)
		}
	}

	selected := NewSelector().SelectHighestPriorityQueries(filtered)

	out := make([]internal.QueryFile, 0, len(selected))
	for _, f := range selected {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
