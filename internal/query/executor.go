package query

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/collection"
	"clickhouse-diagnostic/internal/collectmode"
	"clickhouse-diagnostic/internal/runlog"
	"clickhouse-diagnostic/pkg"
)

// Executor handles query execution and result management
type Executor struct {
	client *pkg.ClickHouseClient
	// salt, when non-empty, replaces the literal '%salt%' placeholder
	// inside .sql query bodies just before execution. Used only in
	// gov mode so the hash of database/table names is unique to the
	// customer's run instead of a publicly known constant.
	salt string
	// format is the ClickHouse FORMAT results are serialised in, and the
	// extension they are written with. Zero value means the default.
	format OutputFormat
	// from / to override every query's own {from:…} / {to:…} default when
	// non-zero. Zero means "each query keeps the window it declares".
	from time.Time
	to   time.Time
	// mode feeds {sys.<table>} expansion (cloud wraps per-replica tables
	// in clusterAllReplicas). Empty leaves such placeholders unexpanded,
	// which the unbound check below then refuses.
	mode string
	// rec, when set, receives one entry per query (outcome, wall time,
	// result size) for execution_log.txt. nil is a no-op.
	rec *runlog.Recorder
	// watch decides, collector by collector, whether the run is still full:
	// once it has tripped (an essential collection chosen up front, a
	// timeout, two slow queries in a row) full-tier collectors are skipped,
	// and past the time budget everything left is. nil = always full.
	watch *collectmode.Watchdog
	// maxLookback caps every {from:<default>} window (essential: 1 day).
	maxLookback time.Duration
	// detachedScope / detachedScopeP fill {detached_scope(_p)}.
	detachedScope, detachedScopeP string
	// keeperDown: skip collectors that need a live Keeper session, with
	// this reason.
	keeperDown string
}

// WithKeeperDown makes the executor skip `-- needs: keeper` collectors
// (reason non-empty: the server has no Keeper session).
func (e *Executor) WithKeeperDown(reason string) *Executor {
	e.keeperDown = reason
	return e
}

// WithDetachedScope sets the detached-parts scope (see DetachedScope).
func (e *Executor) WithDetachedScope(scope, qualified string) *Executor {
	e.detachedScope, e.detachedScopeP = scope, qualified
	return e
}

// WithWatchdog attaches the collection-mode watchdog.
func (e *Executor) WithWatchdog(w *collectmode.Watchdog) *Executor {
	e.watch = w
	return e
}

// WithMaxLookback caps the default window of every query (0 = none).
func (e *Executor) WithMaxLookback(d time.Duration) *Executor {
	e.maxLookback = d
	return e
}

// WithRecorder attaches the execution-log recorder.
func (e *Executor) WithRecorder(r *runlog.Recorder) *Executor {
	e.rec = r
	return e
}

// WithMode sets the topology mode used for {sys.<table>} expansion.
func (e *Executor) WithMode(mode string) *Executor {
	e.mode = mode
	return e
}

// WithWindow overrides the time window of every query that declares one.
// Zero values leave each query's own default in place.
func (e *Executor) WithWindow(from, to time.Time) *Executor {
	e.from, e.to = from, to
	return e
}

// NewExecutor creates a new query executor
func NewExecutor(client *pkg.ClickHouseClient) *Executor {
	return &Executor{
		client: client,
		format: DefaultOutputFormat,
	}
}

// WithOutputFormat sets the serialisation format for query results.
func (e *Executor) WithOutputFormat(f OutputFormat) *Executor {
	e.format = f
	return e
}

// outputFormat returns the configured format, tolerating a zero-value
// Executor built by older call sites or tests.
func (e *Executor) outputFormat() OutputFormat {
	if e.format.Clause == "" {
		return DefaultOutputFormat
	}
	return e.format
}

// WithSalt sets the gov-mode salt used for runtime substitution of the
// '%salt%' placeholder in .sql files. The salt must already be sanitised
// (alphanumeric); the caller is responsible for validation.
func (e *Executor) WithSalt(salt string) *Executor {
	e.salt = salt
	return e
}

// RunFolderName is the name of a run's results folder — and, with .tar.gz,
// of the archive built from it: clickhouse_diagnostic_<YYYYMMDD_HHMMSS>.
// Releases before the rename wrote clickhouse_backup_<ts>; the pre-pass
// reads both.
func RunFolderName(t time.Time) string {
	return "clickhouse_diagnostic_" + t.Format("20060102_150405")
}

