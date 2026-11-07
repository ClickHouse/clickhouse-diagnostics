-- tier: essential
-- 数据库在每个副本上都相同；只读取一次。
SELECT
    name,
    engine,
    uuid
FROM system.databases
ORDER BY engine, name
