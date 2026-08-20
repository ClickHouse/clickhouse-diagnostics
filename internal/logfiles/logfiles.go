// Package logfiles copies ClickHouse's on-disk server logs into the
// diagnostic bundle.
//
// The useful part is not the copying but the DISCOVERY: rather than
// assuming /var/log/clickhouse-server, it reads the server configuration
// and follows the <log>/<errorlog> paths actually in effect. Operators
// relocate logs far more often than they change anything else, and a bundle
// that silently contains no logs because they live on a data volume is the
// failure this avoids.
//
// Not collected in gov mode: log lines contain raw queries, table names and
// file paths as free text. That cannot be hashed without destroying the
// thing that makes a log useful, which is the same reasoning that withholds
// the query-analysis bundle.
package logfiles

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultLogDir is the packaged location, used when configuration yields
// nothing (or can't be read for permission reasons).
const DefaultLogDir = "/var/log/clickhouse-server"

// reLogPath matches <log> and <errorlog> elements in the server config.
// Deliberately a targeted regex rather than a full XML parse: ClickHouse
// configs use includes, substitutions and mixed XML/YAML, and we only need
// two well-known leaf elements — a partial parse must not fail the run.
var reLogPath = regexp.MustCompile(`(?is)<(log|errorlog)>\s*([^<\s][^<]*?)\s*</(?:log|errorlog)>`)

// Options controls collection. Zero values are safe defaults.
type Options struct {
	// Dir, when set, is used verbatim and discovery is skipped.
	Dir string
	// ConfigDir is searched for <log>/<errorlog> paths when Dir is empty.
	ConfigDir string
	// IncludeArchives also copies rotated *.gz logs (off by default: they
	// are frequently larger than everything else in the bundle combined).
	IncludeArchives bool
	// MaxBytesPerFile caps each copied file. Larger files are TAIL-copied —
	// the recent end is what explains a current incident.
	MaxBytesPerFile int64
}

// Result reports what happened, so the caller can print an honest summary.
type Result struct {
	Dirs       []string
	Copied     []string
	Truncated  []string
	TotalBytes int64
	Notes      []string
}

const defaultMaxBytes = 50 << 20 // 50 MiB per file

// Collect copies logs into <destDir>/logs/ and returns what it did.
// Never returns an error for "nothing found" — a missing log directory is a
// normal condition when running remotely, and is reported as a note.
func Collect(destDir string, opts Options) (*Result, error) {
	if opts.MaxBytesPerFile <= 0 {
		opts.MaxBytesPerFile = defaultMaxBytes
	}
	res := &Result{}

	dirs := DiscoverDirs(opts, res)
	if len(dirs) == 0 {
		res.Notes = append(res.Notes, "no readable ClickHouse log directory found — "+
			"run on the ClickHouse host, or pass -logs-dir")
		return res, nil
	}
	res.Dirs = dirs

	outDir := filepath.Join(destDir, "logs")
	if err := os.MkdirAll(outDir, 0750); err != nil {
		return res, fmt.Errorf("create logs output dir: %w", err)
	}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !wanted(e.Name(), opts.IncludeArchives) {
				continue
			}
			src := filepath.Join(dir, e.Name())
			// Flatten into one directory but keep provenance when the same
			// filename appears in two log dirs.
			dst := filepath.Join(outDir, uniqueName(outDir, e.Name()))
			n, truncated, err := copyTail(src, dst, opts.MaxBytesPerFile)
			if err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("%s: %v", src, err))
				continue
			}
			res.Copied = append(res.Copied, filepath.Base(dst))
			res.TotalBytes += n
			if truncated {
				res.Truncated = append(res.Truncated, filepath.Base(dst))
			}
		}
	}
	sort.Strings(res.Copied)
	return res, nil
}