// ExecuteQueries executes a map of selected queries and saves results
// Returns the path to the specific folder where results were saved
func (e *Executor) ExecuteQueries(queries map[string]internal.QueryFile, outputDir string) (string, error) {
	now := time.Now()
	timestamp := now.Format("20060102_150405")
	finalOutputDir := filepath.Join(outputDir, RunFolderName(now))

	// Create output directory
	if err := os.MkdirAll(finalOutputDir, 0750); err != nil {
		return "", fmt.Errorf("error creating output directory: %w", err)
	}

	if e.client.IsDryRun() {
		fmt.Printf("Would execute %d unique queries (dry-run):\n", len(queries))
	} else {
		fmt.Printf("Executing %d unique queries...\n\n", len(queries))
	}

	successCount := 0
	errorCount := 0
	skippedCount := 0

	// Essential collectors first, then the rest, each group by name: the
	// cheap decisive tables are in the bundle even if the run is cut short,
	// and the order no longer depends on map iteration.
	ordered := orderByTier(queries)
	for _, oq := range ordered {
		query := oq.file
		if e.watch.OverBudget() {
			e.skip(query, fmt.Sprintf("time budget (%v) spent before it ran", e.watch.Budget()))
			skippedCount++
			continue
		}
		if e.keeperDown != "" && oq.needsKeeper {
			e.skip(query, "needs a live Keeper session — "+e.keeperDown)
			skippedCount++
			continue
		}
		if tripped, why := e.watch.Tripped(); tripped && oq.tier != TierEssential {
			e.skip(query, "essential collection: "+why)
			skippedCount++
			continue
		}
		started := time.Now()
		err := e.executeQuery(query, finalOutputDir, timestamp)
		e.watch.Observe(query.Name, time.Since(started), err)
		if err != nil {
			fmt.Printf("Error executing query '%s': %v\n", query.Name, err)
			errorCount++
		} else {
			successCount++
		}
	}
	if skippedCount > 0 {
		fmt.Printf("\n%d collector(s) skipped (essential collection or time budget) — listed in execution_log.txt\n", skippedCount)
	}

	if e.client.IsDryRun() {
		fmt.Printf("\nDry-run summary: %d query file(s) would have been executed.\n", successCount)
	} else {
		fmt.Printf("\nQuery execution completed: %d successful, %d failed\n", successCount, errorCount)
		fmt.Printf("Results saved to: %s\n", finalOutputDir)
	}

	return finalOutputDir, nil
}

// executeQuery validates, executes a single query and saves the result.
func (e *Executor) executeQuery(query internal.QueryFile, outputDir, timestamp string) error {
	// Read query from file
	queryContent, err := os.ReadFile(query.FullPath)
	if err != nil {
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed", Rows: -1, Error: "error reading query file: " + err.Error()})
		return fmt.Errorf("error reading query file: %w", err)
	}

	if len(queryContent) == 0 {
		fmt.Printf("Query file '%s' is empty, skipping\n", query.FullPath)
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "empty", Rows: -1, Extra: "query file is empty"})
		return nil
	}

	// Bind the template. This resolves the {from:<default>} / {to:<default>}
	// time windows — to the --from / --to flags when they were given, and
	// otherwise to the default each query declares for itself — and, in gov
	// mode, replaces the public '%salt%' marker with the customer salt.
	// Salt format is validated upstream (alphanumeric only), so it cannot
	// break out of the SQL string literal.
	sqlText := Apply(string(queryContent), Vars{
		Mode:           e.mode,
		From:           e.from,
		To:             e.to,
		GovSalt:        e.salt,
		MaxLookback:    e.lookback(),
		DetachedScope:  e.detachedScope,
		DetachedScopeP: e.detachedScopeP,
	})

	// A placeholder left unbound would reach the server as a literal
	// brace: either a parse error or, worse, a silently wrong window.
	if unbound := UnboundPlaceholders(sqlText); len(unbound) > 0 {
		err := fmt.Errorf("query '%s' has unbound placeholder(s) %v", query.Name, unbound)
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed", Rows: -1, Error: err.Error()})
		return err
	}

	// Serialise in the configured format, replacing any FORMAT the file
	// carries. Done before validation so the validator sees exactly the
	// text that will be sent to the server.
	sqlText = e.outputFormat().Apply(sqlText)

	// Security: enforce read-only SELECT before execution.
	if err := ValidateQueryContent(sqlText); err != nil {
		err = fmt.Errorf("security validation failed for '%s': %w", query.Name, err)
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed", Rows: -1, Error: err.Error()})
		return err
	}

	// Show source information. Prefix changes in dry-run to make clear
	// nothing actually runs.
	verb := "Executing"
	if e.client.IsDryRun() {
		verb = "Would execute"
	}
	if query.DirName != "" {
		fmt.Printf("%s query '%s' from directory '%s'...\n", verb, query.Name, query.DirName)
	} else {
		fmt.Printf("%s query '%s' from root directory...\n", verb, query.Name)
	}

	// Execute the query, timing the round trip: this is the number that
	// tells which collectors are expensive on a given server.
	started := time.Now()
	result, err := e.client.ExecuteQuery(sqlText)
	elapsed := time.Since(started)
	if err != nil {
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed",
			Duration: elapsed, Rows: -1, Error: err.Error()})
		return fmt.Errorf("error executing query: %w", err)
	}

	// Credentials embedded in DDL. system.tables.create_table_query / engine_full
	// / as_select and system.dictionaries.source carry engine arguments verbatim
	// on servers below 23.x (an S3 secret key, a MySQL password); newer servers
	// mask them as '[HIDDEN]' themselves. The config sanitizer never saw these
	// files — it is wired to the XML/YAML collector only — so this is where the
	// result text is scrubbed before it touches disk.
	result, redactedNote := redactResult(query.Name, e.outputFormat().Ext, result)

	// Generate output filename
	baseFileName := strings.TrimSuffix(query.Name, filepath.Ext(query.Name))
	outputFileName := fmt.Sprintf("%s_%s%s", baseFileName, timestamp, e.outputFormat().Ext)
	outputPath := filepath.Join(outputDir, outputFileName)

	// Save the result to a file
	if err := os.WriteFile(outputPath, []byte(result), 0600); err != nil {
		e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "failed",
			Duration: elapsed, Bytes: int64(len(result)), Rows: -1, Error: "error saving result: " + err.Error()})
		return fmt.Errorf("error saving result: %w", err)
	}

	// Rows are only countable in line-oriented formats; Native is opaque, and
	// TSVWithNamesAndTypes carries two header lines that are not data.
	rows := int64(-1)
	switch e.outputFormat().Ext {
	case ".jsonl":
		rows = int64(strings.Count(result, "\n"))
	case ".tsv":
		rows = int64(strings.Count(result, "\n")) - 2
		if rows < 0 {
			rows = 0
		}
	}
	extra := outputFileName
	if redactedNote != "" {
		extra += "; " + redactedNote
	}
	e.rec.Record(runlog.Entry{Stage: "collector", Name: query.Name, Source: query.DirName, Status: "ok",
		Duration: elapsed, Bytes: int64(len(result)), Rows: rows, Extra: extra})

	if !e.client.IsDryRun() {
		fmt.Printf("Query '%s' executed successfully. Result saved to %s\n\n", query.Name, outputPath)
	}
	return nil
}

