package collection

import (
	"regexp"
	"strings"
)

// redacted 是用于替换任何被脱敏凭据的哨兵值，在结构化
// 和启发式两轮处理中都使用。使用单一常量，便于用 grep 检索输出。
const redacted = "REMOVED"

// credentialPattern 是一个正则加一个用于诊断的人类可读标签。
// 正则会捕获应当被替换的完整片段——整个匹配（第 0 组）会被换成
// `redacted`，因此多组正则必须明确表达脱敏范围。
type credentialPattern struct {
	name string
	re   *regexp.Regexp
}

// credentialPatterns 描述在 ClickHouse 配置语境中几乎总是凭据的
// 值的字节形态特征，无论它们出现在哪个标签或键下。
// 这些规则在结构化脱敏器之后运行，因此任何因标签名缺失
// 而幸存的内容（注释、拼错的键、新厂商的标签）仍会被擦除。
//
// 校准原则是宁可过度脱敏也不要脱敏不足：脱敏后的配置里多一个
// 被标记的哈希无伤大雅，泄漏一个凭据则不然。
var credentialPatterns = []credentialPattern{
	// PEM 封装的私钥。跨多行——必须使用 (?s)。
	{
		name: "pem_private_key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	},
	// 嵌入 URL 的凭据：只替换密码部分，使生成的 URL
	// 仍可用于诊断主机问题。
	{
		name: "url_basic_auth",
		re:   regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^:@\s/]+:)[^@\s]+(@)`),
	},
	// AWS access key ID 具有固定且独特的形态。
	{
		name: "aws_access_key_id",
		re:   regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	},
	// AWS 临时 access key ID。
	{
		name: "aws_temp_access_key_id",
		re:   regexp.MustCompile(`\bASIA[0-9A-Z]{16}\b`),
	},
	// JWT：三段 base64url。开头的 eyJ 是 '{"' 的 base64 编码——
	// 足够独特，可避免在普通 base64 数据块上误报。
	{
		name: "jwt",
		re:   regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`),
	},
	// Bcrypt 哈希（$2a$ / $2b$ / $2x$ / $2y$ + 22 字符盐 + 31 字符哈希）。
	{
		name: "bcrypt_hash",
		re:   regexp.MustCompile(`\$2[abxy]\$\d{2}\$[./A-Za-z0-9]{53}`),
	},
	// 长十六进制字符串：SHA-1（40）、SHA-256（64）以及更长。
	// ClickHouse 配置通常不会包含这种长度的纯十六进制字符串，
	// 除非在 password_sha*_hex 标签中（已被捕获）或字面量
	// 哈希值中——两者都值得脱敏。
	{
		name: "long_hex",
		re:   regexp.MustCompile(`\b[a-fA-F0-9]{40,}\b`),
	},
	// 长 base64（128+ 字符）。覆盖 GCP 服务账号密钥载荷
	// 和其他大型编码凭据。阈值设得较高，以避免匹配
	// 合法的大型字母数字值。
	{
		name: "long_base64",
		re:   regexp.MustCompile(`\b[A-Za-z0-9+/]{128,}={0,2}`),
	},
	// 自由文本中的键/值凭据披露——注释、文档、
	// TODO 备注等。连接符是必需的（':'、'='，或紧跟关键词的
	// 引号），因此 "password requirements" 或
	// "rotate the secret quarterly" 这类文字不会匹配。只脱敏
	// 值；关键词保留，使注释仍可阅读。
	{
		// 形式 1：keyword: value   或   keyword = value
		// 关键词前没有 \b，因此 `legacy_secret` 和 `db_password`
		// 这类复合名称仍会匹配——复合词中的关键词
		// 部分仍是凭据信号。
		name: "keyword_value_eq",
		re:   regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)s?(\s*[:=]\s*)(["'][^"'\n]+["']|\S+)`),
	},
	{
		// 形式 2：keyword "value"   或   keyword 'value'
		name: "keyword_value_quoted",
		re:   regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)s?(\s+)(["'][^"'\n]+["'])`),
	},
}

// RedactCredentialsInText 扫描 s 中几乎总是凭据的字节模式，
// 并将其替换为脱敏哨兵值。返回修改后的字符串和执行的替换次数。
//
// 模式按声明顺序应用；较早的模式优先匹配（例如 JWT 会在
// long-base64 回退之前被消费）。
func RedactCredentialsInText(s string) (string, int) {
	return redactHeuristicsWith(s, redacted, redacted)
}

