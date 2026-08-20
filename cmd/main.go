package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/alert"
	"clickhouse-diagnostic/internal/config"
	"clickhouse-diagnostic/internal/dashboard"
	"clickhouse-diagnostic/internal/hostinfo"
	"clickhouse-diagnostic/internal/logfiles"
	"clickhouse-diagnostic/internal/query"
	"clickhouse-diagnostic/internal/version"
	"clickhouse-diagnostic/pkg"

	"golang.org/x/term"
)

func main() {
	// Define command line flags
	hostFlag := flag.String("host", "", "ClickHouse Host")
	portFlag := flag.String("port", "", "ClickHouse Port")
	userFlag := flag.String("user", "", "Username")
	passwordFlag := flag.String("password", "", "Password (not recommended for security reasons)")
	protocolFlag := flag.String("protocol", "", "Protocol (http or https)")
	modeFlag := flag.String("mode", "onprem", "Query mode (cloud, onprem, gov)")
	outputDirFlag := flag.String("output-dir", "./clickhouse_results", "Directory for results output")
	configDirFlag := flag.String("config-dir", "", "ClickHouse config directory to collect (default: /etc/clickhouse-server/config.d/)")
	skipConfigFlag := flag.Bool("skip-config", false, "Skip collecting configuration files")
	skipArchiveFlag := flag.Bool("skip-archive", false, "Skip creating archive of results and configuration")
	skipDashboardFlag := flag.Bool("skip-dashboard", false, "Skip generating HTML dashboard")
	skipAlertsFlag := flag.Bool("skip-alerts", false, "Skip evaluating alert rules")
	alertsDirFlag := flag.String("alerts-dir", "./alerts", "Directory containing alert YAML rule files")
	saltFlag := flag.String("salt", "", "Gov-mode hashing salt (8–64 alphanumeric chars; prompts interactively if empty)")
	queryIDFlag := flag.String("query-id", "", "Run query analysis focused on this query_id (UUID)")
	normalizedHashFlag := flag.String("normalized-query-hash", "", "Run query analysis focused on this normalized_query_hash (uint64)")
	fromFlag := flag.String("from", "", "Start of the collection window (RFC3339 or YYYY-MM-DD). "+
		"Overrides the per-query default look-back (7 days for most log tables, 1 day for text_log). "+
		"Also sets the query-analysis window")
	toFlag := flag.String("to", "", "End of the collection window (RFC3339 or YYYY-MM-DD; default: now). "+
		"Also sets the query-analysis window")
	analysisDirFlag := flag.String("analysis-dir", "./queries.query_analysis", "Directory containing query-analysis SQL files")
	hostInfoFlag := flag.String("host-info", "auto", "Collect host OS/kernel/CPU/memory/disk/process facts: auto|on|off. "+
		"auto = on for onprem, off for cloud (the tool would profile the machine it runs on, not the managed server) "+
		"and off for gov (never collected — hostnames and command lines cannot be hashed)")
	logsFlag := flag.String("logs", "auto", "Collect ClickHouse server log files from disk: auto|on|off. "+
		"auto = on for onprem, off for cloud (logs live on the managed nodes, not locally) and off for gov")
	logsDirFlag := flag.String("logs-dir", "", "Directory holding ClickHouse server logs. Default: discovered from the server configuration's <log>/<errorlog>, falling back to /var/log/clickhouse-server")
	logsMaxMBFlag := flag.Int("logs-max-mb", 50, "Per-file size cap for collected logs, in MiB. Larger files are tail-truncated (the recent end is kept)")
	logsArchivesFlag := flag.Bool("logs-include-archives", false, "Also collect rotated log archives (*.gz, *.zst). Off by default — they are often larger than the rest of the bundle combined")
	collectTextLogFlag := flag.Bool("collect-text-log", false, "Collect a time-bounded slice of system.text_log. Requires --from and --to; not available in gov mode")
	textLogLevelFlag := flag.String("text-log-level", "", "Minimum severity for --collect-text-log (Fatal|Critical|Error|Warning|Notice|Information|Debug|Trace). Default: all levels")
	textLogLimitFlag := flag.Int("text-log-limit", 0, fmt.Sprintf("Row cap for --collect-text-log (default %d)", query.DefaultTextLogRowLimit))
	outputFormatFlag := flag.String("output-format", query.DefaultOutputFormat.Name,
		fmt.Sprintf("Serialisation format for query results: %s. jsonl is one self-describing "+
			"JSON object per line (grep- and jq-able); native is the ClickHouse binary format; "+
			"tsv carries names and types in its first two lines. 64-bit integers are exact in "+
			"all three", query.OutputFormatNames()))
	dryRunFlag := flag.Bool("dry-run", false, "List every query the tool would execute (with the system tables each touches and an EXPLAIN ESTIMATE per SELECT) and exit. Does NOT write results or create an archive. EXPLAIN ESTIMATE is a read-only metadata query — it reports the rows/marks/parts the SELECT WOULD scan without reading any data.")

	// Parse command line flags
	flag.Parse()

	// Initialize variables with defaults
	var (
		host           = *hostFlag
		port           = *portFlag
		username       = *userFlag
		password       = *passwordFlag
		protocol       = *protocolFlag
		mode           = *modeFlag
		outputDir      = *outputDirFlag
		configDir      = *configDirFlag
		skipConfig     = *skipConfigFlag
		skipArchive    = *skipArchiveFlag
		dryRun         = *dryRunFlag
		skipDashboard  = *skipDashboardFlag
		skipAlerts     = *skipAlertsFlag
		alertsDir      = *alertsDirFlag
		govSalt        = *saltFlag
		queryID        = *queryIDFlag
		normalizedHash = *normalizedHashFlag
		fromStr        = *fromFlag
		toStr          = *toFlag
		analysisDir    = *analysisDirFlag
		hostInfoMode   = *hostInfoFlag
		logsMode       = *logsFlag
		logsDir        = *logsDirFlag
		logsMaxMB      = *logsMaxMBFlag
		logsArchives   = *logsArchivesFlag
		collectTextLog = *collectTextLogFlag
		textLogLevel   = *textLogLevelFlag
		textLogLimit   = *textLogLimitFlag
	)
	// --from / --to narrow the collection window of every query that
	// declares one. Parsed before anything blocking (the password prompt,
	// the connection) so a malformed or inverted window fails immediately.
	//
	// Parsed separately from analysisOpts: analysis substitutes its own
	// event-time-centred defaults when the flags are absent, whereas the
	// collection queries fall back to the per-query defaults they declare.
	collectFrom, err := query.ParseTimeFlag(fromStr)
	if err != nil {
		fmt.Printf("Error: invalid --from: %v\n", err)
		return
	}
	collectTo, err := query.ParseTimeFlag(toStr)
	if err != nil {
		fmt.Printf("Error: invalid --to: %v\n", err)
		return
	}
	if !collectFrom.IsZero() && !collectTo.IsZero() && collectTo.Before(collectFrom) {
		fmt.Println("Error: --to is earlier than --from")
		return
	}

	outputFormat, err := query.ParseOutputFormat(*outputFormatFlag)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Resolve the two local-filesystem collectors against the run mode.
	// Both read the machine the tool is EXECUTING on, which is only the
	// ClickHouse server in onprem deployments — hence the mode-dependent
	// default rather than a plain on/off.
	skipHostInfo, err := resolveLocalCollector(hostInfoMode, mode, "host-info")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	skipLogs, err := resolveLocalCollector(logsMode, mode, "logs")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// --dry-run is read-only by definition — silence the side effects
	// that would write empty/garbage artefacts to disk.
	if dryRun {
		skipConfig = true
		skipArchive = true
		// Host facts and log files are filesystem copies, not queries;
		// there is nothing meaningful to "preview", so skip them rather
		// than write files a dry run promised not to write.
		skipHostInfo = true
		skipLogs = true
	}

	// Get user input for missing parameters
	if err := getUserInput(&protocol, &host, &port, &username, &password, &mode, &configDir, skipConfig); err != nil {
		fmt.Printf("Error getting user input: %v\n", err)
		return
	}

	// Map mode to queries directory
	queriesDir := getQueriesDir(mode)

	// Create the output folder if it doesn't exist
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		fmt.Printf("Error creating output folder: %v\n", err)
		return
	}

	// Check if queries folder exists
	if _, err := os.Stat(queriesDir); os.IsNotExist(err) {
		fmt.Printf("Error: Queries folder '%s' does not exist\n", queriesDir)
		return
	}

	// Display connection information
	fmt.Printf("\nConnecting to ClickHouse at %s://%s:%s\n", protocol, host, port)
	if username != "" {
		fmt.Printf("Using authentication with user: %s\n", username)
	}
	fmt.Printf("Using query mode: %s (queries from: %s)\n", mode, queriesDir)

	// Collect configuration files if not skipped
	if !skipConfig {
		configCollector := config.NewCollector()
		configCollector.Collect(configDir, false)
	}

	// Create ClickHouse client
	client := pkg.NewClickHouseClient(protocol, host, port, username, password)

	// Check ClickHouse version
	fmt.Println("Checking ClickHouse server version...")
	versionResult, err := client.ExecuteQuery("SELECT version()")
	if err != nil {
		fmt.Printf("Error checking ClickHouse version: %v\n", err)
		return
	}

	// Parse the version string
	parser := version.NewParser()
	serverVersion, err := parser.ParseClickHouseVersion(versionResult)
	if err != nil {
		fmt.Printf("Error parsing ClickHouse version: %v\n", err)
		return
	}

	fmt.Printf("ClickHouse server version: %d.%d.%d.%d\n",
		serverVersion.Major, serverVersion.Minor, serverVersion.Patch, serverVersion.Build)

	// Query analysis is not available in gov mode: its results embed raw
	// query text, exception messages, identifiers and full DDL
	// (query_details, failed_queries, tables_for_query, text_log slices),
	// which gov-mode hashing cannot cover — the archive would leak the
	// names gov exists to protect. Refuse the flags rather than silently
	// producing an unhashed bundle. Checked before the dry-run banner so
	// an unsupported combination fails immediately.
	if mode == "gov" && (queryID != "" || normalizedHash != "") {
		fmt.Println("Error: --query-id / --normalized-query-hash are not supported in gov mode " +
			"(query-analysis output contains raw query text and identifiers that cannot be hashed)")
		return
	}

	// Same reasoning for text_log: its messages are free-form text carrying
	// raw SQL, identifiers and file paths. Hashing a log line destroys the
	// only thing that makes it useful, so gov refuses rather than shipping
	// it unhashed.
	if mode == "gov" && collectTextLog {
		fmt.Println("Error: --collect-text-log is not supported in gov mode " +
			"(log messages embed raw queries and identifiers that cannot be hashed)")
		return
	}

	// Fail fast on the required window rather than after the collection run.
	if collectTextLog && (fromStr == "" || toStr == "") {
		fmt.Println("Error: --collect-text-log requires both --from and --to " +
			"(system.text_log is high-volume; an unbounded dump would be very large " +
			"and would itself load the server)")
		return
	}

	// Say why the two local collectors are off, so their absence from the
	// bundle reads as a deliberate choice rather than a silent failure.
	// resolveLocalCollector already made the decision; this only reports it.
	if skipHostInfo && skipLogs && !dryRun {
		switch mode {
		case "gov":
			fmt.Println("Gov mode: not collecting host facts or server log files " +
				"(hostnames, mount paths, process command lines and log bodies cannot be hashed).")
		case "cloud":
			fmt.Println("Cloud mode: not collecting host facts or server log files — they would " +
				"describe this machine, not the managed service. Pass --host-info=on / --logs=on " +
				"to override when running on a self-managed node.")
		}
	}

	// Dry-run: activate AFTER the version probe so version detection
	// still works, but BEFORE everything else. resolveAnalysisOpts
	// uses ExecuteQueryReal for its pre-flight lookups, which bypasses
	// the dry-run intercept — that way the derived query_id / hash
	// flow into the printed SQL instead of leaving `{query_id}`
	// markers unbound.
	if dryRun {
		fmt.Println("\n=== DRY RUN ===")
		fmt.Println("No data will be written to disk.")
		fmt.Println("No archive will be created.")
		fmt.Println("Each SELECT is printed with the system tables it would touch and an")
		fmt.Println("EXPLAIN ESTIMATE block (read-only metadata, no data parts scanned).")
		fmt.Println("Empty estimates render as `this table is empty` — that's the planner")
		fmt.Println("confirming nothing matches the query predicate.")
		fmt.Println()
		// EXPLAIN ESTIMATE is always on under --dry-run — one flag,
		// one behavior. The pre-flight queries (PreflightForX) use
		// ExecuteQueryReal to bypass the interception, so derived
		// values flow into the printed SQL.
		client.SetDryRun(os.Stdout, true)
		tmpDir, err := os.MkdirTemp("", "diag-dryrun-*")
		if err != nil {
			fmt.Printf("Error creating dry-run temp dir: %v\n", err)
			return
		}
		defer os.RemoveAll(tmpDir)
		outputDir = tmpDir
	}

	// Resolve query-analysis options up front so an invalid --query-id
	// or --normalized-query-hash fails fast, before any heavy work runs.
	analysisOpts, err := resolveAnalysisOpts(client, mode, queryID, normalizedHash, fromStr, toStr)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Gov mode requires a customer-supplied salt so hashes in the
	// support-bound artifacts cannot be reversed by a public rainbow
	// table. Prompt for it now (with no echo) if it wasn't passed via
	// --salt. Salt never leaves the operator's machine.
	//
	// In dry-run we skip the prompt and use a placeholder so the
	// printed `.sql` files still show what would substitute; nothing
	// is written and no archive is built, so the placeholder cannot
	// leak.
	if mode == "gov" && !dryRun {
		if govSalt == "" {
			s, err := promptForGovSalt()
			if err != nil {
				fmt.Printf("Error reading gov-mode salt: %v\n", err)
				return
			}
			govSalt = s
		}
		if err := internal.ValidateGovSalt(govSalt); err != nil {
			fmt.Printf("Invalid gov-mode salt: %v\n", err)
			return
		}
	} else if mode == "gov" && dryRun && govSalt == "" {
		govSalt = "DRYRUNPLACEHOLDER"
	}

	if !collectFrom.IsZero() || !collectTo.IsZero() {
		fmt.Printf("Collection window: %s\n", describeWindow(collectFrom, collectTo))
	}

	// Find and execute queries - get the specific folder path
	queryManager := query.NewManager().WithOutputFormat(outputFormat).
		WithWindow(collectFrom, collectTo)
	finalOutputDir, err := queryManager.ExecuteQueries(client, queriesDir, serverVersion, outputDir, govSalt)
	if err != nil {
		fmt.Printf("Error executing queries: %v\n", err)
		return
	}

	// Time-bounded system.text_log slice (opt-in, --from/--to required).
	// Runs before the dry-run summary so --dry-run previews the SQL.
	if collectTextLog {
		tlOpts := query.TextLogOpts{
			From:     analysisOpts.From,
			To:       analysisOpts.To,
			Level:    textLogLevel,
			RowLimit: textLogLimit,
		}
		// analysisOpts only carries a window when query analysis is active;
		// otherwise parse the flags directly.
		if tlOpts.From.IsZero() || tlOpts.To.IsZero() {
			f, ferr := query.ParseTimeFlag(fromStr)
			t, terr := query.ParseTimeFlag(toStr)
			if ferr != nil || terr != nil {
				fmt.Printf("Error parsing --from/--to: %v %v\n", ferr, terr)
				return
			}
			tlOpts.From, tlOpts.To = f, t
		}
		tlColl := query.NewTextLogCollector(client, mode).WithOutputFormat(outputFormat)
		path, err := tlColl.Collect(tlOpts, finalOutputDir, serverVersion)
		if err != nil {
			fmt.Printf("Warning: text_log collection failed: %v\n", err)
		} else if path != "" {
			fmt.Printf("  text_log written to %s\n", path)
		}
	}

	// Query analysis bundle (only when --query-id or
	// --normalized-query-hash was set). Runs the .sql files under
	// analysisDir against the focus parameters and writes results into
	// <finalOutputDir>/query_analysis/. Runs BEFORE the dry-run summary
	// so --dry-run previews the analysis SQL too (Collect's IsDryRun
	// handling prints each file instead of writing results) — otherwise
	// "list every query the tool would execute" would omit the bundle.
	if analysisOpts.Enabled() {
		coll := query.NewAnalysisCollector(client, mode).WithOutputFormat(outputFormat)
		if _, _, err := coll.Collect(analysisOpts, analysisDir, finalOutputDir, serverVersion); err != nil {
			fmt.Printf("Warning: query analysis failed: %v\n", err)
		}
	}

	if dryRun {
		fmt.Println("\n=== DRY RUN SUMMARY ===")
		fmt.Println("Above are the queries that would be executed.")
		fmt.Println("To run for real, re-invoke without --dry-run.")
		return
	}
	// Gov mode: print + save a local mapping from real database/table
	// names to the hex(SHA256(name+salt)) form that appears in the
	// support-bound output files. Saved outside the archive folder.
	// Skipped in dry-run — the mapping is a local artefact, no point
	// producing it (the SQL itself is still printed via the
	// intercepting client when the function runs).
	if mode == "gov" && !dryRun {
		if err := internal.PrintGovNameMapping(client, outputDir, finalOutputDir, govSalt); err != nil {
			fmt.Printf("Warning: gov-mode name mapping failed: %v\n", err)
		}
	}

	// Host facts: the OS/kernel/hardware context that explains what the
	// ClickHouse system tables report. Read from /proc, /sys and /etc — so
	// it only yields anything when run ON the server, and degrades to
	// "unavailable" with a note otherwise rather than failing the run.
	if !skipHostInfo {
		fmt.Println("Collecting host OS and hardware facts...")
		report := hostinfo.Collect()
		if path, err := hostinfo.WriteJSON(finalOutputDir, report); err != nil {
			fmt.Printf("Warning: host info could not be written: %v\n", err)
		} else {
			fmt.Print(report.Summary())
			for _, n := range report.Notes {
				fmt.Printf("  note: %s\n", n)
			}
			fmt.Printf("  written to %s\n", filepath.Base(path))
		}
	}

	// ClickHouse server log files from disk. The directory is discovered
	// from the server configuration when not given explicitly, because
	// operators relocate logs far more often than anything else.
	if !skipLogs {
		fmt.Println("Collecting ClickHouse server log files...")
		res, err := logfiles.Collect(finalOutputDir, logfiles.Options{
			Dir:             logsDir,
			ConfigDir:       configDir,
			IncludeArchives: logsArchives,
			MaxBytesPerFile: int64(logsMaxMB) << 20,
		})
		if err != nil {
			fmt.Printf("Warning: log collection failed: %v\n", err)
		} else {
			fmt.Print(res.Summary())
		}
	}

	// Evaluate alert rules if not skipped
	var alertResults []alert.Result
	if !skipAlerts {
		fmt.Println("Evaluating alert rules...")
		alertResults = alert.NewEvaluator(client, mode).RunAll(alertsDir, serverVersion)
		// "checked" counts only rules that produced an answer: both skipped
		// (table not present here) and errored (query failed) rules are
		// excluded, because reporting either as checked would imply a
		// verification that never happened. See alert.Summarize.
		evaluated, fired, errored, skipped := alert.Summarize(alertResults)
		fmt.Printf("Alert evaluation complete: %d rule(s) checked, %d fired, %d errored, %d not applicable\n",
			evaluated, fired, errored, skipped)
		if errored > 0 {
			fmt.Printf("  (%d rule(s) could not run — see the [alert] ERROR lines above; "+
				"these are NOT findings)\n", errored)
		}
	}

	// Generate HTML dashboard if not skipped.
	//
	// Not produced in gov mode: dashboard.html lands in the same archive as
	// the hashed .native files, but its queries are built in Go and select
	// raw identifiers (database/table names for up to 2000 tables, disk
	// paths, users, plus server-generated text like last_exception and
	// last_error_message) — the very values the queries.gov/*.sql files
	// hash. Shipping it would defeat gov-mode hashing, so it is refused for
	// the same reason query analysis is. Hashing every dashboard panel is
	// the follow-up that would restore it.
	dashboardWritten := false
	switch {
	// An explicit --skip-dashboard is reported first: a user who asked to
	// skip it should be told their flag was honoured, not that gov policy
	// withheld it. Same outcome either way (dashboardWritten stays false).
	case skipDashboard:
		fmt.Println("Skipping HTML dashboard (--skip-dashboard).")
	case mode == "gov":
		fmt.Println("Skipping HTML dashboard in gov mode: its panels select raw identifiers " +
			"(table names, disk paths, users, exception text) that cannot be hashed, and the " +
			"dashboard is part of the support-bound archive.")
	default:
		gen := dashboard.NewGenerator(client, mode).WithServerVersion(serverVersion).WithAnalysis(analysisOpts, analysisDir)
		if err := gen.Generate(finalOutputDir, alertResults); err != nil {
			fmt.Printf("Warning: dashboard generation failed: %v\n", err)
		} else {
			dashboardWritten = true
		}
	}

	// The dashboard is the only OTHER consumer of alertResults, so whenever it
	// isn't produced — gov mode, --skip-dashboard, or a generation failure —
	// the archive would carry no alert record at all: the rules ran, a
	// critical one may have fired, and the artifact the customer ships back
	// says nothing. Write rule-level metadata instead: name/title/severity/
	// state and the instance COUNT, never the matched rows (their columns are
	// rule-defined, so in gov mode they would carry raw identifiers).
	if !skipAlerts && !dashboardWritten && len(alertResults) > 0 {
		if err := alert.WriteSummaryJSON(finalOutputDir, alertResults, mode); err != nil {
			fmt.Printf("Warning: alert summary could not be written: %v\n", err)
		} else {
			fmt.Println("Wrote alerts_summary.json (rule outcomes and instance counts; " +
				"matched rows are never included).")
		}
	}

	// Create archive if not skipped - use the specific folder that was created
	if !skipArchive {
		if err := createArchiveWithTimestamp(finalOutputDir); err != nil {
			fmt.Printf("Error creating archive: %v\n", err)
			return
		}
	}
}

