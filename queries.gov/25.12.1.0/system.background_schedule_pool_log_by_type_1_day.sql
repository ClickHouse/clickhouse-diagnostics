-- Background schedule pool runs per host, hour and task type over the last
-- day (system.background_schedule_pool_log, 25.12+; the server logs the
-- slower runs). The pool runs every table's periodic work — replica
-- restarting threads, queue updates, cleanup, SharedMergeTree leader
-- elections and assignees — and when it saturates, replication and merges
-- stall without an error. metric_log shows that the pool was full; this says
-- which task type held it, how long its runs took and how many failed.
-- task_type is log_name with its "<db>.<table> (...)" prefix removed, so one
-- row per type and hour even on a server with tens of thousands of tables.
-- Gov: the task type only — no table names, no exception text. Task names
-- can carry identifiers outside the "(...)" form: a "<disk>::" prefix
-- (DiskLocalCheckThread) is masked to "*::", as in text_log_histogram_1_day,
-- and a name still holding a dot (a bare "<db>.<table>") is hashed.
SELECT
    hex(SHA256(concat(hostName(), '%salt%')))    AS hostname,
    toStartOfHour(event_time)                    AS time,
    if(masked LIKE '%.%', hex(SHA256(concat(masked, '%salt%'))), masked) AS task_type,
    count()                                      AS runs,
    sum(duration_ms)                             AS total_ms,
    max(duration_ms)                             AS max_ms,
    quantile(0.99)(duration_ms)                  AS p99_ms,     -- sampled: bounded memory whatever the row count
    uniqExact(database, table)                   AS tables,
    countIf(error != 0)                          AS errors
FROM
(
    SELECT
        *,
        replaceRegexpOne(
            if(match(log_name, '^.+ \\((.+)\\)$'), extract(log_name, '^.+ \\((.+)\\)$'), log_name),
            '^[^:\\s]+::', '*::') AS masked
    FROM system.background_schedule_pool_log
)
WHERE event_date >= toDate({from:1d}) AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY hostname, time, task_type
ORDER BY hostname, time, total_ms DESC
