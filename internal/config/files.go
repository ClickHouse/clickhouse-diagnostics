package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultConfigDir is the server's configuration directory: config.xml and
// users.xml, and the config.d/ and users.d/ overrides merged on top of them.
const DefaultConfigDir = "/etc/clickhouse-server/"

// DefaultDataPath is ClickHouse's default <path>; the merged configuration
// is written to <path>/preprocessed_configs/.
const DefaultDataPath = "/var/lib/clickhouse/"

// IsConfigFile reports whether a file name is ClickHouse configuration.
// Anything else in the configuration directory — the server's TLS key and
// certificate, dhparam.pem, editor backups — is never collected.
func IsConfigFile(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range []string{".xml", ".yaml", ".yml", ".conf"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// SourceFiles are the configuration files of configDir in ClickHouse's merge
// order: the files at its top level (config.xml, users.xml), then each *.d
// directory's files alphabetically. A configDir that is itself a *.d
// directory (the old default, config.d/) brings the main files beside it.
func SourceFiles(configDir string) []string {
	if configDir == "" {
		return nil
	}
	dir := filepath.Clean(configDir)
	if strings.HasSuffix(filepath.Base(dir), ".d") {
		return append(filesIn(filepath.Dir(dir)), filesIn(dir)...)
	}
	out := filesIn(dir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".d") {
			out = append(out, filesIn(filepath.Join(dir, e.Name()))...)
		}
	}
	return out
}

func filesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && IsConfigFile(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

var rePath = regexp.MustCompile(`(?s)<path>\s*([^<]*?)\s*</path>`)

// PreprocessedDir is where the server wrote its merged configuration:
// <path>/preprocessed_configs/. serverPath is the running server's path
// (system.server_settings, 23.3+); without it the top-level <path> of the
// source files is used, then ClickHouse's default.
func PreprocessedDir(serverPath string, sources []string) string {
	p := strings.TrimSpace(serverPath)
	if p == "" {
		for _, f := range sources {
			blob, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			if m := rePath.FindStringSubmatch(string(StripComments(blob))); m != nil && filepath.IsAbs(m[1]) {
				p = m[1] // a later file overrides an earlier one, as in the merge
			}
		}
	}
	if p == "" {
		p = DefaultDataPath
	}
	return filepath.Join(p, "preprocessed_configs")
}

// DiscoveryFiles are the files to read the server's own settings from —
// Keeper members, log paths. The merged config.xml when it is readable: it
// is what the server runs, with includes and substitutions resolved and
// every override applied. The source files otherwise.
func DiscoveryFiles(configDir, preprocessedDir string) []string {
	if preprocessedDir != "" {
		merged := filepath.Join(preprocessedDir, "config.xml")
		if f, err := os.Open(merged); err == nil {
			f.Close()
			return []string{merged}
		}
	}
	return SourceFiles(configDir)
}