func getUserInput(protocol, host, port, username, password, mode, configDir *string, skipConfig bool) error {
	reader := bufio.NewReader(os.Stdin)

	// Get protocol if not provided
	if *protocol == "" {
		fmt.Print("Select protocol (http/https) [default: http]: ")
		input, _ := reader.ReadString('\n')
		*protocol = strings.TrimSpace(input)
		if *protocol == "" {
			*protocol = "http"
		}
	}
	// Validate protocol value
	*protocol = strings.ToLower(*protocol)
	if *protocol != "http" && *protocol != "https" {
		fmt.Printf("Invalid protocol '%s'. Using http instead.\n", *protocol)
		*protocol = "http"
	}

	// Get host if not provided
	if *host == "" {
		fmt.Print("Enter ClickHouse Host [default: localhost]: ")
		input, _ := reader.ReadString('\n')
		*host = strings.TrimSpace(input)
		if *host == "" {
			*host = "localhost"
		}
	}

	// Get port if not provided
	if *port == "" {
		defaultPort := "8123"
		if *protocol == "https" {
			defaultPort = "8443"
		}
		fmt.Printf("Enter ClickHouse Port [default: %s]: ", defaultPort)
		input, _ := reader.ReadString('\n')
		*port = strings.TrimSpace(input)
		if *port == "" {
			*port = defaultPort
		}
	}

	// Get username if not provided
	if *username == "" {
		fmt.Print("Enter Username: ")
		input, _ := reader.ReadString('\n')
		*username = strings.TrimSpace(input)
	}

	// Get password if not provided
	if *password == "" && *username != "" {
		fmt.Print("Enter Password: ")
		passwordBytes, _ := term.ReadPassword(int(syscall.Stdin))
		*password = string(passwordBytes)
		fmt.Println() // Add a newline after password input
	}

	// Get mode if not provided or validate provided mode
	validModes := []string{"cloud", "onprem", "gov"}
	*mode = strings.ToLower(*mode)
	if !isValidMode(*mode) {
		fmt.Printf("Select query mode (cloud/onprem/gov) [default: onprem]: ")
		input, _ := reader.ReadString('\n')
		*mode = strings.TrimSpace(strings.ToLower(input))
		if *mode == "" {
			*mode = "onprem"
		}
		if !isValidMode(*mode) {
			fmt.Printf("Invalid mode '%s'. Using onprem instead.\n", *mode)
			*mode = "onprem"
		}
	}

	// Display available modes for user reference
	fmt.Printf("Available modes: %s\n", strings.Join(validModes, ", "))

	// Get config directory if not provided and not skipping config collection
	if *configDir == "" && !skipConfig {
		fmt.Print("Enter ClickHouse config directory to collect [default: /etc/clickhouse-server/config.d/]: ")
		input, _ := reader.ReadString('\n')
		*configDir = strings.TrimSpace(input)
		if *configDir == "" {
			*configDir = "/etc/clickhouse-server/config.d/"
		}
	}

	return nil
}

