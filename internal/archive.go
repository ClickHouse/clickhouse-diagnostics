package internal

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

		// Set the name with directory structure
		if relPath == "." {
			header.Name = baseDir
		} else {
			header.Name = filepath.Join(baseDir, relPath)
		}

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
