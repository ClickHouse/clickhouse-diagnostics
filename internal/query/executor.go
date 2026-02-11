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
}

// NewExecutor creates a new query executor
func NewExecutor(client *pkg.ClickHouseClient) *Executor {
	return &Executor{
		client: client,
	}
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

	fmt.Printf("Executing %d unique queries...\n\n", len(queries))

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

	fmt.Printf("\nQuery execution completed: %d successful, %d failed\n", successCount, errorCount)
	fmt.Printf("Results saved to: %s\n", finalOutputDir)

	return finalOutputDir, nil
}

// executeQuery executes a single query and saves the result
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

	// Show source information
	if query.DirName != "" {
		fmt.Printf("Executing query '%s' from directory '%s'...\n", query.Name, query.DirName)
	} else {
		fmt.Printf("Executing query '%s' from root directory...\n", query.Name)
	}

	// Execute the query
	result, err := e.client.ExecuteQuery(string(queryContent))
	if err != nil {
		return fmt.Errorf("error executing query: %w", err)
	}

	// Generate output filename
	baseFileName := strings.TrimSuffix(query.Name, filepath.Ext(query.Name))
	outputFileName := fmt.Sprintf("%s_%s.native", baseFileName, timestamp)
	outputPath := filepath.Join(outputDir, outputFileName)

	// Save the result to a file
	if err := os.WriteFile(outputPath, []byte(result), 0600); err != nil {
		return fmt.Errorf("error saving result: %w", err)
	}

	fmt.Printf("Query '%s' executed successfully. Result saved to %s\n\n", query.Name, outputPath)
	return nil
}

// ValidateQuery performs basic validation on a query before execution
func (e *Executor) ValidateQuery(queryPath string) error {
	content, err := os.ReadFile(queryPath)
	if err != nil {
		return fmt.Errorf("cannot read query file: %w", err)
	}

	if len(content) == 0 {
		return fmt.Errorf("query file is empty")
	}

	// Basic SQL validation - check for potentially dangerous commands
	queryStr := strings.ToLower(strings.TrimSpace(string(content)))
	dangerousCommands := []string{"drop", "delete", "truncate", "alter", "create", "insert", "update"}

	for _, cmd := range dangerousCommands {
		if strings.HasPrefix(queryStr, cmd) {
			return fmt.Errorf("potentially dangerous query detected: starts with %s", cmd)
		}
	}

	return nil
}
