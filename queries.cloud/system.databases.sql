-- Databases are identical on every replica; read them once.
SELECT
    name,
    engine,
    uuid
FROM system.databases
ORDER BY engine, name