// resolveAnalysisOpts builds the query-analysis options from the raw
// CLI flag strings. When --query-id is set without
// --normalized-query-hash, a pre-flight SELECT against system.query_log
// derives the hash and (when found) the query's event_time — the
// default time window is then centred on that event_time instead of
// "last N days from now," so a slow query from a week ago isn't missed
// by a present-time default.
func resolveAnalysisOpts(client *pkg.ClickHouseClient, mode, queryID, normalizedHash, fromStr, toStr string) (query.AnalysisOpts, error) {
	opts := query.AnalysisOpts{
		QueryID:             queryID,
		NormalizedQueryHash: normalizedHash,
	}
	if !opts.Enabled() {
		return opts, nil // neither set; analysis stays off, validation skipped
	}

	from, err := query.ParseTimeFlag(fromStr)
	if err != nil {
		return opts, fmt.Errorf("--from: %w", err)
	}
	to, err := query.ParseTimeFlag(toStr)
	if err != nil {
		return opts, fmt.Errorf("--to: %w", err)
	}
	opts.From, opts.To = from, to

	// Pre-flight: when only query_id is set, derive the hash and the
	// query's event_time. Skipped when the user already supplied the
	// hash or when no query_id was given.
	if opts.QueryID != "" && opts.NormalizedQueryHash == "" {
		hash, eventTime, err := query.PreflightForQueryID(client, mode, opts.QueryID)
		if err != nil {
			return opts, err
		}
		opts.NormalizedQueryHash = hash
		fmt.Printf("Pre-flight: query_id %s → normalized_query_hash %s (event_time %s)\n",
			opts.QueryID, hash, eventTime.Format(time.RFC3339))
		// If the user didn't pin --from/--to, center the default 3-day
		// window on the derived event_time so the slow execution is
		// definitely inside the analysis window.
		if opts.From.IsZero() && opts.To.IsZero() && !eventTime.IsZero() {
			opts.From = eventTime.Add(-36 * time.Hour)
			opts.To = eventTime.Add(12 * time.Hour)
		}
	}

	// Validate runs the default-window logic for whatever fields are
	// still empty. We need a populated window before the reverse
	// pre-flight (hash → slowest query_id) can run.
	if err := opts.Validate(time.Now().UTC()); err != nil {
		return opts, err
	}

	// Reverse pre-flight: when only --normalized-query-hash is set,
	// pick the slowest finished execution as the representative
	// query_id so the single-id queries (ProfileEvents, text_log,
	// tables-referenced …) have something to filter on. Without
	// this, hash-only mode shipped a dashboard with most cards
	// empty (the user's main complaint).
	if opts.NormalizedQueryHash != "" && opts.QueryID == "" {
		qid, eventTime, err := query.PreflightForHash(client, mode, opts.NormalizedQueryHash, opts.From, opts.To)
		if err != nil {
			return opts, err
		}
		if qid == "" {
			fmt.Printf("Pre-flight: no QueryFinish row found for normalized_query_hash %s in the window — single-id files will be skipped\n",
				opts.NormalizedQueryHash)
		} else {
			opts.QueryID = qid
			fmt.Printf("Pre-flight: normalized_query_hash %s → slowest query_id %s (event_time %s)\n",
				opts.NormalizedQueryHash, qid, eventTime.Format(time.RFC3339))
		}
	}
	return opts, nil
}

