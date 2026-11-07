package collection

import (
	"strings"
	"testing"
)

// 下面的 DDL 形态是 22.8 服务器上
// system.tables.create_table_query / engine_full 的样子，此时服务端不做任何屏蔽。
// 在 ≥ 23.x 上服务器已经写入 '[HIDDEN]'，脱敏器必须保持原样不动。
func TestRedactSQLText_PositionalEngineSecrets(t *testing.T) {
	cases := []struct {
		name, in   string
		wantHidden []string // 必须消失的子串
		wantKept   []string // 必须保留的子串
	}{
		{
			name:       "S3 with key id + secret + format",
			in:         `CREATE TABLE d.t (x UInt8) ENGINE = S3('https://b.s3.amazonaws.com/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY', 'CSV')`,
			wantHidden: []string{"wJalrXUtnFEMI", "AKIAIOSFODNN7EXAMPLE"},
			wantKept:   []string{"https://b.s3.amazonaws.com/f.csv", "'CSV'"},
		},
		{
			name:       "S3 with session token before the format",
			in:         `S3('https://b/f', 'AKIAIOSFODNN7EXAMPLE', 'sEcReT', 'FwoGZXIvYXdzEBYaDHtoken', 'Parquet')`,
			wantHidden: []string{"sEcReT", "FwoGZXIvYXdzEBYaDHtoken"},
			wantKept:   []string{"'Parquet'"},
		},
		{
			name:     "S3 url + format only: nothing to hide",
			in:       `S3('https://b/f.csv', 'CSV', 'gzip')`,
			wantKept: []string{"'CSV'", "'gzip'"},
		},
		{
			name:     "S3 NOSIGN: anonymous, nothing to hide",
			in:       `s3('https://b/f.csv', NOSIGN, 'CSV')`,
			wantKept: []string{"NOSIGN", "'CSV'"},
		},
		{
			name:       "MySQL table engine: password is arg 5",
			in:         `ENGINE = MySQL('mysql-host:3306', 'db', 'tbl', 'dbuser', 'Sup3rS3cretPw!')`,
			wantHidden: []string{"Sup3rS3cretPw!"},
			wantKept:   []string{"'mysql-host:3306'", "'db'", "'tbl'", "'dbuser'"},
		},
		{
			name:       "MySQL database engine: password is arg 4",
			in:         `CREATE DATABASE m ENGINE = MySQL('h:3306', 'db', 'u', 'dbpw')`,
			wantHidden: []string{"'dbpw'"},
			wantKept:   []string{"'h:3306'", "'db'", "'u'"},
		},
		{
			name:       "PostgreSQL",
			in:         `PostgreSQL('pg:5432', 'db', 'tbl', 'pguser', 'pgpw', 'schema')`,
			wantHidden: []string{"'pgpw'"},
			wantKept:   []string{"'schema'", "'pguser'"},
		},
		{
			name:       "table function nested inside an MV SELECT",
			in:         `CREATE MATERIALIZED VIEW d.mv TO d.t AS SELECT * FROM s3('https://b/f', 'AKIAIOSFODNN7EXAMPLE', 'nestedSecret', 'CSV') WHERE x > 1`,
			wantHidden: []string{"nestedSecret"},
			wantKept:   []string{"WHERE x > 1", "TO d.t"},
		},
		{
			name:       "remote() with db.table shorthand: 4 args, password last",
			in:         `SELECT * FROM remote('h1:9000', 'db.t', 'ruser', 'rpw')`,
			wantHidden: []string{"'rpw'"},
			wantKept:   []string{"'db.t'", "'h1:9000'"},
		},
		{
			name:       "s3Cluster shifts by the cluster name",
			in:         `s3Cluster('default', 'https://b/f', 'AKIAIOSFODNN7EXAMPLE', 'clusterSecret', 'CSV')`,
			wantHidden: []string{"clusterSecret"},
			wantKept:   []string{"'default'", "'CSV'"},
		},
		{
			name:       "Azure account key in position 5 and in the connection string",
			in:         `AzureBlobStorage('DefaultEndpointsProtocol=https;AccountName=acc;AccountKey=abc123==;EndpointSuffix=core.windows.net', 'cont', 'blob', 'acc', 'k3y', 'CSV')`,
			wantHidden: []string{"AccountKey=abc123", "'k3y'"},
			wantKept:   []string{"'cont'", "'CSV'"},
		},
		{
			name:       "Kafka credentials live in SETTINGS, caught by the keyword rule",
			in:         `ENGINE = Kafka SETTINGS kafka_broker_list = 'b:9092', kafka_sasl_username = 'u', kafka_sasl_password = 'k4fkaPw', kafka_format = 'JSONEachRow'`,
			wantHidden: []string{"k4fkaPw"},
			wantKept:   []string{"kafka_broker_list = 'b:9092'", "kafka_format = 'JSONEachRow'", "kafka_sasl_password = '[HIDDEN]'"},
		},
		{
			name:       "dictionary SOURCE keyword form",
			in:         `SOURCE(MYSQL(host 'mysql-host' port 3306 user 'dbuser' password 'DictPw!123' db 'db' table 'tbl'))`,
			wantHidden: []string{"DictPw!123"},
			wantKept:   []string{"host 'mysql-host'", "table 'tbl'"},
		},
		{
			name:       "URL basic auth",
			in:         `URL('https://alice:s3cr3t@example.com/data.json', 'JSONEachRow')`,
			wantHidden: []string{"s3cr3t"},
			wantKept:   []string{"alice:", "@example.com/data.json"},
		},
		{
			name:     "already masked by a >= 23.x server: idempotent",
			in:       `S3('https://b/f', 'AKIAIOSFODNN7EXAMPLE', '[HIDDEN]', 'CSV')`,
			wantKept: []string{"'[HIDDEN]'"},
		},
		{
			name:     "named collection reference is an identifier, not a secret",
			in:       `S3(my_s3_collection, format = 'CSV')`,
			wantKept: []string{"my_s3_collection"},
		},
		{
			name:     "truncated DDL: emitted unchanged past the break",
			in:       `ENGINE = MySQL('h', 'db', 'tbl', 'u', 'pw`,
			wantKept: []string{"MySQL('h', 'db', 'tbl', 'u', 'pw"},
		},
		{
			name:     "a quoted string that merely mentions s3( is not a call",
			in:       `COMMENT 'loaded via s3(...) nightly'`,
			wantKept: []string{"'loaded via s3(...) nightly'"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := RedactSQLText(c.in)
			for _, h := range c.wantHidden {
				if strings.Contains(got, h) {
					t.Errorf("仍然包含 %q：\n%s", h, got)
				}
			}
			for _, k := range c.wantKept {
				if !strings.Contains(got, k) {
					t.Errorf("丢失了 %q：\n%s", k, got)
				}
			}
			if strings.Count(got, "'") != strings.Count(c.in, "'") && !strings.Contains(c.name, "truncated") && !strings.Contains(c.name, "URL") {
				t.Errorf("引号数量变了——某个字面量未就地替换：\n in: %s\nout: %s", c.in, got)
			}
			if strings.Contains(got, "REMOVED") {
				t.Errorf("SQL 脱敏必须使用 [HIDDEN]，即服务器自己的 token，而不是配置哨兵值：\n%s", got)
			}
		})
	}
}

