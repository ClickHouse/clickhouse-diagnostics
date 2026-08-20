package query

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/pkg"
)

// TextLogOpts configures a bounded dump of system.text_log.
//
// This collector is OPT-IN and requires an explicit time window. text_log is
// the highest-volume table ClickHouse writes — an unbounded dump can be
// gigabytes and will itself load the server — so there is deliberately no
// default range: the operator must say which incident they are looking at.
type TextLogOpts struct {
	From     time.Time
	To       time.Time
	Level    string // optional minimum severity filter, e.g. "Warning"
	RowLimit int
}

// DefaultTextLogRowLimit bounds a single collection. Generous enough for an
// incident window, small enough that the archive stays shippable.
const DefaultTextLogRowLimit = 500000

// Validate enforces the required window and normalises the row limit.
func (o *TextLogOpts) Validate() error {
	if o.From.IsZero() || o.To.IsZero() {
		return fmt.Errorf("--collect-text-log requires both --from and --to " +
			"(text_log is high-volume; an unbounded dump would be huge and would load the server)")
	}
	if !o.To.After(o.From) {
		return fmt.Errorf("--to (%s) must be after --from (%s)",
			o.To.Format(time.RFC3339), o.From.Format(time.RFC3339))
	}
	if o.RowLimit <= 0 {
		o.RowLimit = DefaultTextLogRowLimit
	}
	if o.Level != "" && !validLevels[strings.Title(strings.ToLower(o.Level))] {
		return fmt.Errorf("--text-log-level %q is not a ClickHouse log level (%s)",
			o.Level, strings.Join(levelNames, ", "))
	}
	return nil
}

var levelNames = []string{"Fatal", "Critical", "Error", "Warning", "Notice", "Information", "Debug", "Trace"}

var validLevels = func() map[string]bool {
	m := make(map[string]bool, len(levelNames))
	for _, l := range levelNames {
		m[l] = true
	}
	return m
}()

// TextLogCollector dumps a time-bounded slice of system.text_log.
type TextLogCollector struct {
	client *pkg.ClickHouseClient
	mode   string
}

func NewTextLogCollector(client *pkg.ClickHouseClient, mode string) *TextLogCollector {
	return &TextLogCollector{client: client, mode: strings.ToLower(mode)}
}

// Collect writes text_log_<timestamp>.native into outDir and returns the
// path. A missing text_log table is reported as "not collected" rather than
// an error: the table is disabled by default in ClickHouse, so its absence
// is a configuration fact, not a failure — the same skipped-vs-errored
// distinction the alert evaluator makes.
func (c *TextLogCollector) Collect(opts TextLogOpts, outDir string, serverVersion internal.Version) (string, error) {
	if err := opts.Validate(); err != nil {
		return "", err
	}

	sql := c.buildSQL(opts, serverVersion)
	if err := ValidateQueryContent(sql); err != nil {
		return "", fmt.Errorf("security validation failed for text_log query: %w", err)
	}

	fmt.Printf("Collecting system.text_log between %s and %s (limit %d rows)...\n",
		opts.From.Format(time.RFC3339), opts.To.Format(time.RFC3339), opts.RowLimit)

	result, err := c.client.ExecuteQuery(sql)
	if err != nil {
		if strings.Contains(err.Error(), "UNKNOWN_TABLE") {
			return "", fmt.Errorf("system.text_log does not exist on this server — "+
				"it is disabled by default; enable the <text_log> section in the server "+
				"configuration to collect it (original error: %w)", err)
		}
		return "", err
	}

	if c.client.IsDryRun() {
		return "", nil // the client already printed the SQL; nothing to write
	}

	path := filepath.Join(outDir, fmt.Sprintf("text_log_%s.native",
		time.Now().UTC().Format("20060102_150405")))
	if err := os.WriteFile(path, []byte(result), 0600); err != nil {
		return "", fmt.Errorf("write text_log output: %w", err)
	}
	return path, nil
}

// buildSQL assembles the query. Kept separate from Collect so the SQL shape
// is unit-testable without a server.
func (c *TextLogCollector) buildSQL(opts TextLogOpts, serverVersion internal.Version) string {
	// message_format_string was added in 23.1; selecting it on an older
	// server is an UNKNOWN_IDENTIFIER, so gate it the same way the
	// version-directory .sql files do.
	formatCol := ""
	if isAtLeast(serverVersion, 23, 1) {
		formatCol = "    message_format_string,\n"
	}

	var levelFilter string
	if opts.Level != "" {
		// text_log.level is an Enum8 ordered most-severe-first, so "at least
		// this severe" is <=. Comparing against the string lets ClickHouse
		// resolve the enum without us hardcoding numeric values.
		levelFilter = fmt.Sprintf("  AND level <= '%s'\n", strings.Title(strings.ToLower(opts.Level)))
	}

	return fmt.Sprintf(`SELECT
    event_time_microseconds                                AS ts,
    hostName()                                             AS hostname,
    level,
    thread_id,
    logger_name,
    message,
%s    source_file,
    source_line,
    query_id
FROM %s
WHERE event_time >= '%s'
  AND event_time <= '%s'
%sORDER BY event_time_microseconds ASC
LIMIT %d
FORMAT Native`,
		formatCol,
		SysTable(c.mode, "text_log"),
		opts.From.UTC().Format("2006-01-02 15:04:05"),
		opts.To.UTC().Format("2006-01-02 15:04:05"),
		levelFilter,
		opts.RowLimit)
}

// isAtLeast reports whether v >= major.minor.
func isAtLeast(v internal.Version, major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}
