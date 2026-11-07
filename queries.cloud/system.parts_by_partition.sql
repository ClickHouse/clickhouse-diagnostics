-- tier: essential
-- 每个分区的活动 part 数，针对所有超过 100 个 part 的分区——
-- TOO_MANY_PARTS (252) 关注的就是这个数量。system.parts 只保留
-- 最大的 50 000 个 part（它是逐 part 列表），会恰好丢掉
-- 在这种分区中堆积的小型新 part，因此本文件改为统计它们：
-- 只使用廉价列，不读取任何 part。标记为 essential，这样
-- 运行被提前截断时 too_many_parts alert 仍有数据源。
SELECT
    database,
    table,
    partition_id,
    count()           AS parts,
    min(level)        AS min_level,
    sum(rows)         AS rows
FROM system.parts
WHERE active AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY database, table, partition_id
HAVING parts > 100
ORDER BY parts DESC
LIMIT 2000
