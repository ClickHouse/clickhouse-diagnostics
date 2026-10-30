-- The system databases are skipped: system.detached_parts lists the
-- detached/ directory of every table, one listing per table (a remote call
-- on object-storage disks whose metadata lives in Keeper), and the server
-- applies a database filter BEFORE listing. Materialized-view targets
-- (.inner*) are user data and stay in.
-- {detached_scope} is 1 (every table) except on a large SharedMergeTree
-- server in an auto/full run: every non-shared table plus the shared tables
-- named in broken-part log lines (internal/query/detached_scope.go).
SELECT 
  database,
  table,
  partition_id,
  bytes_on_disk,
  disk,
  path,
  reason,
  min_block_number,
  max_block_number,
  level
FROM clusterAllReplicas(default, system.detached_parts)
WHERE database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
  AND {detached_scope}
