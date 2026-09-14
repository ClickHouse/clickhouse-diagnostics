-- Part fetches in flight right now: which replica this node is pulling from,
-- how far along, how big. Many long-running fetches after a Keeper or network
-- incident = the replica is catching up; a fetch stuck at the same progress
-- across two bundles is a wedged interserver connection.
SELECT
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
FROM system.replicated_fetches
ORDER BY elapsed DESC
LIMIT 500
