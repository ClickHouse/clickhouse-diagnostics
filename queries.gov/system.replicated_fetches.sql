-- Gov: database/table/part/partition/source host hashed; port dropped.
SELECT
    hex(SHA256(concat(database, '%salt%')))                AS database,
    hex(SHA256(concat(table, '%salt%')))                   AS table,
    elapsed,
    progress,
    hex(SHA256(concat(result_part_name, '%salt%')))        AS result_part_name,
    hex(SHA256(concat(partition_id, '%salt%')))            AS partition_id,
    total_size_bytes_compressed,
    bytes_read_compressed,
    hex(SHA256(concat(source_replica_hostname, '%salt%'))) AS source_replica_hostname,
    to_detached
FROM system.replicated_fetches
ORDER BY elapsed DESC
LIMIT 500
