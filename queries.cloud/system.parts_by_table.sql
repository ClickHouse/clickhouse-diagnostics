-- Active parts per user table: count, rows and sizes, one row per table,
-- no cap. system.parts keeps only the 50 000 largest parts, so its sums
-- understate every table with many small parts; the dashboard's summary,
-- storage-by-database, largest-tables and tables-explorer panels read this.
-- Cheap columns only: no part is read. Shared across replicas in cloud.
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