// promptForGovSalt asks the operator for the gov-mode salt without
// echoing it to the terminal. The salt is the customer's local secret
// — keeping it off the screen avoids accidental capture in screen-share
// recordings, ticket attachments, or terminal scrollback.
func promptForGovSalt() (string, error) {
	fmt.Println()
	fmt.Println("Gov mode: enter a salt used to hash database / table names in the output.")
	fmt.Println("  - 8–64 alphanumeric characters (A–Z, a–z, 0–9)")
	fmt.Println("  - Keep this value local; do NOT share it with ClickHouse support.")
	fmt.Println("  - Use the same salt across runs if you want hashes to be comparable.")
	fmt.Print("Salt: ")
	saltBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(saltBytes)), nil
}

// getQueriesDir maps the mode to the corresponding queries directory
func getQueriesDir(mode string) string {
	switch strings.ToLower(mode) {
	case "cloud":
		return "./queries.cloud"
	case "onprem":
		return "./queries.onprem"
	case "gov":
		return "./queries.gov"
	default:
		return "./queries.onprem" // fallback to onprem
	}
}

// isValidMode checks if the provided mode is valid
func isValidMode(mode string) bool {
	validModes := []string{"cloud", "onprem", "gov"}
	for _, validMode := range validModes {
		if mode == validMode {
			return true
		}
	}
	return false
}

