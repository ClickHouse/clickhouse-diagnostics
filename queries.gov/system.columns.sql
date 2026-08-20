select
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%'))) AS table,
    hex(SHA256(concat(name, '%salt%'))) AS name,
    type,
    position,
    default_kind
FROM system.columns
