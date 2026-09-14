// Package runlog records what a diagnostic run did — every collector query
// with its outcome and duration, every alert rule, and the wall time of each
// phase — and writes it into the bundle as execution_log.txt.
//
// The file answers two questions the result files cannot: "which collectors
// did not run, and why?" (a missing file is otherwise indistinguishable from
// an empty table) and "which collectors are expensive on this server?" so
// the windows and shapes in queries.<mode>/ can be tuned against real data.
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileName is the artefact written into the run directory.
const FileName = "execution_log.txt"

// errorCap bounds the error text kept per entry: enough to carry the
// ClickHouse code and the first sentence, not a stack trace.
const errorCap = 300

// Entry is one unit of work: a collector query, an alert rule, a text_log
// slice, an analysis file.
type Entry struct {
	Stage    string // collector | alert | text_log | analysis
	Name     string // file name (system.parts.sql, too_many_parts.yaml …)
	Source   string // version directory it came from, "" = root
	Status   string // ok | failed | empty | skipped | fired | clean
	Duration time.Duration
	Bytes    int64  // result bytes written (collectors)
	Rows     int64  // result rows when the format is line-oriented; -1 = unknown
	Extra    string // stage-specific note (alert instance count, skip reason)
	Error    string
}

// Phase is the wall time of one step of the run.
type Phase struct {
	Name     string
	Duration time.Duration
	Note     string
}

// Recorder accumulates entries and phases. Safe for concurrent Record calls.
type Recorder struct {
	mu      sync.Mutex
	started time.Time
	meta    []kv
	entries []Entry
	phases  []Phase
}

type kv struct{ k, v string }

// New starts a recorder; the start time is the run's start time.
func New() *Recorder { return &Recorder{started: time.Now()} }

// SetMeta adds a header line (mode, server version, target, window …).
func (r *Recorder) SetMeta(key, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.meta {
		if r.meta[i].k == key {
			r.meta[i].v = value
			return
		}
	}
	r.meta = append(r.meta, kv{key, value})
}

// Record appends one entry. Error text is capped here so the file never
// carries a stack trace.
func (r *Recorder) Record(e Entry) {
	if r == nil {
		return
	}
	if len(e.Error) > errorCap {
		e.Error = e.Error[:errorCap] + "…"
	}
	e.Error = strings.ReplaceAll(strings.ReplaceAll(e.Error, "\n", " "), "|", "/")
	r.mu.Lock()
	r.entries = append(r.entries, e)
	r.mu.Unlock()
}

// Phase appends a phase timing.
func (r *Recorder) Phase(name string, d time.Duration, note string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.phases = append(r.phases, Phase{name, d, note})
	r.mu.Unlock()
}

// Entries returns a copy of the recorded entries (for tests and callers).
func (r *Recorder) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Entry(nil), r.entries...)
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%d µs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2f s", d.Seconds())
	default:
		return fmt.Sprintf("%.1f min", d.Minutes())
	}
}

