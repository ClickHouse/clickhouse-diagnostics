-- 面向最旧受支持服务器（22.8–22.10）的根目录变体：
-- bytes_on_disk 于 22.11 加入 —— 更新服务器见 22.11.1.0/
-- （增加 bytes_on_disk）和 23.11.1.0/（还增加 modification_time）。
-- 会跳过 system 库：system.detached_parts 会列出每张表的
-- detached/ 目录，每张表一次列举（在元数据存放在 Keeper 的对象存储
-- 磁盘上是一次远程调用），而服务器会在列举之前应用数据库过滤。
-- 物化视图目标（.inner*）属于用户数据，保留。
-- {detached_scope} 为 1（所有表），例外是大规模
-- SharedMergeTree 服务器在 auto/full 运行时：所有非共享表加上
-- broken-part 日志行中点名的共享表
-- （internal/query/detached_scope.go）。
SELECT
  hex(SHA256(concat(database, '%salt%'))) AS database,
  hex(SHA256(concat(table, '%salt%'))) AS table,
  partition_id,
  reason,
  min_block_number,
  max_block_number,
  level
-- Gov：p.database 才是列；这里若用不带限定的 `database`，指的会是上面的
-- 哈希别名，从而匹配每一行。
FROM system.detached_parts AS p
WHERE p.database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
  AND {detached_scope_p}
