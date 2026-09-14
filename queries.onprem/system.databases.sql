-- One row per database with its engine. The count of Replicated databases is a
-- Keeper-load signal (one DDLWorker, one Keeper watch set and one DDL log per
-- database per replica), and system.tables alone cannot tell Atomic from
-- Replicated. uuid links to data/<uuid>/ paths seen in exception messages.
SELECT
    name,
    engine,
    uuid
FROM system.databases
ORDER BY engine, name
