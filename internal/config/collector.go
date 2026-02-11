package config

import (
	"fmt"
	"os"
	"path/filepath"

	"clickhouse-diagnostic/internal/collection"
)

// Collector handles collection of configuration files
type Collector struct {
	sanitizer *collection.Sanitizer
}

// NewCollector creates a new configuration collector
func NewCollector() *Collector {
	return &Collector{
		sanitizer: collection.NewSanitizer(),
	}
}

// Collect collects configuration files from the specified directory
func (c *Collector) Collect(configDir string, keepPasswords bool) error {
	fmt.Printf("Collecting configuration files from '%s'...\n", configDir)

	// Check if the source directory exists
	_, err := os.Stat(configDir)
	if os.IsNotExist(err) {
		fmt.Printf("Config directory '%s' does not exist, skipping collection\n", configDir)
		return nil
	}

	// Create the destination directory
	destDir := "./configuration"
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return fmt.Errorf("error creating configuration directory: %w", err)
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

		// Sanitize content if needed
		var sanitizedContent []byte
		var passwordsFound int

		if c.sanitizer.ShouldSanitize(path, content, keepPasswords) {
			if c.sanitizer.IsYAMLFile(path) {
				sanitizedContent, passwordsFound = c.sanitizer.SanitizeYAMLContent(content)
			} else {
				sanitizedContent, passwordsFound = c.sanitizer.SanitizeXMLContent(content)
			}
			if passwordsFound > 0 {
				fmt.Printf("Found and removed %d credentials in file '%s'\n", passwordsFound, filepath.Base(path))
				passwordCount += passwordsFound
			}
		} else {
			sanitizedContent = content
		}

		// Get the base filename
		fileName := filepath.Base(path)
		destPath := filepath.Join(destDir, fileName)

		// Write the file to the destination
		if err := os.WriteFile(destPath, sanitizedContent, 0600); err != nil {
			fmt.Printf("Error saving config file '%s': %v\n", fileName, err)
			return nil
		}

		fileCount++
		return nil
	})

	if err != nil {
		return fmt.Errorf("error walking through config directory: %w", err)
	}

	fmt.Printf("Collected %d configuration files to '%s'\n", fileCount, destDir)
	if passwordCount > 0 {
		fmt.Printf("Removed %d passwords from configuration files\n", passwordCount)
	}

	return nil
}