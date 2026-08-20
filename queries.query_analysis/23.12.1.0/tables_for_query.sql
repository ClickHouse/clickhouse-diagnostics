-- 23.12+ variant: includes system.tables.total_bytes_uncompressed (added in 23.12). See root file for the 22.8 baseline.
-- Current DDL + size for the tables the focus query touched.
-- The query_log.tables array reports `database.table` strings; we
-- split on '.' and look up each name in system.tables.
-- Run when --query-id is set.
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
