-- 每张表的数据跳过索引：哪些列/表达式建有 minmax、
-- set、bloom_filter 或 ngram 索引，以及粒度是多少。这是判断
-- “索引存在但查询没用上”（P-53）以及供 schema graph 标记索引的
-- 唯一证据。只取定义列——每个 replica 的大小列在这里用不到。
-- 该表早在 22.8 之前就已存在。
SELECT
    database,
    table,
    name,
    type,
    expr,
    granularity
FROM system.data_skipping_indices
