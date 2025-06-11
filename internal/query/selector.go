package query

import (
	"clickhouse-diagnostic/internal"
	"clickhouse-diagnostic/internal/version"
)

// Selector handles query selection and prioritization
type Selector struct {
	parser   *version.Parser
	comparer *version.Comparer
}

// NewSelector creates a new query selector
func NewSelector() *Selector {
	return &Selector{
		parser:   version.NewParser(),
		comparer: version.NewComparer(),
	}
}

// SelectHighestPriorityQueries selects queries with highest priority (largest folder name)
func (s *Selector) SelectHighestPriorityQueries(allQueries []internal.QueryFile) map[string]internal.QueryFile {
	// Map to store the best query for each unique filename
	bestQueries := make(map[string]internal.QueryFile)

	for _, query := range allQueries {
		// Check if we've already seen this filename
		existing, exists := bestQueries[query.Name]

		if !exists {
			// First time seeing this filename
			bestQueries[query.Name] = query
		} else {
			// Determine which query has higher priority
			if s.hasHigherPriority(query, existing) {
				bestQueries[query.Name] = query
			}
		}
	}

	return bestQueries
}

// hasHigherPriority determines if query1 has higher priority than query2
func (s *Selector) hasHigherPriority(query1, query2 internal.QueryFile) bool {
	// If one is from root and one is from a version directory, prefer the versioned one
	if query2.DirName == "" && query1.DirName != "" {
		return true
	}
	if query1.DirName == "" && query2.DirName != "" {
		return false
	}

	// Both are either from root or from versioned directories
	if query1.DirName == "" && query2.DirName == "" {
		// Both from root, keep the existing one (arbitrary choice)
		return false
	}

	// Both from versioned directories, compare versions
	version1, err1 := s.parser.ParseVersionFromDirName(query1.DirName)
	version2, err2 := s.parser.ParseVersionFromDirName(query2.DirName)

	// If we can't parse versions, prefer the first one
	if err1 != nil || err2 != nil {
		return false
	}

	// Choose the one with the highest version
	return s.comparer.IsGreater(version1, version2)
}

// GroupQueriesByType groups queries by their type or category
func (s *Selector) GroupQueriesByType(queries map[string]internal.QueryFile) map[string][]internal.QueryFile {
	groups := make(map[string][]internal.QueryFile)

	for _, query := range queries {
		var group string
		if query.DirName == "" {
			group = "root"
		} else {
			group = query.DirName
		}

		groups[group] = append(groups[group], query)
	}

	return groups
}
