-- The DDL queue lives in Keeper and is the same from every replica; read it once.
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
