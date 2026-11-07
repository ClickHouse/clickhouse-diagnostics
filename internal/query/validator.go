package query

import (
	"fmt"
	"regexp"
	"strings"
)

// 这些正则在包加载时编译一次。
var (
	// 去掉 /* ... */ 块注释（包括多行）。
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

	// 去掉 -- ... 行注释。
	reLineComment = regexp.MustCompile(`(?m)--[^\n]*$`)

	// 去掉查询末尾可选的 ClickHouse FORMAT 子句
	//（例如 "FORMAT Native"、"FORMAT JSONCompact"）。
	reFormatClause = regexp.MustCompile(`(?is)\s+FORMAT\s+\w+\s*$`)

	// 禁用的 DML / DDL 关键字 —— 按整词匹配，这样像 "create_time" 或
	// "delete_ttl_info_min" 这样的标识符不会被误判。
	reForbidden = regexp.MustCompile(
		`(?i)\b(INSERT|UPDATE|DELETE|DROP|TRUNCATE|ALTER|CREATE|RENAME|ATTACH|DETACH|REPLACE|KILL)\b`,
	)
)

// ValidateQueryContent 检查 sql 是否只包含只读的 SELECT 查询。
//
// 强制执行的规则（按顺序）：
//  1. 在检查任何关键字之前先去掉 SQL 注释（-- 和 /* */），
//     这样隐藏的危险命令就无法通过注释绕过校验。
//  2. 去掉 ClickHouse FORMAT 子句（它不属于查询逻辑）。
//  3. 不允许分号 —— 防止把多条语句串联起来。
//  4. 第一个有效关键字必须是 SELECT 或 WITH（CTE）。
//  5. 清理后的查询中任何位置都不得出现禁用的 DML/DDL 关键字，
//     这也能捕获诸如 "WITH x AS (…) INSERT INTO …" 和
//     "INSERT INTO … SELECT …" 之类的模式。
func ValidateQueryContent(sql string) error {
	// 1. 去掉注释。
	clean := reBlockComment.ReplaceAllString(sql, " ")
	clean = reLineComment.ReplaceAllString(clean, " ")

	// 2. 去掉 FORMAT 子句。
	clean = reFormatClause.ReplaceAllString(clean, "")

	// 3. 合并并去除首尾空白。
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return fmt.Errorf("去除注释和 FORMAT 子句后查询为空")
	}

	// 4. 不允许分号 —— 防止串联语句。
	if strings.ContainsRune(clean, ';') {
		return fmt.Errorf("查询包含分号；不允许执行多条语句")
	}

	// 5. 第一个有效关键字必须是 SELECT 或 WITH。
	firstWord := strings.ToUpper(strings.Fields(clean)[0])
	if firstWord != "SELECT" && firstWord != "WITH" {
		return fmt.Errorf("仅允许 SELECT 查询；查询以 %q 开头", firstWord)
	}

	// 6. 查询中任何位置都不得出现禁用的 DML/DDL 关键字。
	//    这可以捕获 INSERT INTO … SELECT、WITH … INSERT INTO … 等。
	if match := reForbidden.FindString(clean); match != "" {
		return fmt.Errorf("查询文件中不允许出现禁用关键字 %q", strings.ToUpper(match))
	}

	return nil
}
