package query

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/version"
)

// Finder handles discovery and filtering of query files
type Finder struct {
	parser   *version.Parser
	comparer *version.Comparer
}

// NewFinder creates a new query finder
func NewFinder() *Finder {
	return &Finder{
		parser:   version.NewParser(),
		comparer: version.NewComparer(),
	}
}

// FindCompatibleQueries finds query files that are compatible with server version
func (f *Finder) FindCompatibleQueries(rootDir string, serverVersion internal.Version) ([]internal.QueryFile, error) {
	var allQueries []internal.QueryFile
	var rootQueries []internal.QueryFile
	var versionedQueries []internal.QueryFile

	// Walk through the queries directory and its subdirectories
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Dotfiles are never query or alert files, and nothing downstream
		// filters by extension — manager.go executes whatever this walk
		// returns — so a `._system.parts.sql` is read and sent to the server.
		// Unpacking the release on macOS and copying the folder to the node
		// leaves an AppleDouble `._name` beside every file and directory: one
		// real bundle came back with 47 of 84 collectors "failed" on
		// "security validation failed for '._system.parts.sql'" and all 15
		// alert rules failing to parse with "yaml: control characters are not
		// allowed", which buried the three genuine failures. rootDir itself is
		// exempt: it is legitimately allowed to be "." or "./queries.onprem".
		if path != rootDir && strings.HasPrefix(filepath.Base(path), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip directories themselves
		if info.IsDir() {
			return nil
		}

		// Get relative path from root queries directory
		relPath, err := filepath.Rel(rootDir, path)
		if err != nil {
			return err
		}

		// Get filename and parent directory
		filename := filepath.Base(path)
		dirPath := filepath.Dir(relPath)

		// Root level query
		if dirPath == "." {
			rootQueries = append(rootQueries, internal.QueryFile{
				Path:     relPath,
				Name:     filename,
				DirName:  "",
				FullPath: path,
			})
			return nil
		}

		// Versioned query (in subdirectory)
		dirName := filepath.Base(dirPath)

		// Try to parse the directory name as a version
		dirVersion, err := f.parser.ParseVersionFromDirName(dirName)
		if err != nil {
			fmt.Printf("Skipping directory with invalid version format: %s\n", dirName)
			return nil
		}

		// Check if the server version is compatible with the directory version
		if f.comparer.IsGreaterOrEqual(serverVersion, dirVersion) {
			versionedQueries = append(versionedQueries, internal.QueryFile{
				Path:     relPath,
				Name:     filename,
				DirName:  dirName,
				FullPath: path,
			})
		} else {
			fmt.Printf("Skipping query '%s' from directory '%s' (server version %d.%d.%d.%d is not high enough)\n",
				filename, dirName, serverVersion.Major, serverVersion.Minor, serverVersion.Patch, serverVersion.Build)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error walking query directory: %w", err)
	}

	// Combine root queries and filtered versioned queries
	allQueries = append(allQueries, rootQueries...)
	allQueries = append(allQueries, versionedQueries...)

	return allQueries, nil
}

// ValidateQueryDirectory checks if the query directory exists and contains files
func (f *Finder) ValidateQueryDirectory(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("queries directory '%s' does not exist", dir)
	}

	// Check if directory contains any files
	empty, err := f.isDirectoryEmpty(dir)
	if err != nil {
		return fmt.Errorf("error checking directory contents: %w", err)
	}

	if empty {
		return fmt.Errorf("queries directory '%s' is empty", dir)
	}

	return nil
}

// isDirectoryEmpty checks if a directory is empty
func (f *Finder) isDirectoryEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
