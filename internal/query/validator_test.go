package query

import (
	"strings"
	"testing"
)

// ── 辅助函数 ──────────────────────────────────────────────────────────────────

func mustPass(t *testing.T, name, sql string) {
	t.Helper()
	if err := ValidateQueryContent(sql); err != nil {
		t.Errorf("[%s] 期望 PASS，却得到错误：%v", name, err)
	}
}

func mustFail(t *testing.T, name, sql, wantFragment string) {
	t.Helper()
	err := ValidateQueryContent(sql)
	if err == nil {
		t.Errorf("[%s] 期望 FAIL，但查询通过了", name)
		return
	}
	if wantFragment != "" && !strings.Contains(err.Error(), wantFragment) {
		t.Errorf("[%s] 错误 %q 不包含 %q", name, err.Error(), wantFragment)
	}
}

// ── 合法查询 ──────────────────────────────────────────────────────────────────

func TestValidateQueryContent_ValidQueries(t *testing.T) {
	cases := []struct{ name, sql string }{
		{
			"simple select",
			"SELECT version() AS version\nFORMAT Native",
		},
		{
			"select with where",
			"SELECT database, name FROM system.tables WHERE active = 1\nFORMAT Native",
		},
		{
			"select star",
			"SELECT * FROM system.crash_log\nFORMAT Native",
		},
		{
			"select with cte",
			`WITH t AS (SELECT database, name FROM system.tables)
			 SELECT * FROM t
			 FORMAT Native`,
		},
		{
			"select clusterAllReplicas",
			`SELECT database, name FROM clusterAllReplicas(default, system.tables)
			 FORMAT Native`,
		},
		{
			"select with ARRAY JOIN",
			`SELECT event_time, tables FROM system.query_log
			 ARRAY JOIN tables
			 WHERE event_time > now() - INTERVAL 7 DAY
			 FORMAT Native`,
		},
		{
			"leading whitespace and newlines",
			"\n\n  SELECT 1 AS x\n  FORMAT Native\n",
		},
		{
			"select with hashing (gov style)",
			`SELECT hex(SHA256(concat(database, '%salt%'))) AS database_hash
			 FROM system.query_log
			 FORMAT Native`,
		},
		{
			"column names that look like keywords",
			// create_time、delete_ttl_info_min、update_time 是列名
			`SELECT create_time, delete_ttl_info_min, last_successful_update_time
			 FROM system.parts
			 FORMAT Native`,
		},
		{
			"no FORMAT clause",
			"SELECT 1",
		},
		{
			"JSONCompact format",
			"SELECT version()\nFORMAT JSONCompact",
		},
	}

	for _, tc := range cases {
		mustPass(t, tc.name, tc.sql)
	}
}

// ── 被禁止的查询 ──────────────────────────────────────────────────────────────

