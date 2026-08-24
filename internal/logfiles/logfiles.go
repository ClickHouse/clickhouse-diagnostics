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
	"bytes"
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
			// Regular files only. copyTail would follow a symlink, and the
			// log directory is typically writable by the clickhouse service
			// account while this tool often runs as root — a planted
			// `evil.log -> /etc/shadow` must not end up in a bundle that
			// gets shared with support. Refusing (with a note) rather than
			// resolving keeps the decision visible in the output.
			if !e.Type().IsRegular() {
				res.Notes = append(res.Notes,
					fmt.Sprintf("%s: skipped (not a regular file)", filepath.Join(dir, e.Name())))
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
	candidates = append(candidates, configDir)
	// config.d/ sits beside config.xml, so the parent is worth scanning —
	// but ONLY for a *.d directory. For "-config-dir /etc/clickhouse-server"
	// the parent is /etc, and any unrelated /etc/*.xml with a <log> element
	// would volunteer a directory whose files then land in the bundle.
	if base := filepath.Base(filepath.Clean(configDir)); strings.HasSuffix(base, ".d") {
		if parent := filepath.Dir(filepath.Clean(configDir)); parent != "." {
			candidates = append(candidates, parent)
		}
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
	// O_NOFOLLOW (unix) closes the race left by the directory-scan check:
	// the same threat model that motivated refusing symlinks — a log dir
	// writable by the service account, the tool running as root — permits
	// swapping a symlink in between the scan and this open.
	in, err := openNoFollow(src)
	if err != nil {
		return 0, false, err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return 0, false, err
	}
	// fstat on the open fd — unraceable. Catches a FIFO, device or
	// directory swapped in; a symlink is already refused by openNoFollow.
	// A HARDLINK to a sensitive file is a regular file and passes both
	// checks — Linux fs.protected_hardlinks=1 (the default) prevents an
	// unprivileged user creating one, but that is a default, not a
	// guarantee this code can provide.
	if !fi.Mode().IsRegular() {
		return 0, false, fmt.Errorf("%s: not a regular file", src)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		// A half-written file would be archived while appearing in neither
		// Copied nor TotalBytes; remove it so failure is visible as absence
		// plus a note, not as a silently short log.
		if err != nil {
			os.Remove(dst)
		}
	}()

	// Copy a SNAPSHOT: limit to the size we measured, so an actively
	// written log cannot push the copy past the cap (or past what the
	// truncation header claims).
	limit := fi.Size()
	if fi.Size() > max {
		truncated = true
		start := fi.Size() - max
		if _, err := in.Seek(start, io.SeekStart); err != nil {
			return 0, true, err
		}
		// Drop the partial first line BEFORE writing the header, so the
		// header states the exact number of payload bytes kept rather
		// than the nominal cap (the alignment shaves up to one line off).
		skip, err := lengthToNewline(in)
		if err != nil {
			return 0, true, err
		}
		// lengthToNewline read ahead; reposition to the line boundary.
		if _, err := in.Seek(start+skip, io.SeekStart); err != nil {
			return 0, true, err
		}
		limit = fi.Size() - (start + skip)
		header := fmt.Sprintf(
			"### support-diagnostic: TRUNCATED — original %d bytes, kept last %d bytes (tail) ###\n",
			fi.Size(), limit)
		hn, err := out.WriteString(header)
		if err != nil {
			return 0, true, err
		}
		written += int64(hn)
	}

	// written includes the truncation header: it reports bytes produced in
	// the bundle (what Result.TotalBytes sums), not bytes read from src.
	n, err := io.Copy(out, io.LimitReader(in, limit))
	return written + n, truncated, err
}

// lengthToNewline returns how many bytes lie between the current position
// and the end of the current line (newline included), scanning at most
// 64 KiB in buffered chunks. If no newline appears within the bound (a
// binary or single-line file), the whole bound is skipped. The caller must
// re-Seek afterwards: reads here advance the fd past the answer.
func lengthToNewline(f *os.File) (int64, error) {
	const bound = 64 << 10
	buf := make([]byte, 8<<10)
	var consumed int64
	for consumed < bound {
		n, err := f.Read(buf)
		if n > 0 {
			if i := bytes.IndexByte(buf[:n], '\n'); i >= 0 {
				return consumed + int64(i) + 1, nil
			}
			consumed += int64(n)
		}
		if err != nil {
			return consumed, nil // EOF: nothing left to align
		}
	}
	return bound, nil
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
