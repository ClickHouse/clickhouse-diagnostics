package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"clickhouse-diagnostic/internal"
)

// Parser 处理版本解析操作
type Parser struct{}

// NewParser 创建一个新的版本解析器
func NewParser() *Parser {
	return &Parser{}
}

// ParseClickHouseVersion 从版本字符串（例如 "21.8.10.3"）中解析 ClickHouse 版本
func (p *Parser) ParseClickHouseVersion(versionStr string) (internal.Version, error) {
	// 清理版本字符串（移除空白、引号等）
	versionStr = strings.TrimSpace(versionStr)

	// 定义用于提取版本各组件的正则表达式
	re := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)\.(\d+)`)
	matches := re.FindStringSubmatch(versionStr)

	if len(matches) != 5 {
		return internal.Version{}, fmt.Errorf("版本格式无效：%s", versionStr)
	}

	// 逐个解析各组件
	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])
	build, _ := strconv.Atoi(matches[4])

	return internal.Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Build: build,
	}, nil
}

// ParseVersionFromDirName 从目录名解析版本
func (p *Parser) ParseVersionFromDirName(dirName string) (internal.Version, error) {
	parts := strings.Split(dirName, ".")
	if len(parts) != 4 {
		return internal.Version{}, fmt.Errorf("目录版本格式无效：%s", dirName)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return internal.Version{}, fmt.Errorf("主版本号无效：%s", parts[0])
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return internal.Version{}, fmt.Errorf("次版本号无效：%s", parts[1])
	}

	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return internal.Version{}, fmt.Errorf("补丁版本号无效：%s", parts[2])
	}

	build, err := strconv.Atoi(parts[3])
	if err != nil {
		return internal.Version{}, fmt.Errorf("构建版本号无效：%s", parts[3])
	}

	return internal.Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Build: build,
	}, nil
}
