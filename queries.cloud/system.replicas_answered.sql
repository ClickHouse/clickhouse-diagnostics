-- tier: essential
-- Which replicas of the service answered this collection. Every request runs
-- with skip_unavailable_shards = 1, so a replica that is down no longer
-- fails the per-replica (clusterAllReplicas) collectors — it is simply
-- missing from them. This file says which, exactly: clusterAllReplicas
-- treats every replica as its own shard and numbers them in cluster order
-- (shard_num, then replica_num), so a reply's _shard_num IS the position of
-- the configured host that sent it. No host-name matching — aliases, IP
-- endpoints and replicas sharing a short name cannot mislead it.
--   pos       the host's position in the `default` cluster
--   answered  1 when a reply came from that position
SELECT
    c.host_name,
    c.host_address,
    c.pos,
    c.pos IN (SELECT _shard_num FROM clusterAllReplicas(default, system.one)) AS answered
FROM
(
    SELECT host_name, host_address, row_number() OVER (ORDER BY shard_num, replica_num) AS pos
    FROM system.clusters
    WHERE cluster = 'default'
) AS c
ORDER BY answered, pos
