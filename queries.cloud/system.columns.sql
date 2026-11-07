select
    database,
    table,
    name,
    type,
    position,
    default_kind,
    default_expression,
    -- 键成员关系决定 schema graph 中的列着色
    -- （dashboard），并在 bundle 中回答“这个过滤列在键中吗？”
    -- 两个标志早在 22.8 下限之前就存在。
    is_in_primary_key,
    is_in_sorting_key,
    is_in_partition_key,
    is_in_sampling_key
FROM system.columns
