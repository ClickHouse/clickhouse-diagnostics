-- Root variant for the oldest supported servers (22.8–22.10):
-- bytes_on_disk was added in 22.11 — see 22.11.1.0/ (adds bytes_on_disk)
-- and 23.11.1.0/ (also adds modification_time) for newer servers.
-- The system databases are skipped: system.detached_parts lists the
-- detached/ directory of every table, one listing per table (a remote call
-- on object-storage disks whose metadata lives in Keeper), and the server
-- applies a database filter BEFORE listing. Materialized-view targets
-- (.inner*) are user data and stay in.
-- {detached_scope} is 1 (every table) except on a large SharedMergeTree
-- server in an auto/full run: every non-shared table plus the shared tables
-- named in broken-part log lines (internal/query/detached_scope.go).
SELECT
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  partition_id,
  reason,
  min_block_number,
  max_block_number,
  level
-- Gov: p.database is the column; an unqualified `database` here would be the
-- hashed alias above, which matches every row.
FROM system.detached_parts AS p
WHERE p.database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
  AND {detached_scope_p}
