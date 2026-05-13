package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
	modeFlag := flag.String("mode", "onprem", "Query mode (cloud, onprem, gouv)")
	outputDirFlag := flag.String("output-dir", "./clickhouse_results", "Directory for results output")
	configDirFlag := flag.String("config-dir", "", "ClickHouse config directory to collect (default: /etc/clickhouse-server/config.d/)")
	skipConfigFlag    := flag.Bool("skip-config", false, "Skip collecting configuration files")
	skipArchiveFlag   := flag.Bool("skip-archive", false, "Skip creating archive of results and configuration")
	skipDashboardFlag := flag.Bool("skip-dashboard", false, "Skip generating HTML dashboard")
	skipAlertsFlag    := flag.Bool("skip-alerts", false, "Skip evaluating alert rules")
	alertsDirFlag     := flag.String("alerts-dir", "./alerts", "Directory containing alert YAML rule files")

	// Parse command line flags
	flag.Parse()

	// Initialize variables with defaults
	var (
		host        = *hostFlag
		port        = *portFlag
		username    = *userFlag
		password    = *passwordFlag
		protocol    = *protocolFlag
		mode        = *modeFlag
		outputDir   = *outputDirFlag
		configDir   = *configDirFlag
		skipConfig    = *skipConfigFlag
		skipArchive   = *skipArchiveFlag
		skipDashboard = *skipDashboardFlag
		skipAlerts    = *skipAlertsFlag
		alertsDir     = *alertsDirFlag
	)

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

	// Find and execute queries - get the specific folder path
	queryManager := query.NewManager()
	finalOutputDir, err := queryManager.ExecuteQueries(client, queriesDir, serverVersion, outputDir)
	if err != nil {
		fmt.Printf("Error executing queries: %v\n", err)
		return
	}

	// Evaluate alert rules if not skipped
	var alertResults []alert.Result
	if !skipAlerts {
		fmt.Println("Evaluating alert rules...")
		alertResults = alert.NewEvaluator(client, mode).RunAll(alertsDir)
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
		gen := dashboard.NewGenerator(client, mode)
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
	validModes := []string{"cloud", "onprem", "gouv"}
	*mode = strings.ToLower(*mode)
	if !isValidMode(*mode) {
		fmt.Printf("Select query mode (cloud/onprem/gouv) [default: onprem]: ")
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

// getQueriesDir maps the mode to the corresponding queries directory
func getQueriesDir(mode string) string {
	switch strings.ToLower(mode) {
	case "cloud":
		return "./queries.cloud"
	case "onprem":
		return "./queries.onprem"
	case "gouv":
		return "./queries.gouv"
	default:
		return "./queries.onprem" // fallback to onprem
	}
}

// isValidMode checks if the provided mode is valid
func isValidMode(mode string) bool {
	validModes := []string{"cloud", "onprem", "gouv"}
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
