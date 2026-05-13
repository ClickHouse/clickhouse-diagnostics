package query

import (
	"fmt"
	"regexp"
	"strings"
)

// These regexps are compiled once at package load.
var (
	// Strip /* ... */ block comments (including multi-line).
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

	// Strip -- ... line comments.
	reLineComment = regexp.MustCompile(`(?m)--[^\n]*$`)

	// Strip the optional ClickHouse FORMAT clause that ends a query
	// (e.g. "FORMAT Native", "FORMAT JSONCompact").
	reFormatClause = regexp.MustCompile(`(?is)\s+FORMAT\s+\w+\s*$`)

	// Forbidden DML / DDL keywords — matched as whole words so that
	// identifiers like "create_time" or "delete_ttl_info_min" are not
	// false-positives.
	reForbidden = regexp.MustCompile(
		`(?i)\b(INSERT|UPDATE|DELETE|DROP|TRUNCATE|ALTER|CREATE|RENAME|ATTACH|DETACH|REPLACE|KILL)\b`,
	)
)

// ValidateQueryContent checks that sql contains only a read-only SELECT query.
//
// Rules enforced (in order):
//  1. SQL comments (-- and /* */) are stripped before any keyword check so
//     that a hidden dangerous command cannot bypass validation through a comment.
//  2. The ClickHouse FORMAT clause is stripped (it is not part of the query logic).
//  3. No semicolons — prevents multiple statements being chained together.
//  4. The first effective keyword must be SELECT or WITH (CTEs).
//  5. No forbidden DML/DDL keyword appears anywhere in the cleaned query,
//     which also catches patterns like "WITH x AS (…) INSERT INTO …" and
//     "INSERT INTO … SELECT …".
func ValidateQueryContent(sql string) error {
	// 1. Strip comments.
	clean := reBlockComment.ReplaceAllString(sql, " ")
	clean = reLineComment.ReplaceAllString(clean, " ")

	// 2. Strip FORMAT clause.
	clean = reFormatClause.ReplaceAllString(clean, "")

	// 3. Collapse and trim whitespace.
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return fmt.Errorf("query is empty after stripping comments and FORMAT clause")
	}

	// 4. No semicolons — prevents chained statements.
	if strings.ContainsRune(clean, ';') {
		return fmt.Errorf("query contains a semicolon; multiple statements are not allowed")
	}

	// 5. First effective keyword must be SELECT or WITH.
	firstWord := strings.ToUpper(strings.Fields(clean)[0])
	if firstWord != "SELECT" && firstWord != "WITH" {
		return fmt.Errorf("only SELECT queries are allowed; query starts with %q", firstWord)
	}

	// 6. No forbidden DML/DDL keyword anywhere in the query.
	//    This catches INSERT INTO … SELECT, WITH … INSERT INTO …, etc.
	if match := reForbidden.FindString(clean); match != "" {
		return fmt.Errorf("forbidden keyword %q is not allowed in query files", strings.ToUpper(match))
	}

	return nil
}
