package collection

import (
	"regexp"
	"strings"
)

// Sentinel used to replace any redacted credential, in both structural
// and heuristic passes. Single constant so output is grep-able.
const redacted = "REMOVED"

// credentialPattern is a regex + a human label for diagnostics.
// The regex captures the entire run that should be replaced — the whole
// match (group 0) gets swapped for `redacted` so multi-group patterns
// must be written to express the redaction range explicitly.
type credentialPattern struct {
	name string
	re   *regexp.Regexp
}

// credentialPatterns describes byte-shape signatures of values that are
// almost always credentials in a ClickHouse config context, regardless
// of which tag or key they appear under. These run AFTER the structural
// redactors, so anything that survived a missing tag name (a comment,
// a misspelled key, a new vendor's tag) is still scrubbed.
//
// Calibrated to over-redact rather than under-redact: a stray flagged
// hash in a sanitised config is harmless, a leaked credential is not.
var credentialPatterns = []credentialPattern{
	// PEM-encapsulated private keys. Span multiple lines — must use (?s).
	{
		name: "pem_private_key",
		re:   regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	},
	// Credentials embedded in URLs: replace just the password portion so
	// the resulting URL is still readable for diagnosing host issues.
	{
		name: "url_basic_auth",
		re:   regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://[^:@\s/]+:)[^@\s]+(@)`),
	},
	// AWS access key IDs have a fixed, distinctive shape.
	{
		name: "aws_access_key_id",
		re:   regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	},
	// AWS temporary access key IDs.
	{
		name: "aws_temp_access_key_id",
		re:   regexp.MustCompile(`\bASIA[0-9A-Z]{16}\b`),
	},
	// JWT: three base64url segments. The leading eyJ is the b64 of '{"' —
	// distinctive enough to avoid false positives on generic base64 blobs.
	{
		name: "jwt",
		re:   regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`),
	},
	// Bcrypt hashes ($2a$ / $2b$ / $2x$ / $2y$ + 22-char salt + 31-char hash).
	{
		name: "bcrypt_hash",
		re:   regexp.MustCompile(`\$2[abxy]\$\d{2}\$[./A-Za-z0-9]{53}`),
	},
	// Long hex strings: SHA-1 (40), SHA-256 (64), and longer. ClickHouse
	// configs do not normally contain pure-hex strings of this length
	// except inside password_sha*_hex tags (already caught) or in literal
	// hash values — both worth redacting.
	{
		name: "long_hex",
		re:   regexp.MustCompile(`\b[a-fA-F0-9]{40,}\b`),
	},
	// Long base64 (128+ chars). Captures GCP service-account key payloads
	// and other large encoded credentials. Set high to avoid matching
	// legitimate large alphanumeric values.
	{
		name: "long_base64",
		re:   regexp.MustCompile(`\b[A-Za-z0-9+/]{128,}={0,2}`),
	},
	// Key/value credential disclosures in free text — comments, docs,
	// TODO notes, etc. Connector is mandatory (':', '=', or a quote
	// directly after the keyword) so prose like "password requirements"
	// or "rotate the secret quarterly" does not match. Only the value
	// is redacted; the keyword stays so the comment still reads.
	{
		// Form 1: keyword: value   or   keyword = value
		// No \b before the keyword so compound names like
		// `legacy_secret` and `db_password` still match — the keyword
		// part of the compound is still a credential signal.
		name: "keyword_value_eq",
		re:   regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)s?(\s*[:=]\s*)(["'][^"'\n]+["']|\S+)`),
	},
	{
		// Form 2: keyword "value"   or   keyword 'value'
		name: "keyword_value_quoted",
		re:   regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)s?(\s+)(["'][^"'\n]+["'])`),
	},
}

// RedactCredentialsInText scans s for byte patterns that are almost
// always credentials and replaces them with the redaction sentinel.
// Returns the modified string and the count of replacements applied.
//
// Patterns are applied in declaration order; earlier patterns get first
// pick (e.g. a JWT is consumed before the long-base64 fallback would).
func RedactCredentialsInText(s string) (string, int) {
	count := 0
	for _, p := range credentialPatterns {
		switch p.name {
		case "url_basic_auth":
			// Preserve the surrounding URL so logs remain useful;
			// only the password between ':' and '@' is redacted.
			s = p.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := p.re.FindStringSubmatch(m)
				if len(sub) == 3 {
					count++
					return sub[1] + redacted + sub[2]
				}
				return m
			})
		case "keyword_value_eq", "keyword_value_quoted":
			// Keep the keyword + connector so the comment stays
			// readable; redact only the value run (group 3).
			s = p.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := p.re.FindStringSubmatch(m)
				if len(sub) == 4 {
					count++
					return sub[1] + sub[2] + redacted
				}
				return m
			})
		default:
			s = p.re.ReplaceAllStringFunc(s, func(m string) string {
				count++
				return redacted
			})
		}
	}
	return s, count
}

// sensitiveExactNames lists tag / attribute / YAML-key names that are
// always treated as secret values, even if they don't contain a
// fragment from sensitiveFragments. Lowercase canonical form.
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

// sensitiveFragments is the substring allowlist for names that
// "look credential-ish" — any tag or key whose lowercased name contains
// one of these strings is redacted. This catches future-named tags like
// `<my_db_password>` or `gcp_service_account_credentials` without
// having to enumerate every variant.
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

// isSensitiveName returns true if name matches one of the sensitive
// exact names or contains one of the sensitive fragments
// (case-insensitive).
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
