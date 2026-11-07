-- 每张表的数据跳过索引：哪些列/表达式拥有 minmax、
-- set、bloom_filter 或 ngram 索引，以及粒度是多少。这是
-- “索引存在但查询没有使用它”（P-53）以及
-- schema graph 索引标记的唯一证据。
--
-- 直接读取，不经过 clusterAllReplicas：这些是定义列，
-- 每个副本都相同，fan-out 会让每个索引按副本重复返回。
-- 按副本的大小列被有意排除。
SELECT
    database,
    table,
    name,
    type,
    expr,
    granularity
FROM system.data_skipping_indices