// DiscoverDirs resolves which directories to read, in precedence order:
// explicit flag, then paths declared in the configuration, then the
// packaged default. Results are de-duplicated and existence-checked.
func DiscoverDirs(opts Options, res *Result) []string {
	if opts.Dir != "" {
		if isDir(opts.Dir) {
			return []string{filepath.Clean(opts.Dir)}
		}
		if res != nil {
			res.Notes = append(res.Notes,
				fmt.Sprintf("-logs-dir %q is not a readable directory", opts.Dir))
		}
		return nil
	}

	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		d = filepath.Clean(d)
		if d == "" || seen[d] || !isDir(d) {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}

	for _, p := range LogPathsFromConfig(opts.ConfigDir) {
		add(filepath.Dir(p))
	}
	add(DefaultLogDir)
	return dirs
}

// LogPathsFromConfig scans the configuration directory (and the adjacent
// main config.xml) for <log>/<errorlog> values.
func LogPathsFromConfig(configDir string) []string {
	if configDir == "" {
		return nil
	}
	var candidates []string
	// config.d/ typically sits beside config.xml; check both.
	candidates = append(candidates, configDir)
	if parent := filepath.Dir(filepath.Clean(configDir)); parent != "." {
		candidates = append(candidates, parent)
	}

	seen := map[string]bool{}
	var paths []string
	for _, root := range candidates {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.ToLower(e.Name())
			if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".conf") {
				continue
			}
			blob, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			for _, m := range reLogPath.FindAllStringSubmatch(string(blob), -1) {
				p := strings.TrimSpace(m[2])
				// Skip unresolved substitutions like <log from_env="..."/>
				// and relative paths we can't anchor reliably.
				if p == "" || strings.Contains(p, "{") || !filepath.IsAbs(p) || seen[p] {
					continue
				}
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

func wanted(name string, includeArchives bool) bool {
	l := strings.ToLower(name)
	if strings.HasSuffix(l, ".log") {
		return true
	}
	return includeArchives && (strings.HasSuffix(l, ".gz") || strings.HasSuffix(l, ".zst"))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// uniqueName avoids clobbering when two log directories hold the same
// filename (e.g. two shards' clickhouse-server.log).
func uniqueName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); os.IsNotExist(err) {
			return cand
		}
	}
	return name
}

// copyTail copies src to dst, keeping only the last max bytes of oversized
// files. It reports how many bytes were written and whether it truncated.
//
// Truncation keeps the END of the file because incidents are diagnosed from
// recent lines; a header records that data was dropped so nobody reads the
// first surviving line as the start of the log.
func copyTail(src, dst string, max int64) (written int64, truncated bool, err error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, false, err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return 0, false, err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 0, false, err
	}
	defer out.Close()

	if fi.Size() > max {
		truncated = true
		if _, err := in.Seek(fi.Size()-max, io.SeekStart); err != nil {
			return 0, true, err
		}
		header := fmt.Sprintf(
			"### support-diagnostic: TRUNCATED — original %d bytes, kept last %d bytes (tail) ###\n",
			fi.Size(), max)
		if _, err := out.WriteString(header); err != nil {
			return 0, true, err
		}
		// Drop the partial first line so the file starts on a record
		// boundary rather than mid-message.
		if err := discardPartialLine(in); err != nil {
			return 0, true, err
		}
	}

	n, err := io.Copy(out, in)
	return n, truncated, err
}

func discardPartialLine(f *os.File) error {
	buf := make([]byte, 1)
	for i := 0; i < 64<<10; i++ { // bounded: don't scan forever on a binary file
		n, err := f.Read(buf)
		if err != nil || n == 0 {
			return nil // EOF is fine — nothing left to align
		}
		if buf[0] == '\n' {
			return nil
		}
	}
	return nil
}

// Summary renders a short operator-facing report.
func (r *Result) Summary() string {
	var b strings.Builder
	if len(r.Copied) == 0 {
		b.WriteString("  no log files collected\n")
	} else {
		fmt.Fprintf(&b, "  collected %d log file(s), %.1f MiB, from: %s\n",
			len(r.Copied), float64(r.TotalBytes)/(1<<20), strings.Join(r.Dirs, ", "))
		if len(r.Truncated) > 0 {
			fmt.Fprintf(&b, "  tail-truncated (size cap): %s\n", strings.Join(r.Truncated, ", "))
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "  note: %s\n", n)
	}
	return b.String()
}
