select
    database,
    table,
    name,
    type,
    position,
    default_kind,
    default_expression,
    -- 是否属于键决定了 schema graph（dashboard）中列的着色，
    -- 并在 bundle 里回答“这个过滤列在键里吗？”。
    -- 这两个标志早在 22.8 下限之前就已存在。
    is_in_primary_key,
    is_in_sorting_key,
    is_in_partition_key,
    is_in_sampling_key
FROM system.columns
