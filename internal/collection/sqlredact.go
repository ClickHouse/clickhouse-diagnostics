package collection

import (
	"encoding/json"
	"strings"
)

// hiddenToken is the replacement for a credential found in SQL/DDL text.
// It is the literal ClickHouse itself writes into system.tables.create_table_query
// and engine_full on servers ≥ 23.x (display_secrets_in_show_and_select = 0),
// so a bundle from an old server and one from a new server read the same, and
// every downstream reader — the schema graph, the skill, a grep — needs one
// convention, not two. Deliberately different from `redacted` ("REMOVED"),
// which marks config-file redaction.
const hiddenToken = "[HIDDEN]"

// positionalSecrets maps an engine / table-function name (lower-cased) to the
// rule that decides which positional string arguments are credentials. The
// server masks these itself on ≥ 23.x; this exists for the 22.x floor, where
// system.tables carries them verbatim, and as defence in depth everywhere.
//
// Rules are written to over-redact: hiding a format name or a user name costs a
// reader nothing, a leaked secret cannot be taken back.
type argRule func(args []string) []int

var positionalSecrets = map[string]argRule{
	// S3-family: (url, format[, compression]) or (url, key_id, secret[, session_token][, format …]).
	"s3": s3Rule, "s3queue": s3Rule, "gcs": s3Rule, "oss": s3Rule, "cosn": s3Rule,
	"deltalake": s3Rule, "iceberg": s3Rule, "icebergs3": s3Rule, "hudi": s3Rule,
	// Cluster variants prepend the cluster name.
	"s3cluster": shifted(s3Rule), "deltalakecluster": shifted(s3Rule), "icebergcluster": shifted(s3Rule),
	"hudicluster": shifted(s3Rule), "icebergs3cluster": shifted(s3Rule),
	// (host, db, table, user, password …) as a table engine / function;
	// (host, db, user, password …) as a database engine.
	"mysql": hostDbUserPw, "postgresql": hostDbUserPw, "mongodb": hostDbUserPw,
	"materializedmysql": hostDbUserPw, "materializedpostgresql": hostDbUserPw,
	"remote": hostDbUserPw, "remotesecure": hostDbUserPw,
	// (engine, host, db, table, user, password)
	"externaldistributed": fixed(6),
	// (host:port, db_index, password …)
	"redis": fixed(3),
	// (connection_string | url, container, blob, account_name, account_key …)
	"azureblobstorage": azureRule, "azurequeue": azureRule, "azure": azureRule,
}

func fixed(idx int) argRule {
	return func(args []string) []int {
		if len(args) >= idx {
			return []int{idx}
		}
		return nil
	}
}

func shifted(r argRule) argRule {
	return func(args []string) []int {
		if len(args) < 2 {
			return nil
		}
		out := r(args[1:])
		for i := range out {
			out[i]++
		}
		return out
	}
}

func hostDbUserPw(args []string) []int {
	switch {
	case len(args) >= 5:
		return []int{5}
	case len(args) == 4:
		return []int{4}
	}
	return nil
}

func s3Rule(args []string) []int {
	if len(args) < 3 {
		return nil
	}
	// NOSIGN (unquoted) in second position means anonymous access: no secrets.
	if strings.EqualFold(strings.TrimSpace(args[1]), "NOSIGN") {
		return nil
	}
	// A format name in second position means (url, format, compression …).
	if looksLikeFormat(args[1]) {
		return nil
	}
	out := []int{3}
	if len(args) >= 4 && !looksLikeFormat(args[3]) && !looksLikeCompression(args[3]) {
		out = append(out, 4) // session token
	}
	return out
}

func azureRule(args []string) []int {
	var out []int
	if len(args) >= 1 && strings.Contains(strings.ToLower(args[0]), "accountkey=") {
		out = append(out, 1)
	}
	if len(args) >= 5 {
		out = append(out, 5)
	}
	return out
}

