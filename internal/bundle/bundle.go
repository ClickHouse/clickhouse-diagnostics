// Package bundle reads what the collectors saved in a run directory, so the
// alert checks and the dashboard work from the collected data instead of
// querying the server a second time.
//
// A collector's file is <collector>_<YYYYMMDD_HHMMSS>.jsonl. Files are
// matched by the exact collector name plus the run timestamp: a prefix match
// would mix system.replicas with system.replicas_problem_tables.
package bundle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status says why a collector has no file: skipped (essential collection,
// time budget, no Keeper session) or failed, with the reason.
type Status struct {
	State  string // "skipped" or "failed"
	Reason string
}

// NotCollected is returned for a collector without a result file. A check
// that gets it reports its rule as not applicable, never as clean.
type NotCollected struct {
	Collector string
	State     string // "skipped", "failed" or "not collected"
	Reason    string
}

func (e *NotCollected) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("%s %s", e.Collector, e.State)
	}
	return fmt.Sprintf("%s %s: %s", e.Collector, e.State, e.Reason)
}

// Bundle is one run directory.
type Bundle struct {
	dir    string
	status map[string]Status
	mu     sync.Mutex
	cache  map[string][]Row
	end    time.Time
}

// Open indexes dir. status, from the execution log, explains collectors
// that have no file; it may be nil.
func Open(dir string, status map[string]Status) *Bundle {
	return &Bundle{dir: dir, status: status, cache: map[string][]Row{}}
}

// WithWindowEnd records an explicit end of the collected window (-to): the
// log files stop there, so "the last N hours" is judged up to it.
func (b *Bundle) WithWindowEnd(t time.Time) *Bundle {
	b.end = t
	return b
}

// WindowEnd is the explicit window end, zero when the run collected up to
// the moment it ran.
func (b *Bundle) WindowEnd() time.Time { return b.end }

// Dir is the run directory.
func (b *Bundle) Dir() string { return b.dir }

var reRunFile = regexp.MustCompile(`^(.+)_(\d{8}_\d{6})\.jsonl$`)

// path returns the collector's file, or "" when there is none.
func (b *Bundle) path(collector string) string {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if m := reRunFile.FindStringSubmatch(e.Name()); m != nil && m[1] == collector {
			return filepath.Join(b.dir, e.Name())
		}
	}
	return ""
}

// Has reports whether the collector produced a file.
func (b *Bundle) Has(collector string) bool { return b.path(collector) != "" }

// Rows returns the collector's rows, reading the file once.
func (b *Bundle) Rows(collector string) ([]Row, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rows, ok := b.cache[collector]; ok {
		return rows, nil
	}
	// A failed collector's file, when there is one, may be a partial write
	// (disk full): the failure is what counts.
	if st, ok := b.status[collector]; ok && st.State == "failed" {
		return nil, &NotCollected{Collector: collector, State: st.State, Reason: st.Reason}
	}
	p := b.path(collector)
	if p == "" {
		if st, ok := b.status[collector]; ok {
			return nil, &NotCollected{Collector: collector, State: st.State, Reason: st.Reason}
		}
		return nil, &NotCollected{Collector: collector, State: "not collected"}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []Row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r map[string]any
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&r); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		rows = append(rows, Row(r))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	b.cache[collector] = rows
	return rows, nil
}

// Row is one collected row. 64-bit integers arrive as quoted strings
// (output_format_json_quote_64bit_integers=1); the accessors read both.
type Row map[string]any

// Str is the column as text ("" when absent).
func (r Row) Str(k string) string {
	switch v := r[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// Int is the column as int64 (0 when absent or not a number).
func (r Row) Int(k string) int64 {
	s := r.Str(k)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	if b, ok := r[k].(bool); ok && b {
		return 1
	}
	return 0
}

// Uint is the column as uint64, exact for full-width UInt64 values.
func (r Row) Uint(k string) uint64 {
	if n, err := strconv.ParseUint(r.Str(k), 10, 64); err == nil {
		return n
	}
	if n := r.Int(k); n > 0 {
		return uint64(n)
	}
	return 0
}

// Float is the column as float64.
func (r Row) Float(k string) float64 {
	f, _ := strconv.ParseFloat(r.Str(k), 64)
	return f
}

// Map is a Map(String, number) column (an errors map, for example).
func (r Row) Map(k string) map[string]int64 {
	out := map[string]int64{}
	if m, ok := r[k].(map[string]any); ok {
		for key, v := range m {
			out[key] = Row{"v": v}.Int("v")
		}
	}
	return out
}
