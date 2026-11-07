-- 会跳过 system 数据库：system.detached_parts 会列出
-- 每张表的 detached/ 目录，每张表一次列表（在元数据存放在 Keeper 的
-- 对象存储磁盘上，这是一次远程调用），而服务器会在
-- 列出之前先应用 database 过滤。物化视图目标
-- （.inner*）属于用户数据，会保留。
-- {detached_scope} 为 1（所有表），但在大型 SharedMergeTree
-- 服务器的 auto/full 运行中例外：所有非 shared 表，加上在
-- broken-part 日志行中点名的 shared 表（internal/query/detached_scope.go）。
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
FROM system.detached_parts
WHERE database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
  AND {detached_scope}
