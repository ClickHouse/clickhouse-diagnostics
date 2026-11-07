package collection

import (
	"encoding/json"
	"strings"
)

// hiddenToken 是 SQL/DDL 文本中发现的凭据的替换值。
// 它正是 ClickHouse 自己在 ≥ 23.x 服务器上写入
// system.tables.create_table_query 和 engine_full 的字面量
// （display_secrets_in_show_and_select = 0），因此旧服务器和新服务器
// 产生的 bundle 读起来一致，所有下游读者——schema graph、skill、grep——
// 只需一种约定，而不是两种。特意不同于 `redacted`（"REMOVED"），
// 后者标记配置文件脱敏。
const hiddenToken = "[HIDDEN]"

// positionalSecrets 将引擎 / 表函数名（小写）映射到
// 判断哪些位置参数字符串是凭据的规则。服务器在 ≥ 23.x 上会自行
// 屏蔽这些内容；此规则用于 22.x 下限（此时 system.tables
// 会原文携带它们），并在所有地方作为纵深防御。
//
// 规则的写法偏向过度脱敏：隐藏一个格式名或用户名对
// 读者没有损失，而泄漏的机密无法收回。
type argRule func(args []string) []int

var positionalSecrets = map[string]argRule{
	// S3 系列：(url, format[, compression]) 或 (url, key_id, secret[, session_token][, format …])。
	"s3": s3Rule, "s3queue": s3Rule, "gcs": s3Rule, "oss": s3Rule, "cosn": s3Rule,
	"deltalake": s3Rule, "iceberg": s3Rule, "icebergs3": s3Rule, "hudi": s3Rule,
	// 集群变体会在开头附加集群名。
	"s3cluster": shifted(s3Rule), "deltalakecluster": shifted(s3Rule), "icebergcluster": shifted(s3Rule),
	"hudicluster": shifted(s3Rule), "icebergs3cluster": shifted(s3Rule),
	// 作为表引擎 / 函数时：(host, db, table, user, password …)；
	// 作为数据库引擎时：(host, db, user, password …)。
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
	// 第二个位置上的 NOSIGN（不带引号）表示匿名访问：没有机密。
	if strings.EqualFold(strings.TrimSpace(args[1]), "NOSIGN") {
		return nil
	}
	// 第二个位置是格式名表示 (url, format, compression …)。
	if looksLikeFormat(args[1]) {
		return nil
	}
	// 成对处理两部分：key id（2）以及 secret（3）。AWS 形态的
	// 启发式能捕获 AKIA…/ASIA… id，但 S3 兼容服务
	// （MinIO、GCS HMAC "GOOG1…"、R2、OSS "LTAI…"）会签发任意形态的 id，
	// 而 id 是凭据的标识符，不是公开值。
	out := []int{2, 3}
	if len(args) >= 4 && !looksLikeFormat(args[3]) && !looksLikeCompression(args[3]) {
		out = append(out, 4) // 会话 token
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

// RedactSQLText 屏蔽 ClickHouse DDL 或引擎文本中的凭据——
// 即 system.tables 的 create_table_query / engine_full / as_select 列
// 和 system.dictionaries 的 source 列。返回文本以及
// 执行的替换次数。
//
// 共两层。首先按位置：每个已知引擎或表函数调用都会被
// 解析（尊重嵌套和引号），携带机密的位置参数
// 被替换为 '[HIDDEN]'。然后是与配置脱敏器共享的字节形态
// 启发式——URL basic-auth 密码、AWS key id、JWT、PEM 块，
// 以及 `keyword = 'value'` 对，例如 kafka_sasl_password = '…'——
// 捕获 SETTINGS 子句或未知引擎携带的内容。
func RedactSQLText(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	out, n := redactCalls(s)
	out, m := redactHeuristicsWith(out, "'"+hiddenToken+"'", hiddenToken)
	return out, n + m
}

// redactCalls 遍历 s 中的 `name(`（name 为已知引擎 / 函数），
// 递归进入参数（从而处理嵌套在 MV 的 SELECT 中的表函数），
// 并屏蔽规则指定的位置。
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
			// 文本被截断：其余部分原样输出。
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

// skipNonCode 报告 s[j] 是否开启一个从不可能包含调用、参数分隔符
// 或外层代码的右括号的词法区域——反引号或双引号标识符、
// `-- line` 注释或 `/* block */` 注释——并返回该区域最后一个
// 字节的索引。下面每个扫描器都以相同方式跳过这些区域。没有这一点，
// 名为 `s3(` 的表或提到 s3( 的注释会被当成引擎调用：
// matchingParen 随后会把那个 '(' 与真实调用的 ')' 配对，
// 而真实密码此时落在规则检查的范围之外，被原样保留。
// 单引号字面量在各扫描器调用方内联处理，因为各处的
// 转义规则不同。未终止的区域会一直延伸到文本末尾。
func skipNonCode(s string, j int) (end int, ok bool) {
	switch {
	case s[j] == '`' || s[j] == '"':
		q := s[j]
		for k := j + 1; k < len(s); k++ {
			if s[k] == '\\' {
				k++
				continue
			}
			if s[k] == q {
				if k+1 < len(s) && s[k+1] == q { // 双写引号转义自身
					k++
					continue
				}
				return k, true
			}
		}
		return len(s) - 1, true
	case s[j] == '-' && j+1 < len(s) && s[j+1] == '-':
		if k := strings.IndexByte(s[j:], '\n'); k >= 0 {
			return j + k, true
		}
		return len(s) - 1, true
	case s[j] == '/' && j+1 < len(s) && s[j+1] == '*':
		if k := strings.Index(s[j+2:], "*/"); k >= 0 {
			return j + 2 + k + 1, true
		}
		return len(s) - 1, true
	}
	return 0, false
}

// nextCall 在 i 及之后查找紧接着 '(' 的标识符，跳过字符串字面量、
// 引用标识符和注释，使被引号包裹或被注释掉的 "s3(" 永远不会
// 被当作调用。返回名称、起始索引和 '(' 的索引；没有时
// start = -1。
func nextCall(s string, i int) (name string, start, open int) {
	inStr := false
	for j := i; j < len(s); j++ {
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
		if end, ok := skipNonCode(s, j); ok {
			j = end
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

// matchingParen 返回与 open 处的 '(' 配对的 ')' 的索引，
// 尊重嵌套括号、单引号字符串（\' 和 ” 转义）、引用
// 标识符和注释。没有时返回 -1。
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
		if end, ok := skipNonCode(s, j); ok {
			j = end
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

// splitArgs 在顶层逗号处切分参数列表，保留每个参数
// 周边的空白，使文本可以逐字重新组装。
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
		if end, ok := skipNonCode(s, j); ok {
			j = end
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

// maskLiteral 将单引号字符串字面量参数替换为
// '[HIDDEN]'，保留其周边空白。非字面量（标识符、
// 数字、命名集合引用）保持原样：它不是机密。
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

// SensitiveCollectorFields 按采集器文件列出 JSONL 列名，
// 这些列的值是 DDL 或引擎文本，因此会在结果写入前
// 经过 RedactSQLText。键为执行器所见查询文件名。
// last_exception 在此列是因为失败的字典，其错误会引用
// 无法建立的连接——"mysqlxx::ConnectionFailed … password = …"——
// 而且同一字段在 dashboard 面板和图中已经过擦除。
var SensitiveCollectorFields = map[string][]string{
	"system.tables.sql":       {"create_table_query", "engine_full", "as_select"},
	"system.dictionaries.sql": {"source", "last_exception"},
}

// RedactJSONLFields 对 body 中每条 JSONEachRow 行的指定字符串字段
// 应用 RedactSQLText。其他每个字节都精确保留——只有值确实改变的行
// 才会重新序列化，因此 64 位整数保持带引号，键顺序保持服务器的顺序，
// 未变化的 bundle 与本功能存在之前写出的 bundle
// 字节完全一致。
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

// replaceJSONStringField 找到一行 JSON 对象中的 `"key":"…"` 成员，
// 把解码后的字符串传给 fn；如果 fn 改变了它，字面量会就地重新编码。
// 返回该行以及是否发生了替换。
//
// 只有 KEY 位置的匹配才计入：这些字节必须紧跟对象的
// '{' 或分隔成员的 ','。在字符串值内部每个引号都被转义，
// 因此合法 JSON 无法在那里重现 needle——但这一守卫
// 没有代价，意味着脱敏不依赖于该论断对每个生产者
// 都成立，而且仅仅以字段名结尾的更长键（`"x_source"`）
// 绝不会被误认为该字段。
func replaceJSONStringField(line, key string, fn func(string) string) (string, bool) {
	needle := `"` + key + `"`
	from := 0
	for from < len(line) {
		rel := strings.Index(line[from:], needle)
		if rel < 0 {
			return line, false
		}
		at := from + rel
		from = at + 1
		if !atJSONKeyPosition(line, at) {
			continue
		}
		// `"key"` 然后是 ':'，然后是字符串值的开引号，冒号
		// 两侧可有空白。ClickHouse 不输出空白，但手工编辑的
		// fixture 可能有，而成员仍是同一成员。
		start := skipJSONBlanks(line, at+len(needle))
		if start >= len(line) || line[start] != ':' {
			continue
		}
		start = skipJSONBlanks(line, start+1)
		if start >= len(line) || line[start] != '"' {
			continue // 不是字符串值：无需脱敏
		}
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
	return line, false
}

// atJSONKeyPosition 报告 line[at] 处的 '"' 是否可能开启对象
// 成员的键：前一个非空白字节是 '{' 或 ','。
func atJSONKeyPosition(line string, at int) bool {
	k := at - 1
	for k >= 0 && (line[k] == ' ' || line[k] == '\t') {
		k--
	}
	return k >= 0 && (line[k] == '{' || line[k] == ',')
}

func skipJSONBlanks(line string, k int) int {
	for k < len(line) && (line[k] == ' ' || line[k] == '\t') {
		k++
	}
	return k
}

// jsonStringEnd 返回从 start 开始的 JSON 字符串字面量的闭引号
// 索引，或 -1。
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
