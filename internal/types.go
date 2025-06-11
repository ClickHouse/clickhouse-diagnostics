package internal

// QueryFile represents a query file with its metadata
type QueryFile struct {
	Path     string // Relative path from queries directory
	Name     string // Filename
	DirName  string // Parent directory name (empty for root queries)
	FullPath string // Absolute path to the file
}

// Version represents a ClickHouse version with semantic versioning
type Version struct {
	Major int
	Minor int
	Patch int
	Build int
}

// Config represents configuration collection settings
type Config struct {
	SourceDir     string
	DestDir       string
	KeepPasswords bool
}

// ArchiveOptions represents options for archive creation
type ArchiveOptions struct {
	Name        string
	Directories []string
	Compress    bool
}
