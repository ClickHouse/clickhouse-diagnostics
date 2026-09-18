-- Part fetches in flight on every replica.
SELECT
    hostName() AS hostname,
    database,
    table,
    elapsed,
    progress,
    result_part_name,
    partition_id,
    total_size_bytes_compressed,
    bytes_read_compressed,
    source_replica_hostname,
    source_replica_port,
    to_detached
FROM clusterAllReplicas(default, system.replicated_fetches)
ORDER BY elapsed DESC
LIMIT 500
