-- Parts-propagation lag per replica on a SharedMergeTree cluster: which
-- partitions a replica is behind on, by how many blocks, and which partitions
-- it has not got at all. Every replica keeps its own view of system.parts and
-- learns about new parts asynchronously (Keeper notification, then a fetch); a
-- replica still missing the newest parts of a partition shows a lower
-- max_block_number than its siblings, and a replica that has fetched none of a
-- partition contributes no row at all — which is why the replica set is
-- compared explicitly (see replicas_missing_partition below). Per
-- (database, table, partition_id) because block numbers are allocated per
-- partition.
--
-- Why not system.replicas: that table says, per replica, how many level-0
-- parts it has not fetched (inserts_in_queue) and how old the oldest of them
-- is (absolute_delay, inserts_oldest_time) — the server's own answer, and the
-- one to use for "how long". It does not say which partition or which block
-- each replica reached, which is what this file adds.
--
-- Columns that are easy to over-read:
--   * blocks_behind is sound: block numbers are monotonic per partition, so
--     cluster max minus this replica's max is a real count of blocks it has
--     not got.
--   * newest_part_skew_s is NOT the age of those missing blocks. It is the gap
--     between two independently maximised modification_time values, and it
--     misleads in three ways: after an idle spell it is large even when the
--     missing blocks are seconds old; for a burst written within a few seconds
--     it stays small however long the replica stays behind; and a merge on the
--     lagging replica refreshes its modification_time without closing the
--     block gap. Read it as skew between the newest part each replica holds —
--     useful as corroboration, never as a duration. The duration lives in
--     system.replicas.absolute_delay.
--   * replicas_missing_partition names the replicas with NO active part in
--     this partition, computed as the per-table replica set minus the
--     per-partition one. For a recent partition that is the strongest lag
--     signal there is. For an old one it can be an artefact of the per-host
--     cap below, which keeps each replica's most recently written partitions
--     and drops the rest: compare partitions_sampled_for_host with the cap.
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
-- Only rows that are behind or incomplete are returned, worst first, capped at
-- 1000: an empty file on a cloud collection means no replica was behind and
-- none was missing a partition at collection time. The raw numbers are in
-- parts_max_block_recent.
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
    dateDiff('second', last_part_time, cluster_last_part_time)      AS newest_part_skew_s,
    active_parts,
    replicas_with_partition,
    replicas_with_table,
    replicas_missing_partition,
    partitions_sampled_for_host
FROM
(
    SELECT
        *,
        max(max_block)        OVER w_part AS cluster_max_block,
        max(last_part_time)   OVER w_part AS cluster_last_part_time,
        count()               OVER w_part AS replicas_with_partition,
        uniqExact(hostname)   OVER w_tab  AS replicas_with_table,
        -- The replicas that hold this table but no active part of this
        -- partition. groupUniqArray as a window aggregate, so this stays one
        -- pass over the fan-out.
        arrayFilter(h -> NOT has(groupUniqArray(hostname) OVER w_part, h),
                    groupUniqArray(hostname) OVER w_tab)            AS replicas_missing_partition,
        count()               OVER w_host AS partitions_sampled_for_host
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
    WINDOW
        w_part AS (PARTITION BY database, table, partition_id),
        w_tab  AS (PARTITION BY database, table),
        w_host AS (PARTITION BY hostname)
)
WHERE blocks_behind > 0
   OR notEmpty(replicas_missing_partition)
ORDER BY length(replicas_missing_partition) DESC, blocks_behind DESC, newest_part_skew_s DESC
LIMIT 1000
