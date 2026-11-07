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

// CreateArchive 创建指定目录的 tar.gz 归档。
//
// 归档先写入 archiveName 旁边的隐藏临时文件，只有在每次写入和每次
// 关闭——tar、gzip、file——都成功之后才重命名到位。过去它直接写入
// archiveName，丢弃 Close 错误，并跳过中途遍历失败的目录，因此
// 磁盘写满或文件不可读会留下一个截断或半写入的
// clickhouse_backup_*.tar.gz，看起来像 bundle，而且是在
// “Archive created successfully” 之后。任何错误发生时，临时文件会被
// 删除，archiveName 处已有的归档保持原样。
func CreateArchive(archiveName string, dirs ...string) (err error) {
	fmt.Printf("正在创建归档 %s...\n", archiveName)

	// 检查是否有任何源目录存在
	existingDirs := filterExistingDirs(dirs)

	if len(existingDirs) == 0 {
		fmt.Println("没有可归档的有效目录，跳过归档创建")
		return nil
	}

	// 同一目录，因此重命名是原子的；带点前缀和 .partial，
	// 使任何寻找 clickhouse_backup_*.tar.gz 的东西都不会选中它。
	tmp, err := os.CreateTemp(filepath.Dir(archiveName), "."+filepath.Base(archiveName)+".*.partial")
	if err != nil {
		return fmt.Errorf("创建归档文件失败：%w", err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("创建归档文件失败：%w", err)
	}

	totalFiles, err := writeArchive(tmp, existingDirs)
	if err != nil {
		return fmt.Errorf("写入归档失败：%w", err)
	}
	// 在 Close 之前 Sync：在磁盘写满或网络文件系统上，
	// 错误可能只在这里才暴露。
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("写入归档失败：%w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("写入归档失败：%w", err)
	}
	if err = os.Rename(tmp.Name(), archiveName); err != nil {
		return fmt.Errorf("发布归档失败：%w", err)
	}

	fmt.Printf("归档创建成功，共 %d 个文件\n", totalFiles)
	return nil
}

// writeArchive 将 dirs 作为 tar.gz 流写入 w，并返回写入的文件数。
// 所有错误都会返回，包括关闭 tar 和 gzip 写入器产生的错误——
// gzip 在 Close 时写入其尾部，丢弃该错误正是截断归档
// 过去被当作完整归档的原因。目录失败会中止归档：其条目可能
// 只写了一半，跳过它会发布损坏的流。
func writeArchive(w io.Writer, dirs []string) (int, error) {
	gzipWriter := gzip.NewWriter(w)
	tarWriter := tar.NewWriter(gzipWriter)

	totalFiles := 0
	for _, dir := range dirs {
		filesAdded, err := addDirectoryToArchive(tarWriter, dir)
		if err != nil {
			return totalFiles, fmt.Errorf("归档目录 %q 失败：%w", dir, err)
		}
		totalFiles += filesAdded
	}
	if err := tarWriter.Close(); err != nil {
		return totalFiles, err
	}
	if err := gzipWriter.Close(); err != nil {
		return totalFiles, err
	}
	return totalFiles, nil
}

// filterExistingDirs 过滤掉不存在的目录
func filterExistingDirs(dirs []string) []string {
	var existingDirs []string
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			fmt.Printf("目录 '%s' 不存在，归档时跳过\n", dir)
		} else {
			existingDirs = append(existingDirs, dir)
		}
	}
	return existingDirs
}

// archiveEntryName 根据基础目录和采集机器上 filepath.Rel 产生的
// 相对路径，构建 tar 条目必须携带的斜杠分隔名称。
func archiveEntryName(baseDir, relPath string) string {
	return entryName(baseDir, relPath, filepath.Separator)
}

// entryName 只转换 sep，即采集平台的分隔符——这正是
// filepath.ToSlash 的行为，只是把平台作为参数传入，以便 Windows 情形
// 能在 ToSlash 为无操作的 Linux CI 上测试。在 Unix 上反斜杠
// 是普通文件名字符：configuration/ 和 logs/ 以真实名称复制服务器的
// 文件，因此 `a\b.xml` 和 `a/b.xml` 是两个文件，重写反斜杠
// 会把它们合并成一个条目。
func entryName(baseDir, relPath string, sep rune) string {
	if relPath == "." || relPath == "" {
		return baseDir
	}
	if sep != '/' {
		relPath = strings.ReplaceAll(relPath, string(sep), "/")
	}
	return path.Join(baseDir, relPath)
}

// addDirectoryToArchive 将目录及其内容加入 tar 归档
func addDirectoryToArchive(tarWriter *tar.Writer, dir string) (int, error) {
	fileCount := 0
	baseDir := filepath.Base(dir)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 获取头部信息
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		// 在归档中创建正确的路径
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// 设置带目录结构的名称。
		//
		// 用 `path`，绝不用 `filepath`：按定义 tar 名称以斜杠分隔，
		// 而 filepath.Join 和 filepath.Rel 使用主机分隔符。在 Windows 上
		// 这曾写出 `clickhouse_backup_…\system.parts_….jsonl` 这样的条目，
		// 它在归档中根本不是路径——其他平台随后会解出
		// 一个名称包含反斜杠的单个文件，而不是目录，于是
		// dashboard、分析 skill 和普通 `tar` 都看到一个空
		// bundle。在由 Windows 构建采集的客户 bundle 上实测：
		// `tar` 和 Python 都产生了单文件形式，该
		// 案例为此损失了两个来回。斜杠在所有地方都正确——
		// Windows 在 Win32 路径中接受它们，Python 的 tarfile 会在
		// 解压时将其映射为 os.sep。
		header.Name = archiveEntryName(baseDir, relPath)

		// 写入头部
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		// 如果是普通文件，写入内容
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

// addFileContent 将文件内容加入 tar 归档
func addFileContent(tarWriter *tar.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(tarWriter, file)
	return err
}