func TestRedactSQLText_Idempotent(t *testing.T) {
	in := `CREATE TABLE d.t (x UInt8) ENGINE = S3('https://b/f', 'AKIAIOSFODNN7EXAMPLE', 'secret', 'CSV') SETTINGS s3_secret_access_key = 'again'`
	once, n1 := RedactSQLText(in)
	twice, n2 := RedactSQLText(once)
	if once != twice {
		t.Errorf("不是幂等的：\n1: %s\n2: %s", once, twice)
	}
	if n1 == 0 || n2 != 0 {
		t.Errorf("计数：first=%d second=%d，期望 first>0 second=0", n1, n2)
	}
}

// JSONL 钩子只能触及指定字段，且只在值确实改变的地方：
// 64 位整数保持带引号，键顺序保持服务器的顺序，
// 每个未被触及的行字节完全一致。
func TestRedactJSONLFields(t *testing.T) {
	line1 := `{"database":"sec","name":"t_s3","uuid":"0b6e-…","total_rows":"12345678901234567890","create_table_query":"CREATE TABLE sec.t_s3 (` + "`x`" + ` UInt8) ENGINE = S3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrX\\/secret', 'CSV')","engine_full":"S3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrX\\/secret', 'CSV')","as_select":""}`
	line2 := `{"database":"d","name":"plain","uuid":"1","total_rows":"7","create_table_query":"CREATE TABLE d.plain (x UInt8) ENGINE = MergeTree ORDER BY x","engine_full":"MergeTree ORDER BY x SETTINGS index_granularity = 8192","as_select":""}`
	body := line1 + "\n" + line2 + "\n"

	got, n := RedactJSONLFields(body, []string{"create_table_query", "engine_full", "as_select"})
	if n == 0 {
		t.Fatal("期望第 1 行发生脱敏")
	}
	lines := strings.Split(got, "\n")
	if lines[1] != line2 {
		t.Errorf("未被触及的行必须字节完全一致：\n got: %s\nwant: %s", lines[1], line2)
	}
	if lines[2] != "" {
		t.Errorf("末尾换行丢失")
	}
	if strings.Contains(lines[0], "secret") || strings.Contains(lines[0], "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("凭据幸存：%s", lines[0])
	}
	for _, keep := range []string{`"total_rows":"12345678901234567890"`, `"database":"sec"`, `"uuid":"0b6e-…"`, `[HIDDEN]`} {
		if !strings.Contains(lines[0], keep) {
			t.Errorf("第 1 行丢失了 %q：%s", keep, lines[0])
		}
	}
	// 键顺序是服务器的顺序，而不是 Go 的 map 顺序。
	if strings.Index(lines[0], `"database"`) > strings.Index(lines[0], `"create_table_query"`) {
		t.Errorf("键顺序变了：%s", lines[0])
	}
}

