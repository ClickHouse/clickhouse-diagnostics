-- tier: essential
-- Each node keeps its own shard directories on local disk, so the queue is
-- read from every replica. hostName() says which one. Snapshot: no time
-- window. last_exception_time (23.3+) is not selected.
SELECT
  hostName() AS hostname,
  database,
  table,
  data_path,
  is_blocked,
  error_count,
  data_files,
  data_compressed_bytes,
  broken_data_files,
  broken_data_compressed_bytes,
  last_exception
FROM clusterAllReplicas(default, system.distribution_queue)
