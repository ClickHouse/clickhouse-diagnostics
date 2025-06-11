package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"clickhouse-diagnostic/internal"
)

// Parser handles version parsing operations
type Parser struct{}

// NewParser creates a new version parser
func NewParser() *Parser {
	return &Parser{}
}

// ParseClickHouseVersion parses ClickHouse version from version string (e.g., "21.8.10.3")
func (p *Parser) ParseClickHouseVersion(versionStr string) (internal.Version, error) {
	// Clean the version string (remove any whitespace, quotes, etc.)
	versionStr = strings.TrimSpace(versionStr)

	// Define a regex to extract version components
	re := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)\.(\d+)`)
	matches := re.FindStringSubmatch(versionStr)

	if len(matches) != 5 {
		return internal.Version{}, fmt.Errorf("invalid version format: %s", versionStr)
	}

	// Parse each component
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

// ParseVersionFromDirName parses version from directory name
func (p *Parser) ParseVersionFromDirName(dirName string) (internal.Version, error) {
	parts := strings.Split(dirName, ".")
	if len(parts) != 4 {
		return internal.Version{}, fmt.Errorf("invalid directory version format: %s", dirName)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return internal.Version{}, fmt.Errorf("invalid major version: %s", parts[0])
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return internal.Version{}, fmt.Errorf("invalid minor version: %s", parts[1])
	}

	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return internal.Version{}, fmt.Errorf("invalid patch version: %s", parts[2])
	}

	build, err := strconv.Atoi(parts[3])
	if err != nil {
		return internal.Version{}, fmt.Errorf("invalid build version: %s", parts[3])
	}

	return internal.Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Build: build,
	}, nil
}
