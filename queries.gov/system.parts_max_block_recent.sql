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
-- Gov: this node's view (see queries.onprem for why the file exists beside
-- the full parts dump). hostname, database and table are hashed as in every
-- gov file; partition_id stays raw like in every other gov parts file (it
-- is the `partition` expression that carries the key). Block numbers,
-- times and counts stay clear: two gov bundles with the same salt still
-- line up on the hashes.
SELECT
    hex(SHA256(concat(hostName(), '%salt%')))       AS hostname,
    hex(SHA256(concat(database, '%salt%')))         AS database,
    hex(SHA256(concat(table, '%salt%')))            AS table,
    partition_id,
    max(max_block_number)                           AS max_block,
    max(modification_time)                          AS last_visible,
    count()                                         AS active_parts
FROM system.parts
WHERE active
  AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY hostname, database, table, partition_id
ORDER BY last_visible DESC, database, table, partition_id
LIMIT 200
