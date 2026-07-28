// Package alert provides a YAML-driven alerting system for ClickHouse diagnostics.
//
// Alert rules live in .yaml files (see the alerts/ directory at the repo root).
// Each rule defines a read-only SELECT query; if the query returns any rows the
// alert fires and those rows are surfaced in the HTML dashboard.
//
// YAML rule format
// ────────────────
//
//	name:        unique_snake_case_id
//	title:       "Human-readable title"
//	severity:    critical | warning | info        (default: warning)
//	description: |
//	  Multi-line explanation of what the alert means
//	  and what to do about it.
//	tags:
//	  - mutations
//	  - performance
//	query: |
//	  SELECT database, table, mutation_id,
//	         dateDiff('hour', create_time, now()) AS hours_running,
//	         parts_to_do
//	  FROM {sys.mutations}                 -- placeholder, see below
//	  WHERE parts_to_do > 0
//	    AND dateDiff('hour', create_time, now()) > 3
//	  ORDER BY hours_running DESC
//	message: "Mutation {mutation_id} on {database}.{table} running {hours_running}h"
//
//	Keep root rules to columns/syntax available on the oldest supported
//	server (22.8); gate anything newer behind a version subdirectory. For
//	example system.mutations.is_killed (24.1+) lives in
//	alerts/24.1.1.0/mutation_running_too_long.yaml, not the root rule.
//
// System-table placeholder
// ────────────────────────
//
//	Use {sys.<table>} in your query instead of a hard-coded table path.
//	The evaluator substitutes the right reference based on the run mode:
//
//	  {sys.query_log}  →  system.query_log                                  (onprem / gov)
//	                   →  clusterAllReplicas(default, system.query_log)     (cloud, per-replica)
//	  {sys.parts}      →  system.parts                                      (all modes)
//
//	Tables whose contents are shared across replicas (parts, tables,
//	replicas, replication_queue, mutations, dictionaries, detached_parts,
//	columns, databases) are queried directly even in cloud mode: each
//	replica sees the same rows, so clusterAllReplicas would duplicate them.
//
// Message template
// ────────────────
//
//	Use {column_name} in the message string; the evaluator substitutes the
//	column value from each result row.  One alert instance is created per row.
//
// Security
// ────────
//
//	All alert queries are validated by ValidateQueryContent before execution.
//	Only SELECT / WITH queries are accepted; INSERT, ALTER, DROP, etc. are
//	rejected with an error and the alert is skipped.
package alert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"clickhouse-diagnostic/internal"
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

// expandQuery replaces {sys.<name>} placeholders with the correct table
// path for the evaluator's mode. Delegated to the shared template
// helper (single source of truth for the cloud-shared-tables list).
func (ev *Evaluator) expandQuery(sql string) string {
	return query.Apply(sql, query.Vars{Mode: ev.mode})
}

// RunAll loads every .yaml file from dir and evaluates each rule.
// If the directory does not exist the call is a no-op (returns nil).
//
// dir follows the same version-directory convention as the query
// directories: a subdirectory named like a ClickHouse version
// (e.g. "25.4.1.0") overrides a same-named root rule when the connected
// server (serverVersion) is at least that version.
func (ev *Evaluator) RunAll(dir string, serverVersion internal.Version) []Result {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	files, err := query.FindVersionedFiles(dir, serverVersion, ".yaml")
	if err != nil {
		fmt.Printf("[alerts] cannot read %s: %v\n", dir, err)
		return nil
	}

	var results []Result
	for _, f := range files {
		r := ev.evalFile(f.FullPath)
		// Preserve the version-override context in the reported filename
		// (mirrors analysis.go) so support engineers can tell which variant
		// fired — e.g. "22.11.1.0/detached_parts_exist.yaml".
		if f.DirName != "" {
			r.File = f.DirName + "/" + f.Name
		}
		// Surface any genuine failure (read / yaml / query / parse) once,
		// here, using the version-aware File — evalFile only stores it in
		// Result.Error, which otherwise never reaches stdout, so a broken
		// rule would look identical to a healthy one. The security case is
		// skipped: evalFile already prints "[alert] BLOCKED". Missing-table
		// skips carry no Error, so they're excluded automatically.
		if r.Error != "" && !strings.HasPrefix(r.Error, "security:") {
			fmt.Printf("  [alert] ERROR %s: %s\n", r.File, r.Error)
		}
		results = append(results, r)
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
		// A missing table means the rule is not applicable on this server
		// (e.g. system.crash_log only exists after a crash; system.text_log
		// / system.query_log may be disabled), not that the rule is broken.
		// Return a non-fired, non-error result so it doesn't masquerade as a
		// fired alert and doesn't trip "[alert] ERROR" — which must stay a
		// reliable signal for genuinely broken rules (missing column, bad
		// SQL). RunAll prints genuine errors.
		if strings.Contains(err.Error(), "UNKNOWN_TABLE") {
			fmt.Printf("  [alert] skipped %q (table not present)\n", def.Name)
			return r
		}
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
