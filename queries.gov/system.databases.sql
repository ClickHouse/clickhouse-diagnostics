-- Gov: the database name is hashed with the same salt as system.tables.database
-- so the two files join; uuid is not collected.
SELECT
    hex(SHA256(concat(name, '%salt%'))) AS name,
    engine
FROM system.databases
ORDER BY engine, name
