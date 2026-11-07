package query

import (
	"fmt"
	"strings"
)

// 大型 SharedMergeTree 服务器上的 detached part（分离 part）。
//
// system.detached_parts 会列出每张表的 detached/ 目录 —— 每张表一次列表操作，
// 在元数据存放在 Keeper 的对象存储磁盘上属于远程调用（在拥有数万张表的服务器上
// 曾观察到每次运行耗时 20–300 秒、返回 0 行）。在 SharedMergeTree 上，所审查的
// 每次升级中 broken part 都最先出现在日志和错误里，因此在这类服务器上，
// auto 或 full 运行会列出所有非 shared 表，加上 broken-part 日志行点名的 shared 表；
// -collect full 则列出全部。

// SignalTablesQuery 查找近期 broken-part 日志行中点名的表
// （logger_name 以 "<database>.<table>" 开头）。
func SignalTablesQuery(mode string) string {
	src := "system.text_log"
	if mode == "cloud" {
		src = "clusterAllReplicas(default, system.text_log)"
	}
	return fmt.Sprintf(`SELECT DISTINCT
    extract(logger_name, '^([^.\\s]+)\\.')       AS db,
    extract(logger_name, '^[^.\\s]+\\.([^\\s]+)') AS tbl
FROM %s
WHERE event_date >= toDate(now() - INTERVAL 1 DAY) AND event_time > now() - INTERVAL 1 DAY
  AND level IN ('Fatal', 'Critical', 'Error', 'Warning')
  AND (message ILIKE '%%broken%%part%%' OR message ILIKE '%%unexpected%%part%%' OR message ILIKE '%%detach%%part%%')
  AND db != '' AND tbl != ''
LIMIT 1000
FORMAT TSV`, src)
}

// ParseSignalTables 解析 SignalTablesQuery 的 TSV，丢弃格式错误和重复的行。
func ParseSignalTables(tsv string) [][2]string {
	var out [][2]string
	seen := map[[2]string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(tsv), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 || f[0] == "" || f[1] == "" {
			continue
		}
		k := [2]string{f[0], f[1]}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// DetachedScope 是 {detached_scope} 谓词：未设置 limit 时为 "1"（所有表），
// 否则为所有非 shared 表加上被点名的表。qualifier 用作列前缀
// （gov 文件中为 "p."，其中 database 是哈希后的别名）。
func DetachedScope(limit bool, tables [][2]string, qualifier string) string {
	if !limit {
		return "1"
	}
	cols := fmt.Sprintf("(%sdatabase, %stable)", qualifier, qualifier)
	s := cols + " IN (SELECT database, name FROM system.tables WHERE engine NOT LIKE 'Shared%')"
	if len(tables) > 0 {
		lits := make([]string, 0, len(tables))
		for _, t := range tables {
			lits = append(lits, fmt.Sprintf("(%s, %s)", sqlString(t[0]), sqlString(t[1])))
		}
		s += " OR " + cols + " IN (" + strings.Join(lits, ", ") + ")"
	}
	return s
}

func sqlString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}
