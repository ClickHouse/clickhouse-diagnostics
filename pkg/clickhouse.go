package pkg

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultQueryTimeout bounds each query server-side. It is deliberately
// lower than the HTTP client timeout: when the server enforces the limit
// the query fails with a clean Code: 159 that lands in the customer's
// query_log, whereas a client-side timeout just drops the connection and
// leaves the server executing.
const DefaultQueryTimeout = 240 * time.Second

// clientTimeoutMargin is how much longer the HTTP client waits than the
// server-side limit, so the server always wins the race and we get a real
// error instead of a silent disconnect.
const clientTimeoutMargin = 60 * time.Second

// ClickHouseClient represents a ClickHouse HTTP client
type ClickHouseClient struct {
	protocol   string
	host       string
	port       string
	username   string
	password   string
	httpClient *http.Client

	// queryTimeout is sent as max_execution_time on every request. Zero
	// means no server-side limit, in which case the HTTP client is also
	// left unbounded so the two stay consistent.
	queryTimeout time.Duration

	// Dry-run state. When dryRun is true, ExecuteQuery prints the SQL
	// and the system tables it would touch to dryOut, then returns an
	// empty result without contacting the server. dryEstimate adds a
	// real EXPLAIN ESTIMATE round-trip (read-only, metadata only) so
	// the operator can see scan sizes without running the query itself.
	//
	// ExecuteQueryReal bypasses this interception — used for the
	// version probe, query-analysis pre-flight (so we have real
	// derived values to template into subsequent SQL), and the
	// EXPLAIN ESTIMATE round-trip itself.
	dryRun      bool
	dryOut      io.Writer
	dryEstimate bool
	dryCounter  int
}

// NewClickHouseClient creates a new ClickHouse client
func NewClickHouseClient(protocol, host, port, username, password string) *ClickHouseClient {
	return &ClickHouseClient{
		protocol:   protocol,
		host:       host,
		port:       port,
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: DefaultQueryTimeout + clientTimeoutMargin},

		queryTimeout: DefaultQueryTimeout,
	}
}

// SetQueryTimeout sets the server-side max_execution_time and moves the
// HTTP client timeout with it, preserving the invariant that the client
// outlives the server limit. A zero duration disables both: the server
// runs the query unbounded and the client waits indefinitely, which is
// only sensible when the operator has asked for it explicitly.
func (c *ClickHouseClient) SetQueryTimeout(d time.Duration) {
	c.queryTimeout = d
	if d <= 0 {
		c.queryTimeout = 0
		c.httpClient.Timeout = 0
		return
	}
	c.httpClient.Timeout = d + clientTimeoutMargin
}

// SetDryRun enables dry-run mode. While enabled, ExecuteQuery does NOT
// contact the server — every query is printed to out (with its
// system-table references) and an empty result is returned to the
// caller so downstream parsing doesn't fail.
//
// If explainEstimate is true, each intercepted SELECT additionally
// triggers a real `EXPLAIN ESTIMATE <query>` against the server. That
// is a metadata-only query — it returns the rows/marks/parts the
// query WOULD scan but reads no data parts. It respects readonly=1.
func (c *ClickHouseClient) SetDryRun(out io.Writer, explainEstimate bool) {
	c.dryRun = true
	c.dryOut = out
	c.dryEstimate = explainEstimate
	c.dryCounter = 0
}

// IsDryRun reports whether the client is in dry-run mode. Callers
// that print their own progress messages (e.g. the query executor,
// the analysis collector) use this to suppress "successfully saved …"
// lines whose presence in dry-run output would be misleading.
func (c *ClickHouseClient) IsDryRun() bool { return c.dryRun }

// ExecuteQuery executes a query against the ClickHouse server, or
// intercepts in dry-run mode.
func (c *ClickHouseClient) ExecuteQuery(query string) (string, error) {
	if c.dryRun {
		return c.dryRunIntercept(query)
	}
	return c.executeReal(query)
}

// ExecuteQueryReal bypasses dry-run interception. Used for queries
// that MUST reach the server even in dry-run: the version probe (to
// pick the right query variants), the query-analysis pre-flight (to
// derive normalized_query_hash from --query-id and vice versa, so the
// printed SQL has real values not unbound `{query_id}` markers), and
// the EXPLAIN ESTIMATE round-trip itself.
//
// Callers can use it for any read-only metadata fetch that should
// run independently of dry-run mode.
func (c *ClickHouseClient) ExecuteQueryReal(query string) (string, error) {
	return c.executeReal(query)
}

