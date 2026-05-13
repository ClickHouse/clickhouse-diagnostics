// Package alert provides a YAML-driven alerting system for ClickHouse diagnostics.
//
// Alert rules live in .yaml files (see the alerts/ directory at the repo root).
// Each rule defines a read-only SELECT query; if the query returns any rows the
// alert fires and those rows are surfaced in the HTML dashboard.
//
// YAML rule format
// ────────────────
//
//   name:        unique_snake_case_id
//   title:       "Human-readable title"
//   severity:    critical | warning | info        (default: warning)
//   description: |
//     Multi-line explanation of what the alert means
//     and what to do about it.
//   tags:
//     - mutations
//     - performance
//   query: |
//     SELECT database, table, mutation_id,
//            dateDiff('hour', create_time, now()) AS hours_running,
//            parts_to_do
//     FROM {sys.mutations}                 -- placeholder, see below
//     WHERE parts_to_do > 0
//       AND is_killed = 0
//       AND dateDiff('hour', create_time, now()) > 3
//     ORDER BY hours_running DESC
//   message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h"
//
// System-table placeholder
// ────────────────────────
//   Use {sys.<table>} in your query instead of a hard-coded table path.
//   The evaluator substitutes the right reference based on the run mode:
//
//     {sys.mutations}  →  system.mutations               (onprem / gov)
//                      →  clusterAllReplicas(default, system.mutations)  (cloud)
//
// Message template
// ────────────────
//   Use {column_name} in the message string; the evaluator substitutes the
//   column value from each result row.  One alert instance is created per row.
//
// Security
// ────────
//   All alert queries are validated by ValidateQueryContent before execution.
//   Only SELECT / WITH queries are accepted; INSERT, ALTER, DROP, etc. are
//   rejected with an error and the alert is skipped.
package alert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"clickhouse-diagnostic/internal/query"
	"clickhouse-diagnostic/pkg"
)

// Severity constants.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Definition is the schema for a single .yaml alert rule file.
type Definition struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Severity    string   `yaml:"severity"`
	Description string   `yaml:"description"`
	Query       string   `yaml:"query"`
	Message     string   `yaml:"message"`
	Tags        []string `yaml:"tags"`
}

// Result holds the outcome of evaluating one alert rule.
// It is JSON-serialisable so it can be embedded in the dashboard payload.
type Result struct {
	Name        string                   `json:"name"`
	Title       string                   `json:"title"`
	Severity    string                   `json:"severity"`
	Description string                   `json:"description"`
	Message     string                   `json:"message"`
	Tags        []string                 `json:"tags"`
	Rows        []map[string]interface{} `json:"rows"`
	FiredAt     string                   `json:"fired_at"`
	Error       string                   `json:"error,omitempty"`
	File        string                   `json:"file"`
}

// Fired returns true when the alert triggered (rows > 0) or had a query error.
func (r Result) Fired() bool { return len(r.Rows) > 0 || r.Error != "" }

// FormattedMessages returns one message string per result row, with
// {column_name} placeholders substituted from that row's values.
func (r Result) FormattedMessages() []string {
	out := make([]string, 0, len(r.Rows))
	for _, row := range r.Rows {
		msg := r.Message
		for k, v := range row {
			msg = strings.ReplaceAll(msg, "{"+k+"}", fmt.Sprintf("%v", v))
		}
		out = append(out, msg)
	}
	return out
}

// Evaluator loads and runs alert rules against a ClickHouse instance.
type Evaluator struct {
	client *pkg.ClickHouseClient
	mode   string
}

// NewEvaluator creates a new Evaluator for the given connection and mode.
func NewEvaluator(client *pkg.ClickHouseClient, mode string) *Evaluator {
	return &Evaluator{client: client, mode: strings.ToLower(mode)}
}

// sysTable returns the mode-appropriate system table reference.
func (ev *Evaluator) sysTable(table string) string {
	if ev.mode == "cloud" {
		return fmt.Sprintf("clusterAllReplicas(default, system.%s)", table)
	}
	return "system." + table
}

