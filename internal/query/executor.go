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

// ExecuteQueries executes a map of selected queries and saves results
// Returns the path to the specific folder where results were saved
func (e *Executor) ExecuteQueries(queries map[string]internal.QueryFile, outputDir string) (string, error) {
	timestamp := time.Now().Format("20060102_150405")
	outputFolderName := fmt.Sprintf("clickhouse_backup_%s", timestamp)
	finalOutputDir := filepath.Join(outputDir, outputFolderName)

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

	// Execute the selected queries
	for _, query := range queries {
		if err := e.executeQuery(query, finalOutputDir, timestamp); err != nil {
			fmt.Printf("Error executing query '%s': %v\n", query.Name, err)
			errorCount++
		} else {
			successCount++
		}
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
		return fmt.Errorf("error reading query file: %w", err)
	}

	if len(queryContent) == 0 {
		fmt.Printf("Query file '%s' is empty, skipping\n", query.FullPath)
		return nil
	}

	// Gov mode: replace the public '%salt%' placeholder in .sql files
	// with the customer-supplied salt. Salt format is validated upstream
	// (alphanumeric only), so it cannot break out of the SQL string literal.
	sqlText := string(queryContent)
	if e.salt != "" {
		sqlText = strings.ReplaceAll(sqlText, "'%salt%'", "'"+e.salt+"'")
	}

	// Serialise in the configured format, replacing any FORMAT the file
	// carries. Done before validation so the validator sees exactly the
	// text that will be sent to the server.
	sqlText = e.outputFormat().Apply(sqlText)

	// Security: enforce read-only SELECT before execution.
	if err := ValidateQueryContent(sqlText); err != nil {
		return fmt.Errorf("security validation failed for '%s': %w", query.Name, err)
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

	// Execute the query
	result, err := e.client.ExecuteQuery(sqlText)
	if err != nil {
		return fmt.Errorf("error executing query: %w", err)
	}

	// Generate output filename
	baseFileName := strings.TrimSuffix(query.Name, filepath.Ext(query.Name))
	outputFileName := fmt.Sprintf("%s_%s%s", baseFileName, timestamp, e.outputFormat().Ext)
	outputPath := filepath.Join(outputDir, outputFileName)

	// Save the result to a file
	if err := os.WriteFile(outputPath, []byte(result), 0600); err != nil {
		return fmt.Errorf("error saving result: %w", err)
	}

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
