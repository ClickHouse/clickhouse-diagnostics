package internal

// QueryFile 表示一个查询文件及其元数据
type QueryFile struct {
	Path     string // 相对于 queries 目录的路径
	Name     string // 文件名
	DirName  string // 父目录名（根查询为空）
	FullPath string // 文件的绝对路径
}

// Version 表示带有语义化版本号的 ClickHouse 版本
type Version struct {
	Major int
	Minor int
	Patch int
	Build int
}

// Config 表示配置采集设置
type Config struct {
	SourceDir     string
	DestDir       string
	KeepPasswords bool
}

// ArchiveOptions 表示创建归档的选项
type ArchiveOptions struct {
	Name        string
	Directories []string
	Compress    bool
}