func fmtBytes(b int64) string {
	const k = 1024.0
	f := float64(b)
	for _, u := range []string{"B", "KiB", "MiB", "GiB"} {
		if f < k || u == "GiB" {
			if u == "B" {
				return fmt.Sprintf("%d B", b)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= k
	}
	return fmt.Sprintf("%d B", b)
}

func src(e Entry) string {
	if e.Source == "" {
		return "root"
	}
	return e.Source
}

// Render produces the text of execution_log.txt. Sections: header, summary,
// most expensive collectors, failed collectors, every entry in execution
// order (a pipe table, so the skill's pre-pass can parse it), alert rules,
// phases.
func (r *Recorder) Render(now time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder
	w := func(format string, a ...interface{}) { fmt.Fprintf(&b, format, a...) }

	w("clickhouse-diagnostic execution log\n")
	w("===================================\n")
	w("started:  %s\n", r.started.Format("2006-01-02 15:04:05 -0700"))
	w("finished: %s   (total %s)\n", now.Format("2006-01-02 15:04:05 -0700"), fmtDur(now.Sub(r.started)))
	for _, m := range r.meta {
		w("%-9s %s\n", m.k+":", m.v)
	}

	var collectors, alerts, others []Entry
	for _, e := range r.entries {
		switch e.Stage {
		case "collector":
			collectors = append(collectors, e)
		case "alert":
			alerts = append(alerts, e)
		default:
			others = append(others, e)
		}
	}
	count := func(es []Entry, status string) int {
		n := 0
		for _, e := range es {
			if e.Status == status {
				n++
			}
		}
		return n
	}
	var collTotal time.Duration
	for _, e := range collectors {
		collTotal += e.Duration
	}

	w("\nSummary\n-------\n")
	w("collectors: %d ok, %d failed, %d empty — %s of query time\n",
		count(collectors, "ok"), count(collectors, "failed"), count(collectors, "empty"), fmtDur(collTotal))
	if len(alerts) > 0 {
		w("alerts:     %d rules — %d fired, %d clean, %d failed, %d skipped\n",
			len(alerts), count(alerts, "fired"), count(alerts, "clean"), count(alerts, "failed"), count(alerts, "skipped"))
	}
	if len(others) > 0 {
		w("other:      %d entries (%d failed)\n", len(others), count(others, "failed"))
	}
	if len(r.phases) > 0 {
		parts := make([]string, 0, len(r.phases))
		for _, p := range r.phases {
			parts = append(parts, fmt.Sprintf("%s %s", p.Name, fmtDur(p.Duration)))
		}
		w("phases:     %s\n", strings.Join(parts, " | "))
	}

	if len(collectors) > 0 {
		sorted := append([]Entry(nil), collectors...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Duration > sorted[j].Duration })
		w("\nMost expensive collectors (by wall time; tune windows in queries.<mode>/ from here)\n")
		w("--------------------------------------------------------------------------------\n")
		for i, e := range sorted {
			if i >= 10 {
				break
			}
			rows := ""
			if e.Rows >= 0 {
				rows = fmt.Sprintf("  %d rows", e.Rows)
			}
			w("%3d. %10s  %-48s (%s)  %s%s  %s\n", i+1, fmtDur(e.Duration), e.Name, src(e), fmtBytes(e.Bytes), rows, e.Status)
		}
	}

	if n := count(collectors, "failed"); n > 0 {
		w("\nFailed collectors (%d) — a missing result file is one of these, not an empty table\n", n)
		w("-------------------------------------------------------------------------------\n")
		for _, e := range collectors {
			if e.Status == "failed" {
				w("  %-48s (%s)  %s — %s\n", e.Name, src(e), fmtDur(e.Duration), e.Error)
			}
		}
	}

	w("\nAll entries (execution order)\n-----------------------------\n")
	w("| # | stage | name | source | status | duration_ms | bytes | rows | note | error |\n")
	w("|---|---|---|---|---|---|---|---|---|---|\n")
	for i, e := range r.entries {
		rows := ""
		if e.Rows >= 0 {
			rows = fmt.Sprintf("%d", e.Rows)
		}
		w("| %d | %s | %s | %s | %s | %d | %d | %s | %s | %s |\n",
			i+1, e.Stage, e.Name, src(e), e.Status, e.Duration.Milliseconds(), e.Bytes, rows,
			strings.ReplaceAll(e.Extra, "|", "/"), e.Error)
	}

	if len(r.phases) > 0 {
		w("\nPhases\n------\n")
		for _, p := range r.phases {
			note := ""
			if p.Note != "" {
				note = "  — " + p.Note
			}
			w("  %-16s %10s%s\n", p.Name, fmtDur(p.Duration), note)
		}
	}
	w("\nThe archive step runs after this file is written, so its duration is not recorded here.\n")
	return b.String()
}

// Write renders the log into dir/execution_log.txt and returns the path.
func (r *Recorder) Write(dir string) (string, error) {
	if r == nil {
		return "", nil
	}
	dst := filepath.Join(dir, FileName)
	if err := os.WriteFile(dst, []byte(r.Render(time.Now())), 0640); err != nil {
		return "", fmt.Errorf("write %s: %w", FileName, err)
	}
	return dst, nil
}
