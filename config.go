package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sanitizeXMLContent removes passwords from XML content
func sanitizeXMLContent(content []byte) ([]byte, int) {
	// Convert to string for easier handling
	contentStr := string(content)

	// Counter for found passwords
	passwordCount := 0

	// List of XML tags and attributes that might contain passwords
	passwordPatterns := []struct {
		regex       *regexp.Regexp
		replacement string
	}{
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

	// Apply each pattern
	for _, pattern := range passwordPatterns {
		// Count matches before replacement
		matches := pattern.regex.FindAllStringSubmatch(contentStr, -1)
		passwordCount += len(matches)

		// Replace the passwords
		contentStr = pattern.regex.ReplaceAllString(contentStr, pattern.replacement)
	}

	return []byte(contentStr), passwordCount
}

// isXMLFile checks if a file appears to be XML based on extension or content
func isXMLFile(path string, content []byte) bool {
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

// collectConfigFiles collects configuration files from the specified directory
func collectConfigFiles(configDir string, keepPasswords bool) {
	fmt.Printf("Collecting configuration files from '%s'...\n", configDir)

	// Check if the source directory exists
	_, err := os.Stat(configDir)
	if os.IsNotExist(err) {
		fmt.Printf("Config directory '%s' does not exist, skipping collection\n", configDir)
		return
	}

	// Create the destination directory
	destDir := "./configuration"
	if err := os.MkdirAll(destDir, 0755); err != nil {
		fmt.Printf("Error creating configuration directory: %v\n", err)
		return
	}

	// Walk through the config directory
	fileCount := 0
	passwordCount := 0
	err = filepath.Walk(configDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Read the file content
		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("Error reading config file '%s': %v\n", path, err)
			return nil
		}

		// Sanitize XML content to remove passwords if needed
		var sanitizedContent []byte
		var passwordsFound int

		if !keepPasswords && isXMLFile(path, content) {
			sanitizedContent, passwordsFound = sanitizeXMLContent(content)
			if passwordsFound > 0 {
				fmt.Printf("Found and removed %d passwords in file '%s'\n", passwordsFound, filepath.Base(path))
				passwordCount += passwordsFound
			}
		} else {
			sanitizedContent = content
		}

		// Get the base filename
		fileName := filepath.Base(path)
		destPath := filepath.Join(destDir, fileName)

		// Write the file to the destination
		if err := os.WriteFile(destPath, sanitizedContent, 0644); err != nil {
			fmt.Printf("Error saving config file '%s': %v\n", fileName, err)
			return nil
		}

		fileCount++
		return nil
	})

	if err != nil {
		fmt.Printf("Error walking through config directory: %v\n", err)
		return
	}

	fmt.Printf("Collected %d configuration files to '%s'\n", fileCount, destDir)
	if passwordCount > 0 {
		fmt.Printf("Removed %d passwords from configuration files\n", passwordCount)
	}
}
