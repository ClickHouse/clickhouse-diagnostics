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
-- The `p.` alias on the WHERE columns is load-bearing, not style. Aliases are
-- global in ClickHouse, so `AS database` on the hashed projection puts
-- `database` in scope as THAT expression: a bare `WHERE database NOT IN
-- ('system', …)` then compares a 64-character hash with 'system', is true for
-- every row, and the system log tables are NOT excluded. Measured on 26.7:
-- the bare form returned both groups (user and system) where the qualified
-- form returns one, and a node with 6 user parts and 408 system parts filled
-- this file's LIMIT 200 with system.metric_log / query_log partitions —
-- pushing out the user tables the file exists to show. Same class as the
-- `pl.` qualification in system.part_log_3_days.sql.
SELECT
    hex(SHA256(concat(hostName(), '%salt%')))       AS hostname,
    hex(SHA256(concat(p.database, '%salt%')))       AS database,
    hex(SHA256(concat(p.table, '%salt%')))          AS table,
    p.partition_id                                  AS partition_id,
    max(p.max_block_number)                         AS max_block,
    max(p.modification_time)                        AS last_visible,
    count()                                         AS active_parts
FROM system.parts AS p
WHERE p.active
  AND p.database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY hostname, database, table, partition_id
ORDER BY last_visible DESC, database, table, partition_id
LIMIT 200
