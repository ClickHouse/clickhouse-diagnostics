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
-- Onprem: this node's view only — one node has nothing to compare against,
-- so two bundles collected on two replicas minutes apart are lined up by the
-- skill's pre-pass on (database, table, partition_id). The full system.parts
-- dump keeps the 50 000 LARGEST parts; on a node with more parts than that
-- the small, freshly inserted ones that show lag are exactly what it drops,
-- which is why this grouped, recency-ordered file exists beside it.
SELECT
    hostName()               AS hostname,
    database,
    table,
    partition_id,
    max(max_block_number)    AS max_block,
    max(modification_time)   AS last_visible,
    count()                  AS active_parts
FROM system.parts
WHERE active
  AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY hostname, database, table, partition_id
ORDER BY last_visible DESC, database, table, partition_id
LIMIT 200
