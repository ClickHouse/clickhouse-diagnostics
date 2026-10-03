-- Parts-propagation lag per replica on a SharedMergeTree cluster: which
-- partitions a replica has not caught up on, by how many blocks, for how
-- long. Every replica keeps its own view of system.parts and learns about
-- new parts asynchronously (Keeper notification, then a fetch); a replica
-- still missing the newest part of a partition shows a lower
-- max_block_number than its siblings. That gap is the lag in blocks, and the
-- gap between the two last modification_time values is how long it has
-- lasted. Per (database, table, partition_id) because block numbers are
-- allocated per partition.
--
-- Why beside system.replicas: on SharedMergeTree that table IS populated per
-- replica — inserts_in_queue counts the level-0 parts this replica has not
-- fetched yet, absolute_delay is the age of the oldest, oldest_part_to_get
-- names it — and it is the cheaper first look. But it counts level-0 parts
-- only (a merge completing elsewhere makes its sources stop counting even
-- though the merged part is not here yet), it says nothing about merged
-- parts, and it does not say what the newest block on the OTHER replicas
-- is. This file compares the replicas directly on system.parts, per
-- partition, and names the block each one has reached.
--
-- Cost, and the two shapes this deliberately avoids:
--   * window functions, not a CTE joined to itself — a WITH … AS (subquery)
--     is inlined at every reference, so a CTE read twice fans out over
--     system.parts twice (measured: 2× rows, 7× memory);
--   * LIMIT 20000 BY hostname inside the fan-out — each replica contributes
--     its 20 000 most recently written partitions (a lagging replica's hot
--     partition is still among ITS most recent), so the initiator holds at
--     most replicas × 20 000 rows for the window step instead of every
--     partition of every replica.
-- Only rows behind the cluster max are returned, worst first, capped at
-- 1000: an empty file on a cloud collection means no replica was behind at
-- collection time. The raw numbers are in parts_max_block_recent.
SELECT
    hostname,
    database,
    table,
    partition_id,
    max_block,
    cluster_max_block,
    cluster_max_block - max_block                                   AS blocks_behind,
    last_part_time,
    cluster_last_part_time,
    dateDiff('second', last_part_time, cluster_last_part_time)      AS seconds_behind,
    active_parts,
    replicas
FROM
(
    SELECT
        *,
        max(max_block)        OVER w AS cluster_max_block,
        max(last_part_time)   OVER w AS cluster_last_part_time,
        count()               OVER w AS replicas
    FROM
    (
        SELECT
            hostName()               AS hostname,
            database,
            table,
            partition_id,
            max(max_block_number)    AS max_block,
            count()                  AS active_parts,
            max(modification_time)   AS last_part_time
        FROM clusterAllReplicas(default, system.parts)
        WHERE active
          AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
        GROUP BY hostname, database, table, partition_id
        ORDER BY hostname, last_part_time DESC
        LIMIT 20000 BY hostname
    )
    WINDOW w AS (PARTITION BY database, table, partition_id)
)
WHERE blocks_behind > 0
ORDER BY blocks_behind DESC, seconds_behind DESC
LIMIT 1000
