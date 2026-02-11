package collection

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
	"regexp"
	"strings"
)

// Sanitizer handles removal of sensitive data from configuration files
type Sanitizer struct {
	xmlPatterns  []sanitizationPattern
	yamlPatterns []sanitizationPattern
}

type sanitizationPattern struct {
	regex       *regexp.Regexp
	replacement string
}

// NewSanitizer creates a new configuration sanitizer
func NewSanitizer() *Sanitizer {
	xmlPatterns := []sanitizationPattern{
		// User authentication
		{regexp.MustCompile(`<password>(.*?)</password>`), "<password>REMOVED</password>"},
		{regexp.MustCompile(`<password_sha256_hex>(.*?)</password_sha256_hex>`), "<password_sha256_hex>REMOVED</password_sha256_hex>"},
		{regexp.MustCompile(`<password_double_sha1_hex>(.*?)</password_double_sha1_hex>`), "<password_double_sha1_hex>REMOVED</password_double_sha1_hex>"},
		{regexp.MustCompile(`<password_sha1_hex>(.*?)</password_sha1_hex>`), "<password_sha1_hex>REMOVED</password_sha1_hex>"},
		{regexp.MustCompile(`<password_bcrypt>(.*?)</password_bcrypt>`), "<password_bcrypt>REMOVED</password_bcrypt>"},
		{regexp.MustCompile(`<secret>(.*?)</secret>`), "<secret>REMOVED</secret>"},
		{regexp.MustCompile(`<token>(.*?)</token>`), "<token>REMOVED</token>"},

		// S3 / Object storage
		{regexp.MustCompile(`<access_key_id>(.*?)</access_key_id>`), "<access_key_id>REMOVED</access_key_id>"},
		{regexp.MustCompile(`<secret_access_key>(.*?)</secret_access_key>`), "<secret_access_key>REMOVED</secret_access_key>"},
		{regexp.MustCompile(`<session_token>(.*?)</session_token>`), "<session_token>REMOVED</session_token>"},
		{regexp.MustCompile(`<role_arn>(.*?)</role_arn>`), "<role_arn>REMOVED</role_arn>"},
		{regexp.MustCompile(`<role_session_name>(.*?)</role_session_name>`), "<role_session_name>REMOVED</role_session_name>"},
		{regexp.MustCompile(`<service_account>(.*?)</service_account>`), "<service_account>REMOVED</service_account>"},

		// Azure Blob Storage
		{regexp.MustCompile(`<connection_string>(.*?)</connection_string>`), "<connection_string>REMOVED</connection_string>"},
		{regexp.MustCompile(`<account_name>(.*?)</account_name>`), "<account_name>REMOVED</account_name>"},
		{regexp.MustCompile(`<account_key>(.*?)</account_key>`), "<account_key>REMOVED</account_key>"},
		{regexp.MustCompile(`<storage_account_url>(.*?)</storage_account_url>`), "<storage_account_url>REMOVED</storage_account_url>"},
		{regexp.MustCompile(`<client_id>(.*?)</client_id>`), "<client_id>REMOVED</client_id>"},
		{regexp.MustCompile(`<tenant_id>(.*?)</tenant_id>`), "<tenant_id>REMOVED</tenant_id>"},

		// Attribute patterns
		{regexp.MustCompile(`password="([^"]+)"`), `password="REMOVED"`},
		{regexp.MustCompile(`password='([^']+)'`), `password='REMOVED'`},
		{regexp.MustCompile(`secret="([^"]+)"`), `secret="REMOVED"`},
		{regexp.MustCompile(`secret='([^']+)'`), `secret='REMOVED'`},
		{regexp.MustCompile(`token="([^"]+)"`), `token="REMOVED"`},
		{regexp.MustCompile(`token='([^']+)'`), `token='REMOVED'`},

		// URL patterns with credentials
		{regexp.MustCompile(`([a-zA-Z]+://[^:]+:)([^@]+)(@[^"'\s<>]+)`), `${1}REMOVED${3}`},
	}

	yamlPatterns := []sanitizationPattern{
		// User authentication
		{regexp.MustCompile(`(?m)(password:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(password_sha256_hex:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(password_double_sha1_hex:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(password_sha1_hex:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(password_bcrypt:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(secret:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(token:\s*)(.+)$`), "${1}REMOVED"},

		// S3 / Object storage
		{regexp.MustCompile(`(?m)(access_key_id:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(secret_access_key:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(session_token:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(role_arn:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(role_session_name:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(service_account:\s*)(.+)$`), "${1}REMOVED"},

		// Azure Blob Storage
		{regexp.MustCompile(`(?m)(connection_string:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(account_name:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(account_key:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(storage_account_url:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(client_id:\s*)(.+)$`), "${1}REMOVED"},
		{regexp.MustCompile(`(?m)(tenant_id:\s*)(.+)$`), "${1}REMOVED"},
	}

	return &Sanitizer{
		xmlPatterns:  xmlPatterns,
		yamlPatterns: yamlPatterns,
	}
}

// SanitizeXMLContent removes passwords and sensitive data from XML content
func (s *Sanitizer) SanitizeXMLContent(content []byte) ([]byte, int) {
	return s.applyPatterns(content, s.xmlPatterns)
}

// SanitizeYAMLContent removes passwords and sensitive data from YAML content
func (s *Sanitizer) SanitizeYAMLContent(content []byte) ([]byte, int) {
	return s.applyPatterns(content, s.yamlPatterns)
}

func (s *Sanitizer) applyPatterns(content []byte, patterns []sanitizationPattern) ([]byte, int) {
	contentStr := string(content)
	count := 0

	for _, pattern := range patterns {
		matches := pattern.regex.FindAllStringSubmatch(contentStr, -1)
		count += len(matches)
		contentStr = pattern.regex.ReplaceAllString(contentStr, pattern.replacement)
	}

	return []byte(contentStr), count
}

// IsXMLFile checks if a file appears to be XML based on extension or content
func (s *Sanitizer) IsXMLFile(path string, content []byte) bool {
	// Check extension first
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".xml" || ext == ".xsd" || ext == ".svg" || ext == ".config" {
		return true
	}

	// Check content for XML declaration
	if bytes.HasPrefix(content, []byte("<?xml")) {
		return true
	}

	// Check if content parses as XML
	var xmlTest interface{}
	if xml.Unmarshal(content, &xmlTest) == nil {
		return true
	}

	return false
}

// IsYAMLFile checks if a file appears to be YAML based on extension
func (s *Sanitizer) IsYAMLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

// ShouldSanitize determines if a file should be sanitized based on its content and path
func (s *Sanitizer) ShouldSanitize(path string, content []byte, keepPasswords bool) bool {
	if keepPasswords {
		return false
	}
	return s.IsXMLFile(path, content) || s.IsYAMLFile(path)
}
