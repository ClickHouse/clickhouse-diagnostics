// Package collectmode decides how much the diagnostic collects: everything
// ("full"), or only the cheap tables that decide most incidents
// ("essential"), when the server is visibly struggling. A short probe before
// the collectors decides the starting mode (Decide); a watchdog during the
// run switches to essential when queries start timing out or crawling
// (Watchdog). The operator can force either with -collect.
package collectmode

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Mode is what -collect asked for, or what was decided.
type Mode string

const (
	Auto      Mode = "auto"
	Essential Mode = "essential"
	Full      Mode = "full"
)

// Parse reads the -collect flag value; empty means auto.
func Parse(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", Auto:
		return Auto, nil
	case Essential, Full:
		return m, nil
	}
	return "", fmt.Errorf("-collect must be auto, essential or full (got %q)", s)
}

// Signals are what the probe read. Zero limits mean "unknown": a ratio
// against an unknown limit is never a reason.
type Signals struct {
	Ping                 time.Duration // SELECT 1
	MetricsRead          time.Duration // the probe's system.metrics read
	Queries              int64         // system.metrics Query
	MaxConcurrentQueries int64         // server setting max_concurrent_queries
	MemoryTracking       int64         // system.metrics MemoryTracking, bytes
	MemoryLimit          int64         // effective server memory limit, bytes
	Load1                float64       // asynchronous_metrics LoadAverage1
	CPUs                 float64       // cores the server uses (max_threads auto)
	ReadonlyReplicas     int64         // system.metrics ReadonlyReplica
	KeeperSessions       int64         // system.metrics ZooKeeperSession
	// KeeperLossSince is ZooKeeperConnectionLossStartedTimestampSeconds:
	// non-zero while the connection to Keeper is lost (an expired session
	// still counts as a ZooKeeperSession). 0 on versions without it.
	KeeperLossSince int64
	Tables          int64 // asynchronous_metrics NumberOfTables
}

// Thresholds, from the field: they start where a node was visibly in
// trouble in the escalations and are meant to be tuned, not trusted blindly.
const (
	slowPing        = 1 * time.Second
	slowMetrics     = 2 * time.Second
	concurrentRatio = 0.8
	memoryRatio     = 0.9
	loadPerCPU      = 2.0
	// LargeTables marks a server where per-table reads are expensive even
	// when it is healthy (one listing / Keeper read per table).
	LargeTables = 10000
)

// Decision is the starting mode and why.
type Decision struct {
	Requested Mode
	Mode      Mode     // Essential or Full
	Reasons   []string // why essential (empty for full)
	Large     bool     // ≥ LargeTables tables
	// KeeperDown: no Keeper session while replicated tables are read-only.
	// Queries that need a live session (system.zookeeper_connection,
	// database_replicas, the Keeper-backed replicas columns) then wait for
	// their timeout instead of answering, so the run skips them.
	KeeperDown bool
	Signals    Signals
}

// Decide turns the probe's signals into a starting mode. probeErr is the
// probe's own failure: a server that cannot answer three in-memory reads is
// treated as struggling.
func Decide(requested Mode, s Signals, probeErr error) Decision {
	d := Decision{Requested: requested, Signals: s, Large: s.Tables >= LargeTables,
		KeeperDown: probeErr == nil && s.ReadonlyReplicas > 0 && (s.KeeperSessions == 0 || s.KeeperLossSince > 0)}
	var why []string
	if probeErr != nil {
		why = append(why, "the load probe itself failed ("+firstLine(probeErr.Error())+")")
	} else {
		if s.Ping >= slowPing {
			why = append(why, fmt.Sprintf("the server took %.1f s to answer SELECT 1", s.Ping.Seconds()))
		}
		if s.MetricsRead >= slowMetrics {
			why = append(why, fmt.Sprintf("reading system.metrics took %.1f s", s.MetricsRead.Seconds()))
		}
		if s.MaxConcurrentQueries > 0 && float64(s.Queries) >= concurrentRatio*float64(s.MaxConcurrentQueries) {
			why = append(why, fmt.Sprintf("%d queries running against max_concurrent_queries = %d", s.Queries, s.MaxConcurrentQueries))
		}
		if s.MemoryLimit > 0 && float64(s.MemoryTracking) >= memoryRatio*float64(s.MemoryLimit) {
			why = append(why, fmt.Sprintf("memory at %.0f %% of the server limit", 100*float64(s.MemoryTracking)/float64(s.MemoryLimit)))
		}
		if s.CPUs > 0 && s.Load1 >= loadPerCPU*s.CPUs {
			why = append(why, fmt.Sprintf("load average %.1f on %.0f cores", s.Load1, s.CPUs))
		}
		if s.ReadonlyReplicas > 0 {
			cause := "Keeper session or disk trouble"
			if d.KeeperDown {
				cause = "no Keeper session"
				if s.KeeperLossSince > 0 {
					cause = "Keeper connection lost since " + time.Unix(s.KeeperLossSince, 0).UTC().Format("2006-01-02 15:04:05 UTC")
				}
			}
			why = append(why, fmt.Sprintf("%d replicated table(s) read-only (%s)", s.ReadonlyReplicas, cause))
		}
	}
	switch requested {
	case Full:
		d.Mode = Full
	case Essential:
		d.Mode = Essential
	default:
		d.Mode = Full
		if len(why) > 0 {
			d.Mode = Essential
		}
	}
	d.Reasons = why
	return d
}