func TestValidateQueryContent_ForbiddenQueries(t *testing.T) {
	cases := []struct {
		name         string
		sql          string
		wantFragment string
	}{
		// ── 开头的显式 DML/DDL ──────────────────────────────────────────────
		{
			"INSERT INTO",
			"INSERT INTO t VALUES (1)",
			`"INSERT"`,
		},
		{
			"ALTER TABLE",
			"ALTER TABLE t ADD COLUMN x Int32",
			`"ALTER"`,
		},
		{
			"DROP TABLE",
			"DROP TABLE t",
			`"DROP"`,
		},
		{
			"DELETE FROM",
			"DELETE FROM t WHERE 1=1",
			`"DELETE"`,
		},
		{
			"TRUNCATE",
			"TRUNCATE TABLE t",
			`"TRUNCATE"`,
		},
		{
			"CREATE TABLE",
			"CREATE TABLE t (x Int32) ENGINE=Memory",
			`"CREATE"`,
		},
		{
			"UPDATE",
			"UPDATE t SET x=1 WHERE 1=1",
			`"UPDATE"`,
		},
		{
			"RENAME TABLE",
			"RENAME TABLE a TO b",
			`"RENAME"`,
		},
		{
			"KILL QUERY",
			"KILL QUERY WHERE query_id='x'",
			`"KILL"`,
		},

		// ── INSERT INTO … SELECT（经典的绕过手法）───────────────────────────
		{
			"INSERT INTO … SELECT",
			"INSERT INTO t SELECT * FROM system.tables",
			`"INSERT"`,
		},
		{
			"insert into lowercase",
			"insert into t select 1",
			`"INSERT"`,
		},

		// ── 藏在注释之后 ────────────────────────────────────────────────────
		{
			"INSERT hidden in block comment prefix",
			"/* safe */ INSERT INTO t SELECT 1",
			`"INSERT"`,
		},
		{
			"INSERT after line comment on first line",
			"-- this looks harmless\nINSERT INTO t VALUES(1)",
			`"INSERT"`,
		},
		{
			"DROP hidden after block comment",
			"/* SELECT 1 */ DROP TABLE t",
			`"DROP"`,
		},

		// ── WITH … INSERT（通过 CTE 绕过）───────────────────────────────────
		{
			"WITH cte then INSERT",
			"WITH x AS (SELECT 1 AS n) INSERT INTO t SELECT n FROM x",
			`"INSERT"`,
		},

		// ── 用分号串联的语句 ─────────────────────────────────────────────────
		{
			"semicolon chained",
			"SELECT 1; DROP TABLE t",
			"分号",
		},
		{
			"semicolon with innocent second statement",
			"SELECT version(); SELECT 1",
			"分号",
		},

		// ── 大小写混合 / 空白字符花招 ────────────────────────────────────────
		{
			"mixed-case INSERT",
			"InSeRt INTO t SELECT 1",
			`"INSERT"`,
		},
		{
			"tab before INSERT",
			"\tINSERT INTO t VALUES(1)",
			`"INSERT"`,
		},

		// ── 剥离后为空 ───────────────────────────────────────────────────────
		{
			"only a comment",
			"-- just a comment\n/* another */",
			"为空",
		},
		{
			"only whitespace",
			"   \n\t  ",
			"为空",
		},

		// ── 第一个关键字不是 SELECT 或 WITH ─────────────────────────────────
		{
			"DESCRIBE",
			"DESCRIBE TABLE system.parts",
			"仅允许 SELECT 查询",
		},
		{
			"SHOW",
			"SHOW TABLES",
			"仅允许 SELECT 查询",
		},
		{
			"EXPLAIN",
			"EXPLAIN SELECT 1",
			"仅允许 SELECT 查询",
		},
		{
			"OPTIMIZE",
			"OPTIMIZE TABLE t FINAL",
			"仅允许 SELECT 查询",
		},
	}

	for _, tc := range cases {
		mustFail(t, tc.name, tc.sql, tc.wantFragment)
	}
}

// ── 确认现有查询文件能通过 ───────────────────────────────────────────────────

func TestValidateQueryContent_ExistingQueryFiles(t *testing.T) {
	// 抽查仓库中的几个真实查询，确保合法文件能通过。
	realQueries := []struct{ name, sql string }{
		{
			"system.version",
			"SELECT \n  version() as version\nFORMAT Native\n",
		},
		{
			"system.parts (cloud)",
			`SELECT
  partition, name, part_type, active, marks, rows, bytes_on_disk,
  data_compressed_bytes, data_uncompressed_bytes, database, table, engine
FROM clusterAllReplicas(default, system.parts)
FORMAT Native`,
		},
		{
			"system.query_log_details cloud",
			`SELECT
    toStartOfInterval(event_time, toIntervalHour(1)) AS time,
    query_kind, tables, type, user,
    sum(memory_usage) as memory_usage,
    count(*) as count
FROM clusterAllReplicas(default, system.query_log)
ARRAY JOIN tables
WHERE (event_time > (now() - toIntervalDay(15)))
GROUP BY ALL
FORMAT Native`,
		},
		{
			"system.query_log_details gov (hashed)",
			`SELECT
    time,
    query_kind,
    hex(SHA256(concat(tables, '%salt%'))) AS tables,
    type,
    hex(SHA256(concat(user, '%salt%'))) AS user,
    memory_usage, count, minDate, maxDate, exception_code
FROM (
    SELECT
        toStartOfInterval(event_time, toIntervalHour(1)) AS time,
        query_kind,
        tables,
        type,
        user,
        sum(memory_usage) as memory_usage,
        count(*) as count,
        min(event_time) as minDate,
        max(event_time) as maxDate,
        exception_code
    FROM system.query_log
    ARRAY JOIN tables
    WHERE (event_time > (now() - toIntervalDay(15)))
    GROUP BY ALL
)
FORMAT Native`,
		},
	}

	for _, tc := range realQueries {
		mustPass(t, tc.name, tc.sql)
	}
}
