-- tier: essential
-- 每个分区的活跃 part 数，针对所有超过 100 的分区 ——
-- TOO_MANY_PARTS (252) 关心的正是这个计数。system.parts 只保留
-- 最大的 50 000 个 part（它是按 part 列举），恰好会丢掉
-- 在这种分区里堆积的小而新的 part，所以本文件改为对它们
-- 计数：只用廉价列，不读取任何 part。设为 essential，这样运行被
-- 提前截断时 too_many_parts 告警仍有数据来源。
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
    partition_id,
    count()           AS parts,
    min(level)        AS min_level,
    sum(rows)         AS rows
FROM system.parts AS p
WHERE p.active AND p.database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY p.database, p.table, partition_id
HAVING parts > 100
ORDER BY parts DESC
LIMIT 2000
