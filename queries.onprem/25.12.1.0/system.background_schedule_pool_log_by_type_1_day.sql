-- Background schedule pool runs per host, hour, pool and task type over the
-- last day (system.background_schedule_pool_log, 25.12+; runs of 30 ms or
-- more). The pool runs every table's periodic work — replica
-- restarting threads, queue updates, cleanup, SharedMergeTree leader
-- elections and assignees — and when it saturates, replication and merges
-- stall without an error. metric_log shows that the pool was full; this says
-- which task type held it, how long its runs took and how many failed.
-- pool: five schedule pools write this log, told apart only by the
-- "<thread name>::<uuid>" query_id — BgSchPool (the one
-- background_schedule_pool_size sizes), BgBufSchPool (Buffer flushes),
-- BgDistSchPool (Distributed sends), BgMBSchPool (Kafka, RabbitMQ, NATS),
-- IcebergSchPool.
-- task_type: log_name without its table, so one row per type and hour even
-- on a server with tens of thousands of tables. Three shapes carry one:
-- "Storage<Engine> (<db>.<table>[ (<uuid>)])[suffix]" (Kafka, Kafka2,
-- FileLog, Buffer) keeps "Storage<Engine>[suffix]";
-- "<db>.<table>.DistributedInsertQueue.<disk>/Bg" becomes
-- "DistributedInsertQueue/Bg"; "<db>.<table> (<Task>)" keeps "<Task>".
-- duration_ms >= 30: the server's default threshold from 25.12.2 on.
-- 25.12.1 logs every run (threshold 0; upstream CI logged 54M rows in one
-- job), and so does a server configured to: the filter gives runs,
-- total_ms and p99_ms the same meaning on every version.
-- ln: the regexes read a plain String copy of log_name. On the
-- LowCardinality column they run over each block's whole dictionary even
-- when the filter keeps a few of its rows: 13 s instead of 2 s on 30M rows
-- logged at threshold 0 (2 threads).
WITH toString(log_name) AS ln
SELECT
    hostName()                                   AS hostname,
    toStartOfHour(event_time)                    AS time,
    substring(query_id, 1, position(query_id, '::') - 1) AS pool,
    multiIf(
        match(ln, '^Storage\\w+ \\('), replaceRegexpOne(ln, '^(Storage\\w+) \\((?:[^()]|\\([^()]*\\))*\\)', '\\1'),
        match(ln, '\\.DistributedInsertQueue\\.[^/]*/Bg$'), 'DistributedInsertQueue/Bg',
        match(ln, '^.+ \\([^()]+\\)$'), extract(ln, '^.+ \\(([^()]+)\\)$'),
        ln)                                      AS task_type,
    count()                                      AS runs,
    sum(duration_ms)                             AS total_ms,
    max(duration_ms)                             AS max_ms,
    quantile(0.99)(duration_ms)                  AS p99_ms,     -- sampled: bounded memory whatever the row count
    uniqExact(database, table)                   AS tables,
    argMax(concat(database, '.', table), duration_ms) AS slowest_table,
    countIf(error != 0)                          AS errors,
    leftUTF8(anyIf(exception, error != 0), 300)  AS example_exception
FROM system.background_schedule_pool_log
WHERE event_date >= toDate({from:1d}, timezone()) AND event_time > {from:1d} AND event_time <= {to:now}
  AND duration_ms >= 30
GROUP BY hostname, time, pool, task_type
ORDER BY hostname, time, pool, total_ms DESC
