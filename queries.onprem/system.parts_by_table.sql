-- 每张用户表的活跃 part：数量、行数和大小，每表一行，无上限。
-- system.parts 只保留 50 000 个最大的 part，因此它的求和会
-- 低估每张含大量小 part 的表；dashboard 的 summary、
-- storage-by-database、largest-tables 和 tables-explorer 面板都读取此文件。
-- 只用廉价列：不读取任何 part。在 cloud 中跨 replica 共享。
SELECT
    database,
    table,
    count()                        AS parts,
    sum(rows)                      AS rows,
    sum(bytes_on_disk)             AS bytes_on_disk,
    sum(data_compressed_bytes)     AS data_compressed_bytes,
    sum(data_uncompressed_bytes)   AS data_uncompressed_bytes
FROM system.parts
WHERE active AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY database, table
ORDER BY bytes_on_disk DESC
