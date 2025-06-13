package query

import (
	"fmt"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/pkg"
)

// Manager orchestrates query finding, selection, and execution
type Manager struct {
	finder   *Finder
	selector *Selector
}

// NewManager creates a new query manager
func NewManager() *Manager {
	return &Manager{
		finder:   NewFinder(),
		selector: NewSelector(),
	}
}

// ExecuteQueries finds, selects, and executes queries for the given ClickHouse instance
// Returns the path to the specific folder where results were saved
func (m *Manager) ExecuteQueries(client *pkg.ClickHouseClient, queriesDir string, serverVersion internal.Version, outputDir string) (string, error) {
	// Validate query directory
	if err := m.finder.ValidateQueryDirectory(queriesDir); err != nil {
		return "", fmt.Errorf("query directory validation failed: %w", err)
	}

	// Find all compatible queries
	allQueries, err := m.finder.FindCompatibleQueries(queriesDir, serverVersion)
	if err != nil {
		return "", fmt.Errorf("error finding query files: %w", err)
	}

	if len(allQueries) == 0 {
		return "", fmt.Errorf("no suitable query files found in '%s' or its subdirectories", queriesDir)
	}

	// Select highest priority queries
	selectedQueries := m.selector.SelectHighestPriorityQueries(allQueries)

	fmt.Printf("Found %d unique query files to execute\n", len(selectedQueries))

	// Execute the selected queries and get the specific output directory
	executor := NewExecutor(client)
	finalOutputDir, err := executor.ExecuteQueries(selectedQueries, outputDir)
	if err != nil {
		return "", fmt.Errorf("error executing queries: %w", err)
	}

	return finalOutputDir, nil
}

// GetQuerySummary returns a summary of available queries
func (m *Manager) GetQuerySummary(queriesDir string, serverVersion internal.Version) (map[string]int, error) {
	allQueries, err := m.finder.FindCompatibleQueries(queriesDir, serverVersion)
	if err != nil {
		return nil, err
	}

	selectedQueries := m.selector.SelectHighestPriorityQueries(allQueries)
	groups := m.selector.GroupQueriesByType(selectedQueries)

	summary := make(map[string]int)
	for group, queries := range groups {
		summary[group] = len(queries)
	}

	return summary, nil
}
