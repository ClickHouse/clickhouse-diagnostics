-- 23.12+ 变体：包含 system.tables.total_bytes_uncompressed（23.12 新增）。22.8 基线见根文件。
-- 目标查询涉及的表当前的 DDL + 大小。
-- query_log.tables 数组报告 `database.table` 字符串；我们
-- 按 '.' 拆分，并在 system.tables 中查找每个名称。
-- 当设置了 --query-id 时运行。
WITH tables_for_query AS (
    SELECT
        splitByChar('.', t)[1]                              AS db,
        splitByChar('.', t)[2]                              AS tbl
    FROM {sys.query_log}
    ARRAY JOIN tables AS t
    WHERE query_id = {query_id}
      AND event_time >= {from}
      AND event_time <= {to}
      AND type != 'QueryStart'
      AND t != ''
)
SELECT
    t.database                                              AS database,
    t.name                                                  AS table_name,
    t.engine                                                AS engine,
    t.total_rows                                            AS total_rows,
    formatReadableSize(t.total_bytes)                       AS size,
    formatReadableSize(t.total_bytes_uncompressed)          AS size_uncompressed,
    t.partition_key                                         AS partition_key,
    t.sorting_key                                           AS sorting_key,
    t.storage_policy                                        AS storage_policy,
    t.create_table_query                                    AS create_table_query
FROM {sys.tables} AS t
WHERE (t.database, t.name) IN (
    SELECT db, tbl FROM tables_for_query WHERE db != '' AND tbl != ''
)
ORDER BY t.total_bytes DESC
