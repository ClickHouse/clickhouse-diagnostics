-- tier: essential
-- Snapshot of async Distributed insert directories. database, table, the
-- shard directory and the sending host are hashed. last_exception is
-- server-generated text that names paths and hosts, so it is hashed too.
--
-- The if(… = '', '', …) guard matters: hashing an empty string yields
-- hex(SHA256(salt)) — a constant 64-char hex on EVERY healthy row —
-- which would destroy the "is this directory erroring at all?" signal.
-- Counts stay readable. last_exception_time (23.3+) is not selected.
SELECT
  hex(SHA256(concat(hostName(), '%salt%'))) AS hostname,
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  hex(SHA256(concat(data_path, '%salt%'))) AS data_path,
  is_blocked,
  error_count,
  data_files,
  data_compressed_bytes,
  broken_data_files,
  broken_data_compressed_bytes,
  if(last_exception = '', '', hex(SHA256(concat(last_exception, '%salt%')))) AS last_exception
FROM system.distribution_queue
