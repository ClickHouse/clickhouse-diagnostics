-- Gov: hosts, cluster, query text and exception text are hashed; status,
-- exception_code and timings stay readable.
SELECT
    entry,
    entry_version,
    hex(SHA256(concat(initiator_host, '%salt%')))                 AS initiator_host,
    hex(SHA256(concat(cluster, '%salt%')))                        AS cluster,
    hex(SHA256(concat(leftUTF8(query, 500), '%salt%')))           AS query,
    query_create_time,
    hex(SHA256(concat(host, '%salt%')))                           AS host,
    status,
    exception_code,
    hex(SHA256(concat(leftUTF8(exception_text, 500), '%salt%')))  AS exception_text,
    query_finish_time,
    query_duration_ms
FROM system.distributed_ddl_queue
WHERE query_create_time > {from:7d} AND query_create_time <= {to:now}
ORDER BY query_create_time DESC
LIMIT 5000