// redactHeuristicsWith 是带调用方指定哨兵值的 RedactCredentialsInText。
// quotedToken 替换本身是带引号字面量的值（使 SQL 文本保持语法有效——
// 在引号残骸之间用 '[HIDDEN]' 而不是 [HIDDEN]）；token 替换其他所有内容。
// 配置脱敏器两者都传 "REMOVED"；SQL 脱敏器传 '[HIDDEN]'
// 和 [HIDDEN]，即 ClickHouse 自己写出的字面量。
func redactHeuristicsWith(s, quotedToken, token string) (string, int) {
	count := 0
	for _, p := range credentialPatterns {
		switch p.name {
		case "url_basic_auth":
			// 保留周边 URL，使日志仍有用；
			// 只脱敏 ':' 和 '@' 之间的密码。
			s = p.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := p.re.FindStringSubmatch(m)
				if len(sub) == 3 {
					count++
					return sub[1] + token + sub[2]
				}
				return m
			})
		case "keyword_value_eq", "keyword_value_quoted":
			// 保留关键词和连接符，使文本仍可读；
			// 只脱敏值（第 3 组）。
			var n int
			s, n = replaceKeywordValues(s, p.re, quotedToken, token)
			count += n
		default:
			s = p.re.ReplaceAllStringFunc(s, func(m string) string {
				count++
				return token
			})
		}
	}
	return s, count
}

// sensitiveExactNames 列出始终被视为机密值的标签 / 属性 / YAML 键名，
// 即使它们不包含 sensitiveFragments 中的片段。
// 使用小写规范形式。
var sensitiveExactNames = map[string]struct{}{
	"client_id":           {},
	"tenant_id":           {},
	"account_name":        {},
	"storage_account_url": {},
	"role_arn":            {},
	"role_session_name":   {},
	"session_token":       {},
	"connection_string":   {},
	"account_key":         {},
}

// sensitiveFragments 是“看起来像凭据”的名称的子串准列表——
// 任何名称小写后包含其中某个字符串的标签或键都会被脱敏。
// 这样可以捕获未来命名的标签，如 `<my_db_password>` 或
// `gcp_service_account_credentials`，而无需逐一枚举
// 所有变体。
var sensitiveFragments = []string{
	"password", "passwd",
	"secret",
	"credential",
	"token",
	"private_key", "privatekey",
	"api_key", "apikey",
	"access_key", "accesskey",
	"service_account",
}

// isSensitiveName 在 name 匹配某个敏感精确名称或包含某个
// 敏感片段（不区分大小写）时返回 true。
func isSensitiveName(name string) bool {
	n := strings.ToLower(name)
	if _, ok := sensitiveExactNames[n]; ok {
		return true
	}
	for _, frag := range sensitiveFragments {
		if strings.Contains(n, frag) {
			return true
		}
	}
	return false
}

// replaceKeywordValues 应用一条 keyword = value 规则。正则定位
// 关键词、连接符和值的 START；对于带引号的值，END 通过按 SQL 转义规则
// 扫描字面量找到——\' 和 ” 在 '…' 内，\" 在 "…" 内——而不是用
// 正则的 [^'"]+，后者会在第一个被转义的引号处停止。没有这一点，
// password = 'pa\'ss' 会被脱敏成 '[HIDDEN]'ss'，把密码的尾部
// 留在磁盘上。
func replaceKeywordValues(s string, re *regexp.Regexp, quotedToken, token string) (string, int) {
	var b strings.Builder
	count := 0
	pos := 0
	for pos < len(s) {
		loc := re.FindStringSubmatchIndex(s[pos:])
		if loc == nil || len(loc) < 8 {
			break
		}
		vStart, vEnd := pos+loc[6], pos+loc[7] // 第 3 组：值
		b.WriteString(s[pos:vStart])
		v := s[vStart:vEnd]
		// 已脱敏（二次处理，或 >= 23.x 服务器先屏蔽了它）：
		// 保留原样，不计入次数。
		if v == quotedToken || v == token {
			b.WriteString(v)
			pos = vEnd
			continue
		}
		count++
		if q := s[vStart]; q == '\'' || q == '"' {
			end := quotedLiteralEnd(s, vStart)
			if end < 0 {
				end = vEnd // 未终止的字面量：回退到正则范围
			}
			b.WriteString(quotedToken)
			pos = end
			continue
		}
		b.WriteString(token)
		pos = vEnd
	}
	b.WriteString(s[pos:])
	return b.String(), count
}

// quotedLiteralEnd 返回从 start 开始的字面量闭合引号之后的位置索引，
// 遵循反斜杠转义和双写引号；字面量永不闭合时返回 -1。
func quotedLiteralEnd(s string, start int) int {
	q := s[start]
	for j := start + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return -1
}
