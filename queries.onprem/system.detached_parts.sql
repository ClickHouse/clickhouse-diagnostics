-- Root variant for the oldest supported servers (22.8–22.10):
-- bytes_on_disk and path were added in 22.11 — see
-- 22.11.1.0/system.detached_parts.sql (and 23.11.1.0/ which also adds
-- modification_time) for the richer variants used on newer servers.
SELECT
  database,
  table,
  partition_id,
  disk,
  reason,
  min_block_number,
  max_block_number,
  level
FROM system.detached_parts
FORMAT Native