// Summary is the one-line explanation for the console, execution_log.txt
// and the dashboard banner.
func (d Decision) Summary() string {
	switch {
	case d.Requested == Essential:
		return "essential collection, requested with -collect essential"
	case d.Requested == Full:
		s := "full collection, requested with -collect full"
		if len(d.Reasons) > 0 {
			s += " (the probe saw: " + strings.Join(d.Reasons, "; ") + ")"
		}
		return s
	case d.Mode == Essential:
		return "essential collection, chosen automatically: " + strings.Join(d.Reasons, "; ")
	default:
		return "full collection (the server answered the load probe normally)"
	}
}

// What an essential collection does differently.
const (
	EssentialQueryTimeout = 30 * time.Second // per query, unless -query-timeout was given
	EssentialBudget       = 3 * time.Minute  // the whole collection, unless -time-budget was given
	EssentialLookback     = 24 * time.Hour   // every 3- and 7-day default window becomes 1 day
)

// slowStreak: two consecutive queries at least this slow switch the rest of
// the run to essential.
const slowQuery = 30 * time.Second

// Watchdog watches the collectors and switches to essential for the rest of
// the run when the server starts timing out or crawling, and enforces an
// overall time budget.
type Watchdog struct {
	budget     time.Duration
	tripBudget time.Duration
	started    time.Time
	tripped    bool
	reason     string
	lastSlow   string
	onTrip     func(reason string)
	locked     bool
	now        func() time.Time
}

// Locked makes the watchdog never switch the run to essential (-collect
// full); its budget, if any, still applies.
func (w *Watchdog) Locked() *Watchdog {
	w.locked = true
	return w
}

// WithTripBudget sets the budget that starts when the run trips to
// essential mid-run (used only when no overall budget was given).
func (w *Watchdog) WithTripBudget(d time.Duration) *Watchdog {
	w.tripBudget = d
	return w
}

// OnTrip registers a callback run once, when the run becomes essential.
func (w *Watchdog) OnTrip(f func(reason string)) {
	if w != nil {
		w.onTrip = f
	}
}

// NewWatchdog returns a watchdog; budget 0 means unbounded.
func NewWatchdog(budget time.Duration) *Watchdog {
	return &Watchdog{budget: budget, now: time.Now}
}

// Start begins the budget clock (called once, before the first collector).
func (w *Watchdog) Start() {
	if w != nil {
		w.started = w.now()
	}
}

var reTimeout = regexp.MustCompile(`(?i)TIMEOUT_EXCEEDED|Code: 159\b|deadline exceeded|Client\.Timeout|timeout awaiting|i/o timeout`)

// Observe records one query's outcome.
func (w *Watchdog) Observe(name string, d time.Duration, err error) {
	if w == nil || w.tripped {
		return
	}
	if err != nil && reTimeout.MatchString(err.Error()) {
		w.trip(fmt.Sprintf("%s timed out after %.0f s", name, d.Seconds()))
		return
	}
	if d >= slowQuery {
		if w.lastSlow != "" {
			w.trip(fmt.Sprintf("%s and %s each took more than %.0f s", w.lastSlow, name, slowQuery.Seconds()))
			return
		}
		w.lastSlow = name
		return
	}
	w.lastSlow = ""
}

func (w *Watchdog) trip(reason string) {
	if w.locked {
		return
	}
	w.tripped, w.reason = true, reason
	if w.budget <= 0 && w.tripBudget > 0 {
		w.budget, w.started = w.tripBudget, w.now()
	}
	if w.onTrip != nil {
		w.onTrip(reason)
	}
}

// Trip switches to essential for an external reason (for example the
// essential mode chosen up front), keeping the first reason given.
func (w *Watchdog) Trip(reason string) {
	if w != nil && !w.tripped {
		w.trip(reason)
	}
}

// Tripped reports whether the run is now essential, and why.
func (w *Watchdog) Tripped() (bool, string) {
	if w == nil {
		return false, ""
	}
	return w.tripped, w.reason
}

// OverBudget reports whether the overall time budget is spent.
func (w *Watchdog) OverBudget() bool {
	if w == nil || w.budget <= 0 || w.started.IsZero() {
		return false
	}
	return w.now().Sub(w.started) > w.budget
}

// Budget returns the configured budget (0 = unbounded).
func (w *Watchdog) Budget() time.Duration {
	if w == nil {
		return 0
	}
	return w.budget
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
