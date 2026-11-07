package query

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/version"
)

// Finder 负责查询文件的发现与过滤
type Finder struct {
	parser   *version.Parser
	comparer *version.Comparer
}

// NewFinder 创建一个新的查询查找器
func NewFinder() *Finder {
	return &Finder{
		parser:   version.NewParser(),
		comparer: version.NewComparer(),
	}
}

// FindCompatibleQueries 查找与服务器版本兼容的查询文件
func (f *Finder) FindCompatibleQueries(rootDir string, serverVersion internal.Version) ([]internal.QueryFile, error) {
	var allQueries []internal.QueryFile
	var rootQueries []internal.QueryFile
	var versionedQueries []internal.QueryFile

	// 遍历 queries 目录及其子目录
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 点文件从来不是查询文件或告警文件，下游也没有任何地方按扩展名
		// 过滤 —— manager.go 会执行这次遍历返回的一切 —— 因此一个
		// `._system.parts.sql` 会被读取并发送到服务器。在 macOS 上解压发行包
		// 再把文件夹拷贝到节点，会在每个文件和目录旁留下 AppleDouble 的 `._name`：
		// 一个真实的 bundle 回来时，84 个采集器中有 47 个报
		// "security validation failed for '._system.parts.sql'"，全部 15 条
		// 告警规则都因 "yaml: control characters are not
		// allowed" 解析失败，把三个真正的失败淹没了。rootDir 本身
		// 例外：它合法地允许是 "." 或 "./queries.onprem"。
		if path != rootDir && strings.HasPrefix(filepath.Base(path), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// 跳过目录本身
		if info.IsDir() {
			return nil
		}

		// 获取相对于根查询目录的相对路径
		relPath, err := filepath.Rel(rootDir, path)
		if err != nil {
			return err
		}

		// 获取文件名和父目录
		filename := filepath.Base(path)
		dirPath := filepath.Dir(relPath)

		// 根目录层级的查询
		if dirPath == "." {
			rootQueries = append(rootQueries, internal.QueryFile{
				Path:     relPath,
				Name:     filename,
				DirName:  "",
				FullPath: path,
			})
			return nil
		}

		// 带版本的查询（位于子目录）
		dirName := filepath.Base(dirPath)

		// 尝试把目录名解析为版本
		dirVersion, err := f.parser.ParseVersionFromDirName(dirName)
		if err != nil {
			fmt.Printf("跳过版本格式无效的目录：%s\n", dirName)
			return nil
		}

		// 检查服务器版本是否与目录版本兼容
		if f.comparer.IsGreaterOrEqual(serverVersion, dirVersion) {
			versionedQueries = append(versionedQueries, internal.QueryFile{
				Path:     relPath,
				Name:     filename,
				DirName:  dirName,
				FullPath: path,
			})
		} else {
			fmt.Printf("跳过查询 '%s'（来自目录 '%s'）（服务器版本 %d.%d.%d.%d 不够高）\n",
				filename, dirName, serverVersion.Major, serverVersion.Minor, serverVersion.Patch, serverVersion.Build)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("遍历查询目录失败：%w", err)
	}

	// 合并根目录查询和过滤后的版本化查询
	allQueries = append(allQueries, rootQueries...)
	allQueries = append(allQueries, versionedQueries...)

	return allQueries, nil
}

// ValidateQueryDirectory 检查查询目录是否存在且包含文件
func (f *Finder) ValidateQueryDirectory(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("查询目录 '%s' 不存在", dir)
	}

	// 检查目录中是否包含任何文件
	empty, err := f.isDirectoryEmpty(dir)
	if err != nil {
		return fmt.Errorf("检查目录内容失败：%w", err)
	}

	if empty {
		return fmt.Errorf("查询目录 '%s' 为空", dir)
	}

	return nil
}

// isDirectoryEmpty 检查目录是否为空
func (f *Finder) isDirectoryEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
