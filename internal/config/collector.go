package config

import (
	"fmt"
	"os"
	"path/filepath"

	"clickhouse-diagnostic/internal/collection"
)

// Collector 处理配置文件的采集
type Collector struct {
	sanitizer *collection.Sanitizer
}

// NewCollector 创建一个新的配置采集器
func NewCollector() *Collector {
	return &Collector{
		sanitizer: collection.NewSanitizer(),
	}
}

// Collect 将配置文件从 configDir 采集到 destDir。
//
// destDir 由调用方提供，而不是在这里固定，这样配置会像工具产出的
// 其他产物一样，落到本次运行自己的结果目录内。进程级目录会在多次运行之间
// 残留，而且由于文件按名称写入，第二次针对不同主机的运行
// 会把第一次运行留下的内容归档——挂在第二次运行的时间戳下，
// 且没有任何提示。
func (c *Collector) Collect(configDir, destDir string, keepPasswords bool) error {
	fmt.Printf("正在从 '%s' 采集配置文件...\n", configDir)

	// 检查源目录是否存在
	_, err := os.Stat(configDir)
	if os.IsNotExist(err) {
		fmt.Printf("配置目录 '%s' 不存在，跳过采集\n", configDir)
		return nil
	}

	// 创建目标目录
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return fmt.Errorf("创建配置目录失败：%w", err)
	}

	// 遍历配置目录
	fileCount := 0
	passwordCount := 0

	err = filepath.Walk(configDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 跳过目录
		if info.IsDir() {
			return nil
		}

		// 读取文件内容
		content, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("读取配置文件 '%s' 失败：%v\n", path, err)
			return nil
		}

		// 需要时脱敏内容。失败即关闭：如果脱敏器
		// 无法解析文件，我们就完全跳过该文件——把无法解析的
		// 配置发给支持人员，比可能泄漏某个在仅靠正则的
		// 回退中幸存下来的凭据更安全。
		var sanitizedContent []byte
		var passwordsFound int

		if c.sanitizer.ShouldSanitize(path, content, keepPasswords) {
			var err error
			if c.sanitizer.IsYAMLFile(path) {
				sanitizedContent, passwordsFound, err = c.sanitizer.SanitizeYAMLContent(content)
			} else {
				sanitizedContent, passwordsFound, err = c.sanitizer.SanitizeXMLContent(content)
			}
			if err != nil {
				fmt.Printf("警告：无法脱敏 '%s'（%v）——该文件已从配置归档中跳过\n",
					filepath.Base(path), err)
				return nil
			}
			if passwordsFound > 0 {
				fmt.Printf("在文件 '%s' 中发现并移除了 %d 个凭据\n", filepath.Base(path), passwordsFound)
				passwordCount += passwordsFound
			}
		} else {
			sanitizedContent = content
		}

		// 在 configDir 下镜像目录树，而不是压平到
		// 基本文件名。config.d/storage.xml 和 users.d/storage.xml 都是
		// 常见名称；压平会静默地用一个覆盖另一个，
		// 并丢失它们各自的来源目录——而正是目录
		// 决定 ClickHouse 的配置合并顺序。
		relPath, err := filepath.Rel(configDir, path)
		if err != nil {
			fmt.Printf("解析 '%s' 的路径失败：%v\n", path, err)
			return nil
		}
		destPath := filepath.Join(destDir, relPath)
		if err := os.MkdirAll(filepath.Dir(destPath), 0750); err != nil {
			fmt.Printf("为 '%s' 创建目录失败：%v\n", relPath, err)
			return nil
		}

		// 将文件写入目标位置
		if err := os.WriteFile(destPath, sanitizedContent, 0600); err != nil {
			fmt.Printf("保存配置文件 '%s' 失败：%v\n", relPath, err)
			return nil
		}

		fileCount++
		return nil
	})

	if err != nil {
		return fmt.Errorf("遍历配置目录失败：%w", err)
	}

	fmt.Printf("已采集 %d 个配置文件到 '%s'\n", fileCount, destDir)
	if passwordCount > 0 {
		fmt.Printf("已从配置文件中移除 %d 个密码\n", passwordCount)
	}

	return nil
}
