-- 跳过 system 数据库：system.detached_parts 会列出每张表的 detached/
-- 目录，每张表一个列表（在对象存储磁盘上是一次远程调用，
-- 这些磁盘的元数据存放在 Keeper 中），并且服务器会在列出
-- 之前应用数据库过滤。materialized view（物化视图）的目标
-- （.inner*）属于用户数据，保留。
-- 除大型 SharedMergeTree 服务器上的 auto/full 运行外，{detached_scope} 为 1（所有表）：
-- 此时包括所有非共享表，以及在 broken-part 日志行中点名的共享表
-- （internal/query/detached_scope.go）。
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
