package collectmode

import (
	"strconv"
	"strings"
	"time"
)

// Runner is the one client method the probe needs (pkg.ClickHouseClient).
type Runner interface {
	ExecuteQueryReal(query string) (string, error)
}

// ProbeQuery reads only in-memory tables, on every supported version (22.8+).
// max_threads reads "auto(16)" when not set by hand, which is the core count
// the server itself uses.
const ProbeQuery = `SELECT
    (SELECT sum(value) FROM system.metrics WHERE metric = 'Query'),
    (SELECT sum(value) FROM system.metrics WHERE metric = 'MemoryTracking'),
    (SELECT sum(value) FROM system.metrics WHERE metric = 'ReadonlyReplica'),
    (SELECT max(value) FROM system.asynchronous_metrics WHERE metric = 'LoadAverage1'),
    (SELECT max(value) FROM system.asynchronous_metrics WHERE metric = 'NumberOfTables'),
    (SELECT max(value) FROM system.asynchronous_metrics WHERE metric = 'OSMemoryTotal'),
    (SELECT toUInt64OrZero(extract(any(value), '\\d+')) FROM system.settings WHERE name = 'max_threads'),
    (SELECT sum(value) FROM system.metrics WHERE metric = 'ZooKeeperSession'),
    (SELECT max(value) FROM system.metrics WHERE metric = 'ZooKeeperConnectionLossStartedTimestampSeconds')
FORMAT TSV`

// LimitsQuery reads the limits the ratios are measured against. Its table
// arrived in 23.3; on older servers it fails and the limits stay unknown.
const LimitsQuery = `SELECT
    (SELECT toInt64OrZero(any(value)) FROM system.server_settings WHERE name = 'max_concurrent_queries'),
    (SELECT toInt64OrZero(any(value)) FROM system.server_settings WHERE name = 'max_server_memory_usage'),
    (SELECT toFloat64OrZero(any(value)) FROM system.server_settings WHERE name = 'max_server_memory_usage_to_ram_ratio')
FORMAT TSV`

// Probe reads the load signals. An error means SELECT 1 or the in-memory
// read itself failed — which Decide treats as a struggling server.
func Probe(r Runner) (Signals, error) {
	var s Signals
	t := time.Now()
	if _, err := r.ExecuteQueryReal("SELECT 1"); err != nil {
		return s, err
	}
	s.Ping = time.Since(t)

	t = time.Now()
	out, err := r.ExecuteQueryReal(ProbeQuery)
	s.MetricsRead = time.Since(t)
	if err != nil {
		return s, err
	}
	f := fields(out)
	s.Queries = int64(num(f, 0))
	s.MemoryTracking = int64(num(f, 1))
	s.ReadonlyReplicas = int64(num(f, 2))
	s.Load1 = num(f, 3)
	s.Tables = int64(num(f, 4))
	ram := num(f, 5)
	s.CPUs = num(f, 6)
	s.KeeperSessions = int64(num(f, 7))
	s.KeeperLossSince = int64(num(f, 8))

	if out, err := r.ExecuteQueryReal(LimitsQuery); err == nil {
		l := fields(out)
		s.MaxConcurrentQueries = int64(num(l, 0))
		limit, ratio := num(l, 1), num(l, 2)
		if limit <= 0 && ram > 0 {
			if ratio <= 0 {
				ratio = 0.9
			}
			limit = ram * ratio
		}
		s.MemoryLimit = int64(limit)
	}
	return s, nil
}

func fields(tsv string) []string {
	return strings.Split(strings.TrimSpace(tsv), "\t")
}

func num(f []string, i int) float64 {
	if i >= len(f) {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.Trim(f[i], `"`), 64)
	return v
}