func TestRedactJSONLFields_NonStringAndMissingFieldsAreLeftAlone(t *testing.T) {
	body := `{"a":1,"source":null,"engine_full":42}` + "\n" + `{"a":2}`
	got, n := RedactJSONLFields(body, []string{"source", "engine_full", "create_table_query"})
	if got != body || n != 0 {
		t.Errorf("得到 %q（n=%d），期望保持不变", got, n)
	}
}

// 配置脱敏器的行为在参数化其哨兵值的重构之后
// 必须保持不变。
func TestRedactCredentialsInText_StillUsesRemoved(t *testing.T) {
	got, n := RedactCredentialsInText(`password = 'abc' and https://u:pw@h/x`)
	if n != 2 || !strings.Contains(got, "password = REMOVED") || !strings.Contains(got, "https://u:REMOVED@h/x") {
		t.Errorf("得到 %q（n=%d）", got, n)
	}
}

// 评审发现：关键词规则的 [^'"]+ 会在转义引号处停止，于是
// password = 'pa\'ss' 变成 '[HIDDEN]'ss'——密码尾部留了下来。
// 现在值的结尾通过按 SQL 转义规则扫描字面量来找到。
func TestRedactSQLText_QuotedValuesWithEscapedQuotes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`SETTINGS password = 'pa\'ss'`, `SETTINGS password = '[HIDDEN]'`},
		{`SETTINGS password = 'pa''ss', x = 1`, `SETTINGS password = '[HIDDEN]', x = 1`},
		{`kafka_sasl_password = 'ends\\', kafka_format = 'CSV'`, `kafka_sasl_password = '[HIDDEN]', kafka_format = 'CSV'`},
		{`SOURCE(MYSQL(user 'u' password 'it''s' db 'd'))`, `SOURCE(MYSQL(user 'u' password '[HIDDEN]' db 'd'))`},
		{`password = 'unterminated`, `password = '[HIDDEN]'`},
	}
	for _, c := range cases {
		got, _ := RedactSQLText(c.in)
		if got != c.want {
			t.Errorf("\n 输入: %s\n得到: %s\n期望: %s", c.in, got, c.want)
		}
	}
	// 以及配置脱敏器的路径，使用它自己的哨兵值。
	got, _ := RedactCredentialsInText(`# password = 'we\'ird' rotate quarterly`)
	if got != `# password = REMOVED rotate quarterly` {
		t.Errorf("配置路径：%q", got)
	}
}

// 评审发现：JSONL 映射脱敏了 dashboard 和图的
// last_exception，但没有脱敏持久化的 system.dictionaries 文件。
func TestSensitiveCollectorFields_DictionariesCoverLastException(t *testing.T) {
	fields := SensitiveCollectorFields["system.dictionaries.sql"]
	for _, want := range []string{"source", "last_exception"} {
		found := false
		for _, f := range fields {
			found = found || f == want
		}
		if !found {
			t.Errorf("system.dictionaries.sql 脱敏缺少 %q（fields：%v）", want, fields)
		}
	}
	line := `{"name":"d","status":"FAILED","source":"","last_exception":"mysqlxx::ConnectionFailed: Can't connect (password = 'geo\\'pw')"}` + "\n"
	out, n := RedactJSONLFields(line, fields)
	if n == 0 || strings.Contains(out, "geo") {
		t.Errorf("last_exception 未脱敏（n=%d）：%s", n, out)
	}
}

