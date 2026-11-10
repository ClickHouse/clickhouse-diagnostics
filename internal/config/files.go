package config

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
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

// PreprocessedDir is where the server wrote its merged configuration:
// <path>/preprocessed_configs/. serverPath is the running server's path
// (system.server_settings, 23.3+); without it the root-level <path> of the
// server-config sources is used (a later file overrides an earlier one, as
// in the merge) — never a nested one such as a disk's <path> in a storage
// configuration, nor one from users.xml — then ClickHouse's default.
func PreprocessedDir(serverPath string, sources []string) string {
	p := strings.TrimSpace(serverPath)
	if p == "" {
		for _, f := range sources {
			if !isServerConfig(f) {
				continue
			}
			if v := rootPath(f); filepath.IsAbs(v) {
				p = v
			}
		}
	}
	if p == "" {
		p = DefaultDataPath
	}
	return filepath.Join(p, "preprocessed_configs")
}

// isServerConfig is config.xml or a file of a server-config override
// directory (config.d/, conf.d/), not users.xml / users.d/.
func isServerConfig(f string) bool {
	base, dir := filepath.Base(f), filepath.Base(filepath.Dir(f))
	if strings.HasPrefix(strings.ToLower(base), "users.") || dir == "users.d" {
		return false
	}
	return true
}

// rootPath is the text of a <path> element directly under the file's root
// element, "" when there is none.
func rootPath(f string) string {
	raw, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	dec := xml.NewDecoder(bytes.NewReader(StripComments(raw)))
	dec.Strict = false
	depth, inPath, val := 0, false, ""
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return val
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 && t.Name.Local == "path" {
				inPath = true
				text.Reset()
			}
		case xml.CharData:
			if inPath {
				text.Write(t)
			}
		case xml.EndElement:
			if inPath && depth == 2 {
				val, inPath = strings.TrimSpace(text.String()), false
			}
			depth--
		}
	}
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
