-- Each replica's newest block number and last part time for its 200 most
-- recently written partitions — the raw numbers a parts-propagation lag is
-- computed from. On SharedMergeTree every replica keeps its own view of
-- system.parts; a replica that has not yet fetched the newest part of a
-- partition shows a lower max_block_number than its siblings, and the gap in
-- last_visible says for how long. Block numbers are allocated per partition,
-- so the grain is the partition; a per-table max would mix partitions.
--
-- Cost: one pass over system.parts (in-memory) with five narrow columns,
-- grouped per partition; 200 rows per host.
-- Cloud: fans out on purpose — the cloud collector otherwise reads
-- system.parts from ONE replica (SharedSystemTables). LIMIT 200 BY hostname
-- so every replica contributes its own 200 and the busiest one cannot take
-- the whole cap; the same partition therefore appears once per replica and
-- the rows compare directly. The derived lag is in parts_max_block_by_replica.
SELECT
    hostName()               AS hostname,
    database,
    table,
    partition_id,
    max(max_block_number)    AS max_block,
    max(modification_time)   AS last_visible,
    count()                  AS active_parts
FROM clusterAllReplicas(default, system.parts)
WHERE active
  AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY hostname, database, table, partition_id
ORDER BY hostname, last_visible DESC, database, table, partition_id
LIMIT 200 BY hostname
