-- 目标查询涉及的表当前的 DDL + 大小。
-- query_log.tables 数组报告 `database.table` 字符串；我们
-- 按 '.' 拆分，并在 system.tables 中查找每个名称。
-- 当设置了 --query-id 时运行。
--
-- 面向最旧受支持服务器（22.8+）的根变体：
-- system.tables.total_bytes_uncompressed 在 23.12 新增——包含未压缩大小的
-- 变体见 23.12.1.0/。
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
    -- total_bytes_uncompressed 在 23.12 加入 system.tables；
    -- dashboard 会渲染 size_uncompressed 列，因此在此处发出一个占位符
    -- 以保持该列存在（23.12.1.0/ 变体会填入实际的
    -- 值）。
    'n/a'                                                   AS size_uncompressed,
    t.partition_key                                         AS partition_key,
    t.sorting_key                                           AS sorting_key,
    t.storage_policy                                        AS storage_policy,
    t.create_table_query                                    AS create_table_query
FROM {sys.tables} AS t
WHERE (t.database, t.name) IN (
    SELECT db, tbl FROM tables_for_query WHERE db != '' AND tbl != ''
)
ORDER BY t.total_bytes DESC
