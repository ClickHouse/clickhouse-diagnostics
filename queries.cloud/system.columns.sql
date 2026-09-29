select
    database,
    table,
    name,
    type,
    position,
    default_kind,
    default_expression,
    -- Key membership drives the column colouring in the schema graph
    -- (dashboard) and answers "is this filter column in the key?" in a
    -- bundle. Both flags exist since long before the 22.8 floor.
    is_in_primary_key,
    is_in_sorting_key
FROM system.columns