// ValidateQuery reads a query file and validates it with ValidateQueryContent.
func (e *Executor) ValidateQuery(queryPath string) error {
	content, err := os.ReadFile(queryPath)
	if err != nil {
		return fmt.Errorf("cannot read query file: %w", err)
	}
	return ValidateQueryContent(string(content))
}

// redactResult scrubs credentials from the DDL-bearing columns of a collector's
// result before it is written. Only the collectors named in
// collection.SensitiveCollectorFields are touched, and only in the JSONL format,
// where a field can be located and rewritten without disturbing any other byte.
// Native and TSV are opaque to a field-level pass, so they are written as-is
// with a note in the execution log — one more reason the default format is the
// one to ship.
func redactResult(queryName, ext, result string) (string, string) {
	fields, ok := collection.SensitiveCollectorFields[queryName]
	if !ok {
		return result, ""
	}
	if ext != ".jsonl" {
		fmt.Printf("  warning: %s carries DDL text and is written as %s, which cannot be redacted field by field; "+
			"credentials in engine arguments may be present — use the default jsonl format for a shareable bundle\n",
			queryName, strings.TrimPrefix(ext, "."))
		return result, "NOT redacted (non-JSONL format)"
	}
	out, n := collection.RedactJSONLFields(result, fields)
	if n == 0 {
		return out, ""
	}
	return out, fmt.Sprintf("%d credential(s) replaced with [HIDDEN]", n)
}

type tieredQuery struct {
	file        internal.QueryFile
	tier        string
	needsKeeper bool
}

// orderByTier sorts collectors essential-first, then by name. A file whose
// tier cannot be read counts as full; executeQuery reports the read error.
func orderByTier(queries map[string]internal.QueryFile) []tieredQuery {
	out := make([]tieredQuery, 0, len(queries))
	for _, q := range queries {
		tier, needs := TierFull, false
		if b, err := os.ReadFile(q.FullPath); err == nil {
			if t, err := ParseTier(string(b)); err == nil {
				tier = t
			}
			needs = NeedsKeeper(string(b))
		}
		out = append(out, tieredQuery{q, tier, needs})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].tier == TierEssential) != (out[j].tier == TierEssential) {
			return out[i].tier == TierEssential
		}
		return out[i].file.Name < out[j].file.Name
	})
	return out
}

// skip records a collector that was not run, and why.
func (e *Executor) skip(q internal.QueryFile, why string) {
	fmt.Printf("Skipping '%s': %s\n", q.Name, why)
	e.rec.Record(runlog.Entry{Stage: "collector", Name: q.Name, Source: q.DirName, Status: "skipped", Rows: -1, Extra: why})
}

// lookback is the window cap for the next query: the configured one, or 1
// day once the run has become essential (chosen up front or mid-run).
func (e *Executor) lookback() time.Duration {
	if e.maxLookback > 0 {
		return e.maxLookback
	}
	if tripped, _ := e.watch.Tripped(); tripped {
		return collectmode.EssentialLookback
	}
	return 0
}
