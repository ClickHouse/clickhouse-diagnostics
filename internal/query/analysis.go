package query

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"clickhouse-diagnostic/pkg"
)

// reQueryID matches a canonical UUID — eight-four-four-four-twelve hex
// digits. Anchored so a partial match cannot smuggle extra SQL through.
var reQueryID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// reNormalizedHash matches a ClickHouse normalized_query_hash — a
// stringified UInt64. Up to 20 digits (max value is 18446744073709551615).
var reNormalizedHash = regexp.MustCompile(`^[0-9]{1,20}$`)

// ValidateQueryID returns an error if id is not a canonical UUID.
// Used by both the CLI flag parser and the analysis collector before
// the value is spliced into a SQL string literal.
func ValidateQueryID(id string) error {
	if id == "" {
		return fmt.Errorf("query-id is required")
	}
	if !reQueryID.MatchString(id) {
		return fmt.Errorf("query-id must be a UUID (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx); got %q", id)
	}
	return nil
}

// ValidateNormalizedQueryHash returns an error if h is not a decimal
// uint64. Empty is rejected — this is only called when the caller
// already requires a value.
func ValidateNormalizedQueryHash(h string) error {
	if h == "" {
		return fmt.Errorf("normalized-query-hash is required")
	}
	if !reNormalizedHash.MatchString(h) {
		return fmt.Errorf("normalized-query-hash must be a positive integer (UInt64); got %q", h)
	}
	return nil
}

// ParseTimeFlag accepts RFC3339 (`2026-05-28T10:00:00Z`) or
// date-only (`2026-05-28`, interpreted as midnight UTC). Used by both
// --from and --to.
func ParseTimeFlag(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q — use RFC3339 (2026-05-28T10:00:00Z) or YYYY-MM-DD", s)
}

// AnalysisOpts captures the runtime parameters for the query-analysis
// collection phase. Constructed by cmd/main.go from CLI flags and
// passed into AnalysisCollector.Collect.
//
// QueryID and NormalizedQueryHash are mutually compatible — supplying
// both is allowed and skips the pre-flight hash derivation.
// At least one of them must be non-empty for the collector to run.
type AnalysisOpts struct {
	QueryID             string
	NormalizedQueryHash string
	From                time.Time // window start (UTC)
	To                  time.Time // window end (UTC)
}

// Enabled reports whether either focus parameter is set — i.e.
// whether the analysis bundle should run at all.
func (o AnalysisOpts) Enabled() bool {
	return o.QueryID != "" || o.NormalizedQueryHash != ""
}

// Validate verifies the populated fields and applies sensible default
// time windows. `now` is the wall-clock used for defaults — callers
// pass time.Now() at flag-parsing time, tests pass a fixed timestamp.
//
// Default window rules:
//   - both --from and --to set:           use as-is
//   - one of them set:                    fill the other relative to now
//   - neither set, --query-id only:       last 3 days (event_time-centered
//                                          windowing happens after the
//                                          pre-flight; see PreflightForQueryID)
//   - neither set, --hash only:           last 7 days  (more history)
func (o *AnalysisOpts) Validate(now time.Time) error {
	if !o.Enabled() {
		return fmt.Errorf("analysis requires --query-id or --normalized-query-hash")
	}
	if o.QueryID != "" {
		if err := ValidateQueryID(o.QueryID); err != nil {
			return err
		}
	}
	if o.NormalizedQueryHash != "" {
		if err := ValidateNormalizedQueryHash(o.NormalizedQueryHash); err != nil {
			return err
		}
	}
	switch {
	case !o.From.IsZero() && !o.To.IsZero():
		// both set; trust the caller
	case !o.From.IsZero():
		o.To = now
	case !o.To.IsZero():
		o.From = o.To.AddDate(0, 0, -7)
	default:
		// neither set — defaults differ by which focus parameter is in play
		days := 7
		if o.QueryID != "" {
			days = 3
		}
		o.To = now
		o.From = now.AddDate(0, 0, -days)
	}
	if !o.To.After(o.From) {
		return fmt.Errorf("--to (%s) must be after --from (%s)",
			o.To.Format(time.RFC3339), o.From.Format(time.RFC3339))
	}
	return nil
}

