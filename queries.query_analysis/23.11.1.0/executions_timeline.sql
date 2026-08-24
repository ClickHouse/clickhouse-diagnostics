-- 23.11+ variant: uses the hostname COLUMN (added to system log tables in 23.11); the root file uses hostName() instead.
-- One row per individual execution of the focus hash within the window.
-- Drives the per-execution scatter chart in the dashboard so each run
-- is visible as its own dot (the hourly hash_summary buckets hide this
-- when, for example, all 10 executions of a slow query happened in the
-- same hour).
--
-- LIMIT 10000 caps the output for very high-traffic queries; the
-- ORDER BY event_time DESC means the most recent 10000 are kept,
-- which is usually what an investigation cares about.
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
SELECT
    toString(event_time)                                    AS ts,
    event_time_microseconds                                 AS ts_micro,
    query_id,
    type,
    exception_code,
    query_duration_ms,
    memory_usage,
    formatReadableSize(memory_usage)                        AS memory_usage_human,
    -- user CPU per execution, in microseconds → JS renders this as
    -- seconds in the per-execution scatter. Coalesced because not
    -- every ProfileEvents map has UserTimeMicroseconds (e.g. very
    -- short queries that never spent CPU in user space).
    ProfileEvents['UserTimeMicroseconds']                   AS user_cpu_us,
    read_rows,
    read_bytes,
    formatReadableSize(read_bytes)                          AS read_bytes_human,
    hostname,
    user
FROM {sys.query_log}
WHERE normalized_query_hash = {normalized_query_hash}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
  AND NOT has(databases, 'system')
ORDER BY event_time DESC
LIMIT 10000
