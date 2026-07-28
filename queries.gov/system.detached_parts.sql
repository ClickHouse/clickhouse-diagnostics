-- Root variant for the oldest supported servers (22.8–22.10):
-- bytes_on_disk was added in 22.11 — see 22.11.1.0/ (adds bytes_on_disk)
-- and 23.11.1.0/ (also adds modification_time) for newer servers.
SELECT
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  partition_id,
  reason,
  min_block_number,
  max_block_number,
  level
FROM system.detached_parts
FORMAT Native
