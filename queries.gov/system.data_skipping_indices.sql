-- Data-skipping indices per table — redacted (gov) variant. database, table and
-- index name are hashed with the same salt as system.tables; expr is dropped
-- because it names columns. type and granularity are ClickHouse constants.
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
    hex(SHA256(concat(name, '%salt%')))     AS name,
    type,
    granularity
FROM system.data_skipping_indices