func looksLikeFormat(arg string) bool {
	v := strings.Trim(strings.TrimSpace(arg), "'")
	if v == "" || strings.ContainsAny(v, " /:.@") {
		return false
	}
	switch strings.ToLower(v) {
	case "csv", "tsv", "json", "parquet", "orc", "avro", "arrow", "native", "tabseparated",
		"jsoneachrow", "jsoncompacteachrow", "lineasstring", "rawblob", "values", "protobuf",
		"capnproto", "msgpack", "auto", "regexp", "template", "customseparated", "arrowstream":
		return true
	}
	l := strings.ToLower(v)
	return strings.HasSuffix(l, "withnames") || strings.HasSuffix(l, "withnamesandtypes") ||
		strings.Contains(l, "eachrow") || strings.HasPrefix(l, "json") || strings.HasPrefix(l, "csv") ||
		strings.HasPrefix(l, "tsv") || strings.HasPrefix(l, "tabseparated")
}

func looksLikeCompression(arg string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(arg), "'")) {
	case "gzip", "gz", "zstd", "lz4", "bz2", "xz", "snappy", "deflate", "brotli", "br", "none", "auto":
		return true
	}
	return false
}

// RedactSQLText masks credentials in ClickHouse DDL or engine text — the
// create_table_query / engine_full / as_select columns of system.tables and the
// source column of system.dictionaries. Returns the text and how many
// replacements were made.
//
// Two layers. First, positional: every known engine or table function call is
// parsed (nesting and quoting respected) and the argument positions that carry
// secrets are replaced with '[HIDDEN]'. Then the byte-shape heuristics shared
// with the config sanitizer — URL basic-auth passwords, AWS key ids, JWTs, PEM
// blocks, and `keyword = 'value'` pairs such as kafka_sasl_password = '…' —
// catch what a SETTINGS clause or an unknown engine carries.
func RedactSQLText(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	out, n := redactCalls(s)
	out, m := redactHeuristicsWith(out, "'"+hiddenToken+"'", hiddenToken)
	return out, n + m
}

// redactCalls walks s for `name(` where name is a known engine / function,
// recurses into the arguments (so a table function nested inside an MV's
// SELECT is handled), and masks the positions the rule names.
func redactCalls(s string) (string, int) {
	count := 0
	var b strings.Builder
	i := 0
	for i < len(s) {
		name, start, open := nextCall(s, i)
		if start < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i:start])
		close := matchingParen(s, open)
		if close < 0 {
			// Truncated text: emit the rest unchanged.
			b.WriteString(s[start:])
			break
		}
		args := splitArgs(s[open+1 : close])
		for k := range args {
			r, n := redactCalls(args[k])
			args[k] = r
			count += n
		}
		if rule, ok := positionalSecrets[strings.ToLower(name)]; ok {
			for _, idx := range rule(args) {
				if idx >= 1 && idx <= len(args) {
					if masked, ok := maskLiteral(args[idx-1]); ok {
						args[idx-1] = masked
						count++
					}
				}
			}
		}
		b.WriteString(s[start : open+1])
		b.WriteString(strings.Join(args, ","))
		b.WriteByte(')')
		i = close + 1
	}
	return b.String(), count
}

// nextCall finds the next identifier immediately followed by '(' at or after
// i, skipping over string literals so a quoted "s3(" is never a call. Returns
// the name, its start index and the index of the '('; start = -1 when none.
func nextCall(s string, i int) (name string, start, open int) {
	inStr := false
	for j := i; j < len(s); j++ {
		c := s[j]
		if inStr {
			if c == '\\' {
				j++
			} else if c == '\'' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '\'':
			inStr = true
		case isIdentStart(c) && (j == 0 || !isIdentByte(s[j-1])):
			k := j
			for k < len(s) && isIdentByte(s[k]) {
				k++
			}
			p := k
			for p < len(s) && (s[p] == ' ' || s[p] == '\t') {
				p++
			}
			if p < len(s) && s[p] == '(' {
				return s[j:k], j, p
			}
			j = k - 1
		}
	}
	return "", -1, -1
}

func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIdentByte(c byte) bool  { return isIdentStart(c) || (c >= '0' && c <= '9') }