// expandQuery replaces {sys.<name>} placeholders with the correct table path.
func (ev *Evaluator) expandQuery(sql string) string {
	out := sql
	for {
		start := strings.Index(out, "{sys.")
		if start == -1 {
			break
		}
		end := strings.Index(out[start:], "}")
		if end == -1 {
			break
		}
		placeholder := out[start : start+end+1] // e.g. "{sys.mutations}"
		tableName := out[start+5 : start+end]   // e.g. "mutations"
		out = strings.ReplaceAll(out, placeholder, ev.sysTable(tableName))
	}
	return out
}

// RunAll loads every .yaml file from dir and evaluates each rule.
// If the directory does not exist the call is a no-op (returns nil).
func (ev *Evaluator) RunAll(dir string) []Result {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Printf("[alerts] cannot read %s: %v\n", dir, err)
		return nil
	}

	var results []Result
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") {
			continue
		}
		results = append(results, ev.evalFile(filepath.Join(dir, e.Name())))
	}

	fired := 0
	for _, r := range results {
		if r.Fired() {
			fired++
		}
	}
	fmt.Printf("[alerts] evaluated %d rule(s), %d fired\n", len(results), fired)
	return results
}

// evalFile loads, validates, and executes one alert rule file.
func (ev *Evaluator) evalFile(path string) Result {
	now := time.Now().UTC().Format(time.RFC3339)

	raw, err := os.ReadFile(path)
	if err != nil {
		return Result{File: filepath.Base(path), Error: fmt.Sprintf("read: %v", err), FiredAt: now}
	}

	var def Definition
	if err := yaml.Unmarshal(raw, &def); err != nil {
		return Result{File: filepath.Base(path), Error: fmt.Sprintf("yaml: %v", err), FiredAt: now}
	}

	r := Result{
		Name:        def.Name,
		Title:       def.Title,
		Severity:    def.Severity,
		Description: def.Description,
		Message:     def.Message,
		Tags:        def.Tags,
		File:        filepath.Base(path),
		FiredAt:     now,
	}
	if r.Severity == "" {
		r.Severity = SeverityWarning
	}

	sql := ev.expandQuery(def.Query)

	// Security: alert queries must be read-only SELECT.
	if err := query.ValidateQueryContent(sql); err != nil {
		r.Error = fmt.Sprintf("security: %v", err)
		fmt.Printf("  [alert] BLOCKED %q: %v\n", def.Name, err)
		return r
	}

	fmt.Printf("  [alert] evaluating %q...\n", def.Name)
	resp, err := ev.client.ExecuteQueryWithFormat(sql)
	if err != nil {
		r.Error = fmt.Sprintf("query: %v", err)
		return r
	}

	rows, err := parseJSONCompact(resp)
	if err != nil {
		r.Error = fmt.Sprintf("parse: %v", err)
		return r
	}

	r.Rows = rows
	if len(rows) > 0 {
		fmt.Printf("  [alert] FIRED  %q — %d row(s)\n", def.Name, len(rows))
	}
	return r
}

// parseJSONCompact converts a ClickHouse JSONCompact response to a slice of
// column-name → value maps.
func parseJSONCompact(raw string) ([]map[string]interface{}, error) {
	var res struct {
		Meta []struct {
			Name string `json:"name"`
		} `json:"meta"`
		Data [][]json.RawMessage `json:"data"`
		Rows int                 `json:"rows"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w (%.120s)", err, raw)
	}
	out := make([]map[string]interface{}, 0, res.Rows)
	for _, row := range res.Data {
		rec := make(map[string]interface{}, len(res.Meta))
		for i, col := range res.Meta {
			if i < len(row) {
				var v interface{}
				_ = json.Unmarshal(row[i], &v)
				rec[col.Name] = v
			}
		}
		out = append(out, rec)
	}
	return out, nil
}