// ExecuteQueryWithFormat executes a query and returns results in JSONCompact format.
// The query must NOT include a FORMAT clause.
func (c *ClickHouseClient) ExecuteQueryWithFormat(query string) (string, error) {
	return c.ExecuteQuery(query + "\nFORMAT JSONCompact")
}

// executeReal is the actual HTTP round-trip. Bypasses dry-run so it
// can be used from inside dryRunIntercept (for the EXPLAIN ESTIMATE
// metadata fetch) without recursion.
func (c *ClickHouseClient) executeReal(query string) (string, error) {
	// Build the URL with readonly setting to prevent write operations.
	//
	// output_format_json_quote_64bit_integers=1 is pinned rather than left
	// to the server default. JSON numbers are IEEE-754 doubles in most
	// parsers, so an unquoted UInt64 above 2^53 is silently rounded on
	// read — JavaScript turns 18446744073709551615 into
	// 18446744073709552000, and so does any jq expression that does
	// arithmetic. Real columns hit this: normalized_query_hash and every
	// cityHash64 value are full-width UInt64. The setting is writable
	// under readonly=1, so a user profile CAN turn it off; pinning it is
	// what makes the guarantee hold. Quoted integers are exact in every
	// parser, and the dashboard already expects this form (parseUInt64).
	//
	// max_execution_time bounds the query on the SERVER. Without it the
	// only bound is the HTTP client timeout, and the two have no
	// relationship: when the client gives up, Go closes the connection and
	// moves to the next query while the server keeps executing. On a slow
	// cluster that accumulates abandoned queries, each still burning CPU
	// and memory, while the collector stacks more on top.
	//
	// cancel_http_readonly_queries_on_client_close makes the server drop
	// the query when we disconnect. It only takes effect under readonly>0,
	// which is why it belongs next to readonly=1. Caveat: it does not fire
	// over HTTPS today — on a graceful close the server's liveness peek
	// sees the TLS close_notify bytes and concludes the peer is still
	// there (ClickHouse/ClickHouse#96737). So it helps on plain http and
	// max_execution_time remains the limit we actually rely on.
	//
	// enable_http_compression lets the server gzip the response. Go's
	// transport already sends Accept-Encoding: gzip and decompresses
	// transparently — but ONLY while we do not set that header ourselves.
	// If anyone adds it by hand, transparent decompression switches off
	// and raw gzip lands in the output files.
	//
	// All of these are applied by the server as one batch of setting
	// changes alongside readonly=1, so listing readonly first does not
	// block them (src/Server/HTTPHandler.cpp builds a single
	// SettingsChanges and checks constraints once).
	url := fmt.Sprintf("%s://%s:%s/?readonly=1&output_format_json_quote_64bit_integers=1"+
		"&max_execution_time=%d&cancel_http_readonly_queries_on_client_close=1&enable_http_compression=1",
		c.protocol, c.host, c.port, int64(c.queryTimeout.Seconds()))

	// Create the request
	req, err := http.NewRequest("POST", url, bytes.NewBufferString(query))
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "text/plain")

	// Add Basic Authentication
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	// Execute the request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("error executing request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("non-OK status: %d, body: %s", resp.StatusCode, body)
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response: %w", err)
	}

	return string(body), nil
}