// createArchiveWithTimestamp creates an archive of the specific results directory
func createArchiveWithTimestamp(specificDir string) error {
	// Extract timestamp from the directory name for archive naming
	dirName := filepath.Base(specificDir)

	// Generate archive name based on the directory name
	archiveName := fmt.Sprintf("%s.tar.gz", dirName)

	// Ensure archive name has .tar.gz extension
	if !strings.HasSuffix(archiveName, ".tar.gz") {
		archiveName += ".tar.gz"
	}

	// Create the archive with the specific results directory and configuration
	return internal.CreateArchive(archiveName, specificDir, "./configuration")
}

// resolveLocalCollector decides whether to run one of the two collectors
// that read the LOCAL filesystem (host facts, server log files), returning
// skip=true when it should not run.
//
// The default is mode-dependent because these collectors describe the
// machine the tool executes on:
//
//   - onprem — that machine IS the ClickHouse server, so collect (on).
//   - cloud  — ClickHouse Cloud is managed and reached over the network, so
//     the local /proc and /var/log describe the operator's laptop or
//     bastion. Collecting them would put confidently wrong data in a
//     support archive, which is worse than collecting nothing (off).
//   - gov    — never, at any setting: hostnames, mount paths, process
//     command lines and log bodies are exactly what gov hashing protects,
//     and none of them can be hashed while staying useful.
//
// "on" forces collection anyway (e.g. cloud mode against a self-managed
// node), except in gov where the refusal is absolute.
func resolveLocalCollector(setting, mode, name string) (skip bool, err error) {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "off":
		return true, nil
	case "on":
		if mode == "gov" {
			return true, fmt.Errorf("--%s=on is not allowed in gov mode "+
				"(host facts and log bodies contain identifiers that cannot be hashed)", name)
		}
		if mode == "cloud" {
			fmt.Printf("Warning: --%s=on in cloud mode collects facts about the machine "+
				"running this tool, not the managed ClickHouse nodes.\n", name)
		}
		return false, nil
	case "auto", "":
		return mode != "onprem", nil
	default:
		return true, fmt.Errorf("--%s must be auto, on or off (got %q)", name, setting)
	}
}

// describeWindow renders the resolved --from / --to window for the
// operator, spelling out which end fell back to the query's own default
// so a half-specified window is not mistaken for a full one.
func describeWindow(from, to time.Time) string {
	const layout = "2006-01-02 15:04:05 UTC"
	switch {
	case from.IsZero():
		return "each query's own default start → " + to.UTC().Format(layout)
	case to.IsZero():
		return from.UTC().Format(layout) + " → now"
	default:
		return from.UTC().Format(layout) + " → " + to.UTC().Format(layout)
	}
}