// 评审发现：nextCall 只跳过单引号字面量，因此反引号
// 或双引号标识符，或包含 "s3(" 的注释，会被当成
// 引擎调用。matchingParen 随后会把那个 '(' 与真实调用的 ')'
// 配对（或一路跑到末尾），真实密码便落在位置规则检查的
// 范围之外。system.tables 中格式化过的 DDL 很少携带
// 这类文本，但脱敏路径不能依赖这一点。
func TestRedactSQLText_SkipsQuotedIdentifiersAndComments(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"CREATE TABLE d.`s3(` (`x` UInt8) ENGINE = S3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXsecret', 'CSV')",
			"CREATE TABLE d.`s3(` (`x` UInt8) ENGINE = S3('https://b/f.csv', '[HIDDEN]', '[HIDDEN]', 'CSV')",
		},
		{
			`CREATE TABLE d."s3(" ("x" UInt8) ENGINE = S3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXsecret', 'CSV')`,
			`CREATE TABLE d."s3(" ("x" UInt8) ENGINE = S3('https://b/f.csv', '[HIDDEN]', '[HIDDEN]', 'CSV')`,
		},
		{
			"/* loads via s3( */ SELECT * FROM s3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXsecret', 'CSV')",
			"/* loads via s3( */ SELECT * FROM s3('https://b/f.csv', '[HIDDEN]', '[HIDDEN]', 'CSV')",
		},
		{
			"-- s3( is the source\nSELECT * FROM s3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrXsecret', 'CSV')",
			"-- s3( is the source\nSELECT * FROM s3('https://b/f.csv', '[HIDDEN]', '[HIDDEN]', 'CSV')",
		},
		// 引用标识符内的 ')' 或 ',' 绝不能关闭或切分参数列表。
		{
			"SELECT * FROM mysql('h:3306', 'db', `t)`, 'u', 'pw')",
			"SELECT * FROM mysql('h:3306', 'db', `t)`, 'u', '[HIDDEN]')",
		},
		// 未终止的区域会一直延伸到末尾，不 panic 也不死循环。
		{"SELECT `unterminated", "SELECT `unterminated"},
		{"SELECT /* open", "SELECT /* open"},
	}
	for _, c := range cases {
		got, _ := RedactSQLText(c.in)
		if got != c.want {
			t.Errorf("\n 输入: %s\n得到: %s\n期望: %s", c.in, got, c.want)
		}
	}
}

// 评审发现：JSONL 字段定位器原本是原始子串搜索。合法
// JSON 会转义值中的每个引号，因此 needle 不可能出现在那里，
// 但定位器现在还要求处于键位置，使这一保证不再
// 依赖上述论断。拼出键名的值，以及以字段名结尾的更长键，
// 都必须跳过，转而匹配真正的成员。
func TestRedactJSONLFields_MatchesKeysNotValues(t *testing.T) {
	line := `{"comment":"see {\"source\":\"MYSQL(password 'inner')\"}","x_source":"MYSQL(user 'u' password 'longer')","source":"MYSQL(user 'u' password 'real' db 'd')"}`
	got, n := RedactJSONLFields(line, []string{"source"})
	if n != 1 {
		t.Fatalf("n = %d，期望 1（只有真正的 source 成员）：%s", n, got)
	}
	if strings.Contains(got, "'real'") {
		t.Errorf("真正成员的凭据幸存：%s", got)
	}
	for _, keep := range []string{`\"source\":\"MYSQL(password 'inner')\"`, `"x_source":"MYSQL(user 'u' password 'longer')"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("非成员的出现位置被重写；丢失了 %q：%s", keep, got)
		}
	}
	// 分隔符之后的空白仍属于键位置。
	spaced := `{"a": 1, "source": "MYSQL(user 'u' password 'p')"}`
	if got, n := RedactJSONLFields(spaced, []string{"source"}); n != 1 || strings.Contains(got, "'p'") {
		t.Errorf("带空白的成员未脱敏：%s", got)
	}
}