// dryRunIntercept prints the query that would be sent and (if
// configured) returns a real EXPLAIN ESTIMATE for it. The returned
// payload is an empty result so callers that try to parse the
// response don't crash.
func (c *ClickHouseClient) dryRunIntercept(query string) (string, error) {
	c.dryCounter++
	tables := ExtractTables(query)
	tablesStr := "(no system.* references — function-only)"
	if len(tables) > 0 {
		tablesStr = strings.Join(tables, ", ")
	}

	fmt.Fprintf(c.dryOut, "\n[%d]\n", c.dryCounter)
	fmt.Fprintf(c.dryOut, "    Tables: %s\n", tablesStr)
	fmt.Fprintf(c.dryOut, "    SQL:\n%s\n", indentBlock(query, "      "))

	if c.dryEstimate && isExplainable(query) {
		// Append `FORMAT PrettyCompactMonoBlock` so the result renders
		// as the box-drawing table the operator expects:
		//   ┌─database─┬─table─┬─parts─┬─rows─┬─marks─┐
		//   │ default  │ ttt   │     1 │  128 │     8 │
		//   └──────────┴───────┴───────┴──────┴───────┘
		// Without an explicit FORMAT the HTTP default is TabSeparated,
		// which prints `default\tttt\t1\t128\t8` with no header — it
		// reads as "empty" at a glance even when the estimate is
		// populated. PrettyCompactMonoBlock also renders an empty
		// table (just headers + separator) for the system.* queries
		// that have no MergeTree parts to estimate, so the operator
		// sees "no parts" explicitly instead of a blank line.
		est, err := c.executeReal("EXPLAIN ESTIMATE " + stripTrailingFormat(query) + " FORMAT PrettyCompactMonoBlock")
		switch {
		case err != nil:
			fmt.Fprintf(c.dryOut, "    EXPLAIN ESTIMATE: (error: %v)\n", err)
		case isEmptyEstimate(est):
			// Header + separator with no data row — the planner found
			// nothing to scan. Most often a virtual system table
			// (e.g. system.dictionaries) or a query whose predicate
			// prunes every part. Saying so plainly beats a blank box.
			fmt.Fprintln(c.dryOut, "    EXPLAIN ESTIMATE: this table is empty")
		default:
			fmt.Fprintf(c.dryOut, "    EXPLAIN ESTIMATE:\n%s\n", indentBlock(strings.TrimRight(est, "\n"), "      "))
		}
	}
	return emptyResponseFor(query), nil
}

// isEmptyEstimate reports whether a PrettyCompactMonoBlock-formatted
// EXPLAIN ESTIMATE result contains no data rows. The planner returns
// just the top "┌──┐" and bottom "└──┘" separators when nothing
// matched. Data rows always render with a "│" body, so any line that
// starts (after leading whitespace) with "│" means we have content.
func isEmptyEstimate(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimLeft(line, " \t\r\n0123456789.")
		if strings.HasPrefix(t, "│") {
			return false
		}
	}
	return true
}

// indentBlock prefixes every line of s with prefix.
func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// isExplainable returns true if the query is a SELECT or WITH that
// EXPLAIN ESTIMATE can analyse. Strips leading SQL line- and block-
// comments first, because many of the .sql files in
// queries.query_analysis/ start with several `-- description` lines
// above the SELECT — without comment-stripping isExplainable would
// see `--` as the first token and skip EXPLAIN ESTIMATE entirely.
func isExplainable(query string) bool {
	trimmed := stripLeadingSQLComments(query)
	trimmed = strings.TrimLeft(strings.TrimSpace(trimmed), "(")
	up := strings.ToUpper(trimmed)
	return strings.HasPrefix(up, "SELECT") || strings.HasPrefix(up, "WITH")
}

// stripLeadingSQLComments removes any leading whitespace, `-- line`
// comments, and `/* block */` comments from s — i.e. everything
// preceding the first real SQL keyword. Used by isExplainable so it
// can correctly identify SELECT/WITH queries whose head is wrapped
// in documentation comments.
func stripLeadingSQLComments(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		default:
			return s
		}
	}
}

// stripTrailingFormat removes a trailing `FORMAT <name>` clause so
// EXPLAIN ESTIMATE can prefix the query cleanly.
func stripTrailingFormat(query string) string {
	t := strings.TrimRight(query, " \t\r\n;")
	idx := strings.LastIndex(strings.ToUpper(t), "FORMAT")
	if idx == -1 {
		return t
	}
	tail := strings.TrimSpace(t[idx:])
	parts := strings.Fields(tail)
	if len(parts) == 2 && strings.EqualFold(parts[0], "FORMAT") {
		return strings.TrimRight(t[:idx], " \t\r\n")
	}
	return t
}

// emptyResponseFor returns a parseable empty result matching the
// shape the caller asked for.
func emptyResponseFor(query string) string {
	if strings.Contains(strings.ToUpper(query), "FORMAT JSONCOMPACT") {
		return `{"meta":[],"data":[],"rows":0}`
	}
	return ""
}

// GetConnectionInfo returns connection information for display
func (c *ClickHouseClient) GetConnectionInfo() string {
	return fmt.Sprintf("%s://%s:%s", c.protocol, c.host, c.port)
}