// matchingParen returns the index of the ')' closing the '(' at open, honouring
// nested parentheses and single-quoted strings (\' and ” escapes). -1 if none.
func matchingParen(s string, open int) int {
	depth := 0
	inStr := false
	for j := open; j < len(s); j++ {
		c := s[j]
		if inStr {
			if c == '\\' {
				j++
			} else if c == '\'' {
				if j+1 < len(s) && s[j+1] == '\'' {
					j++
				} else {
					inStr = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// splitArgs splits an argument list on top-level commas, preserving each
// argument's surrounding whitespace so the text can be reassembled verbatim.
func splitArgs(s string) []string {
	var args []string
	depth := 0
	inStr := false
	last := 0
	for j := 0; j < len(s); j++ {
		c := s[j]
		if inStr {
			if c == '\\' {
				j++
			} else if c == '\'' {
				if j+1 < len(s) && s[j+1] == '\'' {
					j++
				} else {
					inStr = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, s[last:j])
				last = j + 1
			}
		}
	}
	if strings.TrimSpace(s) != "" || len(args) > 0 {
		args = append(args, s[last:])
	}
	return args
}

// maskLiteral replaces a single-quoted string literal argument with
// '[HIDDEN]', keeping the whitespace around it. A non-literal (identifier,
// number, named collection reference) is left alone: it is not a secret.
func maskLiteral(arg string) (string, bool) {
	t := strings.TrimSpace(arg)
	if len(t) < 2 || t[0] != '\'' || t[len(t)-1] != '\'' {
		return arg, false
	}
	if t == "'"+hiddenToken+"'" || t == "''" {
		return arg, false
	}
	lead := arg[:strings.Index(arg, t)]
	trail := arg[len(lead)+len(t):]
	return lead + "'" + hiddenToken + "'" + trail, true
}

// SensitiveCollectorFields names, per collector file, the JSONL columns whose
// values are DDL or engine text and therefore pass through RedactSQLText before
// the result is written. Keyed by the query file name as the executor sees it.
var SensitiveCollectorFields = map[string][]string{
	"system.tables.sql":       {"create_table_query", "engine_full", "as_select"},
	"system.dictionaries.sql": {"source"},
}

// RedactJSONLFields applies RedactSQLText to the named string fields of every
// JSONEachRow line in body. Every other byte is preserved exactly — a line is
// re-serialised only where a value actually changed, so 64-bit integers stay
// quoted, key order stays the server's, and an unchanged bundle is
// byte-identical to one written before this existed.
func RedactJSONLFields(body string, fields []string) (string, int) {
	if len(fields) == 0 || body == "" {
		return body, 0
	}
	total := 0
	lines := strings.Split(body, "\n")
	for li, line := range lines {
		for _, f := range fields {
			line2, n := replaceJSONStringField(line, f, func(v string) string {
				out, c := RedactSQLText(v)
				total += c
				return out
			})
			_ = n
			line = line2
		}
		lines[li] = line
	}
	return strings.Join(lines, "\n"), total
}

// replaceJSONStringField finds `"key":"…"` in one JSON object line and passes
// the decoded string to fn; if fn changes it, the literal is re-encoded in
// place. Returns the line and whether a substitution happened.
func replaceJSONStringField(line, key string, fn func(string) string) (string, bool) {
	needle := `"` + key + `":"`
	at := strings.Index(line, needle)
	if at < 0 {
		return line, false
	}
	start := at + len(needle) - 1 // index of the opening quote of the value
	end := jsonStringEnd(line, start)
	if end < 0 {
		return line, false
	}
	var v string
	if err := json.Unmarshal([]byte(line[start:end+1]), &v); err != nil {
		return line, false
	}
	nv := fn(v)
	if nv == v {
		return line, false
	}
	enc, err := json.Marshal(nv)
	if err != nil {
		return line, false
	}
	return line[:start] + string(enc) + line[end+1:], true
}

// jsonStringEnd returns the index of the closing quote of the JSON string
// literal that opens at start, or -1.
func jsonStringEnd(s string, start int) int {
	for j := start + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return -1
}
