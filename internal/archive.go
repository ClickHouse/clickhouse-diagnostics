package internal

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// CreateArchive creates a tar.gz archive of the specified directories
func CreateArchive(archiveName string, dirs ...string) error {
	fmt.Printf("Creating archive %s...\n", archiveName)

	// Check if any of the source directories exist
	existingDirs := filterExistingDirs(dirs)

	if len(existingDirs) == 0 {
		fmt.Println("No valid directories to archive, skipping archive creation")
		return nil
	}

	// Create the archive file
	file, err := os.OpenFile(archiveName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("error creating archive file: %w", err)
	}
	defer file.Close()

	// Create gzip writer
	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	// Create tar writer
	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	// Track the total number of files archived
	totalFiles := 0

	// Add each directory to the archive
	for _, dir := range existingDirs {
		filesAdded, err := addDirectoryToArchive(tarWriter, dir)
		if err != nil {
			fmt.Printf("Error archiving directory '%s': %v\n", dir, err)
			continue
		}
		totalFiles += filesAdded
	}

	fmt.Printf("Archive created successfully with %d files\n", totalFiles)
	return nil
}

// filterExistingDirs filters out non-existent directories
func filterExistingDirs(dirs []string) []string {
	var existingDirs []string
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			fmt.Printf("Directory '%s' does not exist, skipping in archive\n", dir)
		} else {
			existingDirs = append(existingDirs, dir)
		}
	}
	return existingDirs
}

// archiveEntryName builds the slash-separated name a tar entry must carry,
// from a base directory and a host-separated relative path. Kept separate from
// the walk so the Windows case is testable on any platform: the input is
// whatever filepath.Rel produced on the collecting machine.
//
// The separator is replaced unconditionally rather than with filepath.ToSlash,
// which is a no-op off Windows. CI runs on Linux, so a ToSlash guard could
// never fail there and the regression this function exists to stop would ride
// straight through. The cost is that a Unix filename containing a literal
// backslash — legal, but impossible for the names this tool generates
// (system.<table>_<ts>.jsonl and the files it copies out of the server's
// config and log directories) — would be rewritten as a separator.
func archiveEntryName(baseDir, relPath string) string {
	if relPath == "." || relPath == "" {
		return baseDir
	}
	return path.Join(baseDir, strings.ReplaceAll(relPath, `\`, "/"))
}

// addDirectoryToArchive adds a directory and its contents to the tar archive
func addDirectoryToArchive(tarWriter *tar.Writer, dir string) (int, error) {
	fileCount := 0
	baseDir := filepath.Base(dir)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get header info
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		// Create proper path in the archive
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Set the name with directory structure.
		//
		// `path`, never `filepath`: tar names are slash-separated by
		// definition, while filepath.Join and filepath.Rel use the host
		// separator. On Windows this wrote entries like
		// `clickhouse_backup_…\system.parts_….jsonl`, which is not a path in
		// the archive at all — every other platform then extracts ONE file
		// whose name contains a backslash instead of a directory, so the
		// dashboard, the analysis skill and plain `tar` all see an empty
		// bundle. Measured on a customer bundle collected with the Windows
		// build: `tar` and Python both produced the single-file form, and the
		// case lost two round trips to it. Slashes are correct everywhere —
		// Windows accepts them in Win32 paths, and Python's tarfile maps them
		// to os.sep on extraction.
		header.Name = archiveEntryName(baseDir, relPath)

		// Write the header
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		// If it's a regular file, write the content
		if !info.IsDir() {
			if err := addFileContent(tarWriter, path); err != nil {
				return err
			}
			fileCount++
		}

		return nil
	})

	return fileCount, err
}

// addFileContent adds file content to the tar archive
func addFileContent(tarWriter *tar.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(tarWriter, file)
	return err
}
