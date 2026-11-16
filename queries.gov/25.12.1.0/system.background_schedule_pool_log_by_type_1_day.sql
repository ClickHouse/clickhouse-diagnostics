-- Background schedule pool runs per host, hour, pool and task type over the
-- last day (system.background_schedule_pool_log, 25.12+; the server logs the
-- slower runs). The pool runs every table's periodic work — replica
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
-- Same cost as the single regex it replaced (30M rows, 2 threads: ~15 s
-- either way); one Kafka or Buffer table no longer adds a row per hour.
-- Gov: the task type only — no table names, no exception text. Two task
-- names carry a configured name of their own: "<disk>::Blob{Copier,Killer}Thread"
-- is masked to "*::…" and "FileCache(<cache>)" to "FileCache(*)"; class
-- prefixes such as "StorageReplicatedMergeTree::" stay. A name still
-- holding a dot (a bare "<db>.<table>", as WindowView names its tasks) is
-- hashed.
SELECT
    hex(SHA256(concat(hostName(), '%salt%')))    AS hostname,
    toStartOfHour(event_time)                    AS time,
    substring(query_id, 1, position(query_id, '::') - 1) AS pool,
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
        replaceRegexpOne(replaceRegexpOne(
            multiIf(
                match(log_name, '^Storage\\w+ \\('), replaceRegexpOne(log_name, '^(Storage\\w+) \\((?:[^()]|\\([^()]*\\))*\\)', '\\1'),
                match(log_name, '\\.DistributedInsertQueue\\.[^/]*/Bg$'), 'DistributedInsertQueue/Bg',
                match(log_name, '^.+ \\([^()]+\\)$'), extract(log_name, '^.+ \\(([^()]+)\\)$'),
                log_name),
            '^[^:\\s]+::(Blob\\w+Thread)$', '*::\\1'),
            '^FileCache\\(.*\\)$', 'FileCache(*)') AS masked
    FROM system.background_schedule_pool_log
)
WHERE event_date >= toDate({from:1d}, timezone()) AND event_time > {from:1d} AND event_time <= {to:now}
GROUP BY hostname, time, pool, task_type
ORDER BY hostname, time, pool, total_ms DESC
