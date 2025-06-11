package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/config"
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
	queriesDirFlag := flag.String("queries-dir", "./queries", "Directory containing query files")
	outputDirFlag := flag.String("output-dir", "./clickhouse_results", "Directory for results output")
	configDirFlag := flag.String("config-dir", "", "ClickHouse config directory to collect (default: /etc/clickhouse-server/config.d/)")
	skipConfigFlag := flag.Bool("skip-config", false, "Skip collecting configuration files")
	skipArchiveFlag := flag.Bool("skip-archive", false, "Skip creating archive of results and configuration")

	// Parse command line flags
	flag.Parse()

	// Initialize variables with defaults
	var (
		host        = *hostFlag
		port        = *portFlag
		username    = *userFlag
		password    = *passwordFlag
		protocol    = *protocolFlag
		queriesDir  = *queriesDirFlag
		outputDir   = *outputDirFlag
		configDir   = *configDirFlag
		skipConfig  = *skipConfigFlag
		skipArchive = *skipArchiveFlag
	)

	// Get user input for missing parameters
	if err := getUserInput(&protocol, &host, &port, &username, &password, &configDir, skipConfig); err != nil {
		fmt.Printf("Error getting user input: %v\n", err)
		return
	}

	// Create the output folder if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
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

	// Find and execute queries
	queryManager := query.NewManager()
	if err := queryManager.ExecuteQueries(client, queriesDir, serverVersion, outputDir); err != nil {
		fmt.Printf("Error executing queries: %v\n", err)
		return
	}

	// Create archive if not skipped
	if !skipArchive {
		if err := createArchiveWithTimestamp(outputDir); err != nil {
			fmt.Printf("Error creating archive: %v\n", err)
			return
		}
	}
}

func getUserInput(protocol, host, port, username, password, configDir *string, skipConfig bool) error {
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

func createArchiveWithTimestamp(outputDir string) error {
	// Generate archive name if not provided
	timestamp := time.Now().Format("20060102_150405")
	archiveName := fmt.Sprintf("clickhouse_backup_%s.tar.gz", timestamp)

	// Ensure archive name has .tar.gz extension
	if !strings.HasSuffix(archiveName, ".tar.gz") {
		archiveName += ".tar.gz"
	}

	// Create the archive with results and configuration
	return internal.CreateArchive(archiveName, outputDir, "./configuration")
}
