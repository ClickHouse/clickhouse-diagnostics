package collection

import (
	"strings"
	"testing"
)

// The DDL shapes below are what system.tables.create_table_query / engine_full
// look like on a 22.8 server, where nothing is masked server-side. On ≥ 23.x
// the server already writes '[HIDDEN]', and the redactor must leave that alone.
func TestRedactSQLText_PositionalEngineSecrets(t *testing.T) {
	cases := []struct {
		name, in   string
		wantHidden []string // substrings that must be gone
		wantKept   []string // substrings that must survive
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
					t.Errorf("still contains %q:\n%s", h, got)
				}
			}
			for _, k := range c.wantKept {
				if !strings.Contains(got, k) {
					t.Errorf("lost %q:\n%s", k, got)
				}
			}
			if strings.Count(got, "'") != strings.Count(c.in, "'") && !strings.Contains(c.name, "truncated") && !strings.Contains(c.name, "URL") {
				t.Errorf("quote count changed — a literal was not replaced in place:\n in: %s\nout: %s", c.in, got)
			}
			if strings.Contains(got, "REMOVED") {
				t.Errorf("SQL redaction must use [HIDDEN], the server's own token, not the config sentinel:\n%s", got)
			}
		})
	}
}

func TestRedactSQLText_Idempotent(t *testing.T) {
	in := `CREATE TABLE d.t (x UInt8) ENGINE = S3('https://b/f', 'AKIAIOSFODNN7EXAMPLE', 'secret', 'CSV') SETTINGS s3_secret_access_key = 'again'`
	once, n1 := RedactSQLText(in)
	twice, n2 := RedactSQLText(once)
	if once != twice {
		t.Errorf("not idempotent:\n1: %s\n2: %s", once, twice)
	}
	if n1 == 0 || n2 != 0 {
		t.Errorf("counts: first=%d second=%d, want first>0 second=0", n1, n2)
	}
}

// The JSONL hook must touch only the named fields and only where a value
// changed: 64-bit integers stay quoted, key order stays the server's, and
// every untouched line is byte-identical.
func TestRedactJSONLFields(t *testing.T) {
	line1 := `{"database":"sec","name":"t_s3","uuid":"0b6e-…","total_rows":"12345678901234567890","create_table_query":"CREATE TABLE sec.t_s3 (` + "`x`" + ` UInt8) ENGINE = S3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrX\\/secret', 'CSV')","engine_full":"S3('https://b/f.csv', 'AKIAIOSFODNN7EXAMPLE', 'wJalrX\\/secret', 'CSV')","as_select":""}`
	line2 := `{"database":"d","name":"plain","uuid":"1","total_rows":"7","create_table_query":"CREATE TABLE d.plain (x UInt8) ENGINE = MergeTree ORDER BY x","engine_full":"MergeTree ORDER BY x SETTINGS index_granularity = 8192","as_select":""}`
	body := line1 + "\n" + line2 + "\n"

	got, n := RedactJSONLFields(body, []string{"create_table_query", "engine_full", "as_select"})
	if n == 0 {
		t.Fatal("expected redactions on line 1")
	}
	lines := strings.Split(got, "\n")
	if lines[1] != line2 {
		t.Errorf("untouched line must be byte-identical:\n got: %s\nwant: %s", lines[1], line2)
	}
	if lines[2] != "" {
		t.Errorf("trailing newline lost")
	}
	if strings.Contains(lines[0], "secret") || strings.Contains(lines[0], "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("credential survived: %s", lines[0])
	}
	for _, keep := range []string{`"total_rows":"12345678901234567890"`, `"database":"sec"`, `"uuid":"0b6e-…"`, `[HIDDEN]`} {
		if !strings.Contains(lines[0], keep) {
			t.Errorf("line 1 lost %q: %s", keep, lines[0])
		}
	}
	// Key order is the server's, not Go's map order.
	if strings.Index(lines[0], `"database"`) > strings.Index(lines[0], `"create_table_query"`) {
		t.Errorf("key order changed: %s", lines[0])
	}
}

func TestRedactJSONLFields_NonStringAndMissingFieldsAreLeftAlone(t *testing.T) {
	body := `{"a":1,"source":null,"engine_full":42}` + "\n" + `{"a":2}`
	got, n := RedactJSONLFields(body, []string{"source", "engine_full", "create_table_query"})
	if got != body || n != 0 {
		t.Errorf("got %q (n=%d), want unchanged", got, n)
	}
}

// The config sanitizer's behaviour must not change under the refactor that
// parameterised its sentinel.
func TestRedactCredentialsInText_StillUsesRemoved(t *testing.T) {
	got, n := RedactCredentialsInText(`password = 'abc' and https://u:pw@h/x`)
	if n != 2 || !strings.Contains(got, "password = REMOVED") || !strings.Contains(got, "https://u:REMOVED@h/x") {
		t.Errorf("got %q (n=%d)", got, n)
	}
}
