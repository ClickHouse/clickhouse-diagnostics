-- 每个 replica 最近写入的 200 个分区中，各自最新的 block 编号与最后的
-- part 时间——计算 parts 传播延迟时所用的原始数字。
-- 在 SharedMergeTree 上，每个 replica 保留自己对 system.parts 的视图；
-- 尚未拉取到某分区最新 part 的 replica，其 max_block_number 会低于
-- 同组兄弟，而 last_visible 的差距说明了落后多久。
-- block 编号按分区分配，所以粒度就是分区；按表取 max 会混掉分区。
--
-- 成本：对 system.parts（内存表）做一次扫描，只取五个窄列，
-- 按分区聚合；每台主机 200 行。
-- Onprem：只反映本节点的视图——单个节点没有可比对的对象，
-- 因此需要在相隔数分钟从两个 replica 采集的两个 bundle 上，
-- 由 skill 的预检按 (database, table, partition_id) 对齐。完整的
-- system.parts dump 保留 50 000 个 LARGEST parts；
-- 在 part 数量超过这个数的节点上，
-- 它丢掉的恰恰是那些能显示延迟的、新插入的小 part，
-- 这正是旁边还要有这份按分区聚合、按新近排序的文件的原因。
SELECT
    hostName()               AS hostname,
    database,
    table,
    partition_id,
    max(max_block_number)    AS max_block,
    max(modification_time)   AS last_visible,
    count()                  AS active_parts
FROM system.parts
WHERE active
  AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY hostname, database, table, partition_id
ORDER BY last_visible DESC, database, table, partition_id
LIMIT 200
