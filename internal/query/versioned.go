package query

import (
	"sort"
	"strings"

	"clickhouse-diagnostic/internal"
)

// FindVersionedFiles discovers files with the given extension in dir,
// applying the same version-directory convention as the mode query
// directories (queries.cloud/onprem/gov): files at the root of dir are
// the defaults, and a subdirectory named like a ClickHouse version
// (e.g. "25.4.1.0") overrides a same-named root file when the connected
// server version is >= the directory version. Among several compatible
// version directories, the highest one wins.
//
// ext is matched case-insensitively and must include the dot (".sql",
// ".yaml"). Results are sorted by file name for deterministic
// execution order.
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
