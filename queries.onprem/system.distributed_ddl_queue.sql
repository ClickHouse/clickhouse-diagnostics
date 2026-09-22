-- ON CLUSTER / Replicated-database DDL as seen in Keeper: one row per (entry, host)
-- with status, exception and duration. Stuck DDL (status not Finished for
-- minutes), UUID collisions on replayed CREATE OR REPLACE (code 57) and
-- "session expired" DDL failures (code 571/999) are visible only here.
-- Reading this table costs one Keeper read per entry; the LIMIT and the
-- collector's -query-timeout bound it when Keeper itself is slow.
SELECT
    entry,
    entry_version,
    initiator_host,
    initiator_port,
    cluster,
    leftUTF8(query, 500)          AS query,
    query_create_time,
    host,
    port,
    status,
    exception_code,
    leftUTF8(exception_text, 500) AS exception_text,
    query_finish_time,
    query_duration_ms
FROM system.distributed_ddl_queue
WHERE query_create_time > {from:7d} AND query_create_time <= {to:now}
ORDER BY query_create_time DESC
LIMIT 5000
