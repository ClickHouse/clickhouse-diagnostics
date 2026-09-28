select
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%'))) AS table,
    hex(SHA256(concat(name, '%salt%'))) AS name,
    type,
    position,
    default_kind,
    -- Booleans, not identifiers: safe to ship raw.
    is_in_primary_key,
    is_in_sorting_key
FROM system.columns
