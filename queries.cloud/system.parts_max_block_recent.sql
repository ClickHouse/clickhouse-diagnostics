-- 每个副本最近写入的 200 个分区的最新 block 编号和最后 part 时间——
-- 计算 parts 传播延迟所用的原始数字。在 SharedMergeTree 上，
-- 每个副本保留自己的 system.parts 视图；尚未 fetch 到某个分区
-- 最新 part 的副本会显示比同类更低的 max_block_number，而
-- last_visible 中的差距说明落后了多久。Block 编号按分区分配，
-- 因此粒度是分区；按表取最大值会混合不同
-- 分区。
--
-- 开销：对 system.parts（内存中）做一次扫描，使用五个窄列，
-- 按分区分组；每台主机 200 行。
-- Cloud：有意 fan-out——cloud collector 原本只从
-- 一个副本读取 system.parts（SharedSystemTables）。LIMIT 200 BY hostname
-- 使每个副本贡献自己的 200 行，最繁忙的副本无法占满
-- 全部上限；因此同一分区每个副本出现一次，
-- 行可直接比较。推导出的延迟在 parts_max_block_by_replica 中。
SELECT
    hostName()               AS hostname,
    database,
    table,
    partition_id,
    max(max_block_number)    AS max_block,
    max(modification_time)   AS last_visible,
    count()                  AS active_parts
FROM clusterAllReplicas(default, system.parts)
WHERE active
  AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY hostname, database, table, partition_id
ORDER BY hostname, last_visible DESC, database, table, partition_id
LIMIT 200 BY hostname