// PreflightForQueryID looks up normalized_query_hash and event_time
// for the supplied --query-id. It runs ONCE before the analysis
// bundle, so the bundle's group queries can be parameterised with the
// derived hash without the user having to supply both flags.
//
// Returns the derived hash and the query's event_time. If event_time
// is non-zero, callers should re-center the default time window on it
// (so a 3-day-old slow query isn't excluded by a "last 3 days from
// now" default).
//
// Errors if the query_id isn't found in system.query_log within the
// caller-supplied search window (defaulted to 30 days back) — better
// to bail out with a clear message than ship empty analysis files.
func PreflightForQueryID(client *pkg.ClickHouseClient, mode, queryID string) (hash string, eventTime time.Time, err error) {
	if err := ValidateQueryID(queryID); err != nil {
		return "", time.Time{}, err
	}
	sql := fmt.Sprintf(`SELECT
		toString(normalized_query_hash) AS h,
		toString(event_time) AS et
	FROM %s
	WHERE query_id = '%s'
	  AND event_time > now() - INTERVAL 30 DAY
	  AND type != 'QueryStart'
	ORDER BY event_time DESC
	LIMIT 1
	FORMAT JSONCompact`, SysTable(mode, "query_log"), queryID)

	raw, err := client.ExecuteQuery(sql)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("preflight lookup for query_id %s failed: %w", queryID, err)
	}
	var res struct {
		Data [][]string `json:"data"`
		Rows int        `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return "", time.Time{}, fmt.Errorf("preflight parse failed: %w (%.200s)", err, raw)
	}
	if res.Rows == 0 || len(res.Data) == 0 {
		return "", time.Time{}, fmt.Errorf("query_id %s not found in system.query_log within the last 30 days", queryID)
	}
	row := res.Data[0]
	if len(row) < 2 {
		return "", time.Time{}, fmt.Errorf("preflight returned unexpected shape: %v", row)
	}
	hash = row[0]
	if t, parseErr := time.Parse("2006-01-02 15:04:05", row[1]); parseErr == nil {
		eventTime = t.UTC()
	}
	return hash, eventTime, nil
}

// AnalysisCollector runs the query-analysis SQL bundle and writes the
// results into a `query_analysis/` subdirectory of the per-run backup
// folder.
type AnalysisCollector struct {
	client *pkg.ClickHouseClient
	mode   string
}

// NewAnalysisCollector returns a collector bound to a connected client.
func NewAnalysisCollector(client *pkg.ClickHouseClient, mode string) *AnalysisCollector {
	return &AnalysisCollector{client: client, mode: strings.ToLower(mode)}
}

// Collect walks queriesDir for .sql files, applies the template
// (sys/query_id/normalized_query_hash/from/to placeholders), validates
// each query, executes it, and writes the result as a `.native` file
// into <backupDir>/query_analysis/.
//
// Files that still contain unbound placeholders after template
// substitution are skipped — that's how single-query-id-only files
// behave when --normalized-query-hash is set without --query-id, and
// vice versa.
//
// Returns the number of files written and the number skipped due to
// unbound placeholders (informational only — no error).
func (c *AnalysisCollector) Collect(opts AnalysisOpts, queriesDir, backupDir string) (written, skipped int, err error) {
	if !opts.Enabled() {
		return 0, 0, fmt.Errorf("analysis collector called with no focus parameter set")
	}

	entries, err := os.ReadDir(queriesDir)
	if err != nil {
		return 0, 0, fmt.Errorf("read query-analysis dir %s: %w", queriesDir, err)
	}

	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files) // deterministic execution order

	if len(files) == 0 {
		return 0, 0, fmt.Errorf("no .sql files found in %s", queriesDir)
	}

	outDir := filepath.Join(backupDir, "query_analysis")
	if err := os.MkdirAll(outDir, 0750); err != nil {
		return 0, 0, fmt.Errorf("create query_analysis output dir: %w", err)
	}

	vars := Vars{
		Mode:                c.mode,
		QueryID:             opts.QueryID,
		NormalizedQueryHash: opts.NormalizedQueryHash,
		From:                opts.From,
		To:                  opts.To,
	}
	timestamp := time.Now().UTC().Format("20060102_150405")

	fmt.Printf("\nQuery analysis: running %d file(s) (window %s → %s)\n",
		len(files), opts.From.Format(time.RFC3339), opts.To.Format(time.RFC3339))

	for _, fname := range files {
		full := filepath.Join(queriesDir, fname)
		raw, readErr := os.ReadFile(full)
		if readErr != nil {
			fmt.Printf("  [analysis] read %s: %v\n", fname, readErr)
			continue
		}
		applied := Apply(string(raw), vars)
		if unbound := UnboundPlaceholders(applied); len(unbound) > 0 {
			fmt.Printf("  [analysis] skip %s — needs %v\n", fname, unbound)
			skipped++
			continue
		}
		if validateErr := ValidateQueryContent(applied); validateErr != nil {
			fmt.Printf("  [analysis] skip %s — validation: %v\n", fname, validateErr)
			skipped++
			continue
		}
		result, qErr := c.client.ExecuteQuery(applied)
		if qErr != nil {
			fmt.Printf("  [analysis] %s: %v\n", fname, qErr)
			continue
		}
		base := strings.TrimSuffix(fname, ".sql")
		out := filepath.Join(outDir, fmt.Sprintf("%s_%s.native", base, timestamp))
		if writeErr := os.WriteFile(out, []byte(result), 0600); writeErr != nil {
			fmt.Printf("  [analysis] write %s: %v\n", out, writeErr)
			continue
		}
		fmt.Printf("  [analysis] %s → %s\n", fname, out)
		written++
	}
	fmt.Printf("Query analysis: %d written, %d skipped\n", written, skipped)
	return written, skipped, nil
}
