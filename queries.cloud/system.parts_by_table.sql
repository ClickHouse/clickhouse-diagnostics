-- 每张用户表的活动 part：数量、行数和大小，每张表一行，
-- 不设上限。system.parts 只保留最大的 50 000 个 part，因此它的汇总
-- 会低估每张拥有许多小 part 的表；dashboard 的 summary、
-- storage-by-database、largest-tables 和 tables-explorer 面板会读取本文件。
-- 只使用廉价列：不读取任何 part。在 cloud 中跨副本共享。
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
