-- tier: essential
-- 每个分区的活跃 part 数，只列出超过 100 个的分区——
-- TOO_MANY_PARTS (252) 关注的正是这个数量。system.parts 只保留
-- 50 000 个最大的 part（它是逐 part 列表），恰好会丢掉在这种分区里
-- 越积越多的小而新的 part，所以这个文件改为自己计数：
-- 只用廉价列，不读取任何 part。标记为 essential，这样即使采集
-- 被提前中断，too_many_parts 告警仍然有数据来源。
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
