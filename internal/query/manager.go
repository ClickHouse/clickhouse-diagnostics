package query

import (
	"fmt"
	"time"

	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/pkg"
)

// Manager orchestrates query finding, selection, and execution
type Manager struct {
	finder   *Finder
	selector *Selector
	format   OutputFormat
	from     time.Time
	to       time.Time
	mode     string
}

// WithMode sets the topology mode used for {sys.<table>} expansion.
func (m *Manager) WithMode(mode string) *Manager {
	m.mode = mode
	return m
}

// WithWindow overrides the time window of every query that declares one.
func (m *Manager) WithWindow(from, to time.Time) *Manager {
	m.from, m.to = from, to
	return m
}

// WithOutputFormat sets the serialisation format for query results.
func (m *Manager) WithOutputFormat(f OutputFormat) *Manager {
	m.format = f
	return m
}

// NewManager creates a new query manager
func NewManager() *Manager {
	return &Manager{
		finder:   NewFinder(),
		selector: NewSelector(),
		format:   DefaultOutputFormat,
	}
}

// ExecuteQueries finds, selects, and executes queries for the given ClickHouse instance.
// salt, when non-empty (gov mode), is substituted into the '%salt%' placeholder
// of .sql files just before they are sent to ClickHouse.
// Returns the path to the specific folder where results were saved.
func (m *Manager) ExecuteQueries(client *pkg.ClickHouseClient, queriesDir string, serverVersion internal.Version, outputDir, salt string) (string, error) {
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
	executor := NewExecutor(client).WithSalt(salt).WithOutputFormat(m.format).
		WithWindow(m.from, m.to).WithMode(m.mode)
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
