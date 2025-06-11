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
	patterns []sanitizationPattern
}

type sanitizationPattern struct {
	regex       *regexp.Regexp
	replacement string
}

// NewSanitizer creates a new configuration sanitizer
func NewSanitizer() *Sanitizer {
	patterns := []sanitizationPattern{
		// Common password patterns in XML
		{regexp.MustCompile(`<password>([^<]+)</password>`), "<password>REMOVED</password>"},
		{regexp.MustCompile(`<password_sha256_hex>([^<]+)</password_sha256_hex>`), "<password_sha256_hex>REMOVED</password_sha256_hex>"},
		{regexp.MustCompile(`<password_double_sha1_hex>([^<]+)</password_double_sha1_hex>`), "<password_double_sha1_hex>REMOVED</password_double_sha1_hex>"},
		{regexp.MustCompile(`<password_sha1_hex>([^<]+)</password_sha1_hex>`), "<password_sha1_hex>REMOVED</password_sha1_hex>"},
		{regexp.MustCompile(`<secret>([^<]+)</secret>`), "<secret>REMOVED</secret>"},
		{regexp.MustCompile(`<token>([^<]+)</token>`), "<token>REMOVED</token>"},

		// Attribute patterns
		{regexp.MustCompile(`password="([^"]+)"`), `password="REMOVED"`},
		{regexp.MustCompile(`password='([^']+)'`), `password='REMOVED'`},
		{regexp.MustCompile(`secret="([^"]+)"`), `secret="REMOVED"`},
		{regexp.MustCompile(`secret='([^']+)'`), `secret='REMOVED'`},
		{regexp.MustCompile(`token="([^"]+)"`), `token="REMOVED"`},
		{regexp.MustCompile(`token='([^']+)'`), `token='REMOVED'`},

		// URL patterns with credentials
		{regexp.MustCompile(`([a-zA-Z]+://[^:]+:)([^@]+)(@[^"'\s<>]+)`), `$1REMOVED$3`},
	}

	return &Sanitizer{
		patterns: patterns,
	}
}

// SanitizeContent removes passwords and sensitive data from content
func (s *Sanitizer) SanitizeContent(content []byte) ([]byte, int) {
	// Convert to string for easier handling
	contentStr := string(content)

	// Counter for found passwords
	passwordCount := 0

	// Apply each pattern
	for _, pattern := range s.patterns {
		// Count matches before replacement
		matches := pattern.regex.FindAllStringSubmatch(contentStr, -1)
		passwordCount += len(matches)

		// Replace the passwords
		contentStr = pattern.regex.ReplaceAllString(contentStr, pattern.replacement)
	}

	return []byte(contentStr), passwordCount
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

// ShouldSanitize determines if a file should be sanitized based on its content and path
func (s *Sanitizer) ShouldSanitize(path string, content []byte, keepPasswords bool) bool {
	if keepPasswords {
		return false
	}
	return s.IsXMLFile(path, content)
}
