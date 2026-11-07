-- tier: essential
-- Gov：数据库名用与 system.tables.database 相同的盐哈希，
-- 使两个文件可以关联；不采集 uuid。
SELECT
    hex(SHA256(concat(name, '%salt%'))) AS name,
    engine
FROM system.databases
ORDER BY engine, name
