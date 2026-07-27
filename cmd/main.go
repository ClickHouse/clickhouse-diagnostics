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
	fromFlag := flag.String("from", "", "Time-window start for query analysis (RFC3339 or YYYY-MM-DD)")
	toFlag := flag.String("to", "", "Time-window end for query analysis (RFC3339 or YYYY-MM-DD)")
	analysisDirFlag := flag.String("analysis-dir", "./queries.query_analysis", "Directory containing query-analysis SQL files")
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
	)
	// --dry-run is read-only by definition — silence the side effects
	// that would write empty/garbage artefacts to disk.
	if dryRun {
		skipConfig = true
		skipArchive = true
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

	// Find and execute queries - get the specific folder path
	queryManager := query.NewManager()
	finalOutputDir, err := queryManager.ExecuteQueries(client, queriesDir, serverVersion, outputDir, govSalt)
	if err != nil {
		fmt.Printf("Error executing queries: %v\n", err)
		return
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

	// Query analysis bundle (only when --query-id or
	// --normalized-query-hash was set). Runs the .sql files under
	// analysisDir against the focus parameters and writes results into
	// <finalOutputDir>/query_analysis/.
	if analysisOpts.Enabled() {
		coll := query.NewAnalysisCollector(client, mode)
		if _, _, err := coll.Collect(analysisOpts, analysisDir, finalOutputDir, serverVersion); err != nil {
			fmt.Printf("Warning: query analysis failed: %v\n", err)
		}
	}

	// Evaluate alert rules if not skipped
	var alertResults []alert.Result
	if !skipAlerts {
		fmt.Println("Evaluating alert rules...")
		alertResults = alert.NewEvaluator(client, mode).RunAll(alertsDir, serverVersion)
		fired := 0
		for _, r := range alertResults {
			if r.Fired() {
				fired++
			}
		}
		fmt.Printf("Alert evaluation complete: %d rule(s) checked, %d fired\n", len(alertResults), fired)
	}

	// Generate HTML dashboard if not skipped
	if !skipDashboard {
		gen := dashboard.NewGenerator(client, mode).WithServerVersion(serverVersion).WithAnalysis(analysisOpts, analysisDir)
		if err := gen.Generate(finalOutputDir, alertResults); err != nil {
			fmt.Printf("Warning: dashboard generation failed: %v\n", err)
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
