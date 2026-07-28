-- 22.11+ variant: includes bytes_on_disk (added to system.detached_parts
-- in 22.11). See the root file for the 22.8–22.10 baseline, and
-- 23.11.1.0/ which additionally adds modification_time.
SELECT
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  partition_id,
  bytes_on_disk,
  reason,
  min_block_number,
  max_block_number,
  level
FROM system.detached_parts
FORMAT Native
