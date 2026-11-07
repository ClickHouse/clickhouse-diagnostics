-- 每台副本最近写入的 200 个分区的最新 block 号和最后 part 时间 ——
-- 计算 parts 传递延迟所用的原始数字。在 SharedMergeTree 上，每台
-- 副本维护自己对 system.parts 的视图；某台副本若尚未获取
-- 某个分区最新的 part，它的 max_block_number 会低于同组副本，
-- 而 last_visible 的差距说明持续了多久。块号按分区分配，
-- 所以粒度是分区；按表取最大值会混合不同分区。
--
-- 成本：对 system.parts（内存中）做一次遍历，读取五个窄列，
-- 按分区分组；每台主机 200 行。
-- Gov：这是本节点的视图（该文件为何与完整 parts 转储并存，
-- 见 queries.onprem）。hostname、database 和 table 像每个 gov
-- 文件一样哈希；partition_id 与所有其他 gov parts 文件一样保持原样
-- （携带键的是 `partition` 表达式）。块号、
-- 时间和计数保持清晰：两个盐相同的 gov bundle 仍能按哈希
-- 对应上。
-- WHERE 列上的 `p.` 别名是关键，不是风格问题。ClickHouse 中别名
-- 是全局的，所以哈希投影上的 `AS database` 会把
-- `database` 以那个表达式的形式放进作用域：裸写的 `WHERE database NOT IN
-- ('system', …)` 随后会拿 64 字符哈希与
-- 'system' 比较，对每一行都成立，系统日志表就 NOT 被排除。
-- 在 26.7 上实测：裸写形式返回了用户和系统两类分组，而限定
-- 形式只返回一类；一个只有 6 个用户 part、408 个系统 part 的节点，
-- 用 system.metric_log / query_log 的分区填满了本文件的 LIMIT 200 ——
-- 把文件本来要展示的用户表挤了出去。与
-- system.part_log_3_days.sql 中 `pl.` 限定
-- 属于同一类问题。
SELECT
    hex(SHA256(concat(hostName(), '%salt%')))       AS hostname,
    hex(SHA256(concat(p.database, '%salt%')))       AS database,
    hex(SHA256(concat(p.table, '%salt%')))          AS table,
    p.partition_id                                  AS partition_id,
    max(p.max_block_number)                         AS max_block,
    max(p.modification_time)                        AS last_visible,
    count()                                         AS active_parts
FROM system.parts AS p
WHERE p.active
  AND p.database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY hostname, database, table, partition_id
ORDER BY last_visible DESC, database, table, partition_id
LIMIT 200
