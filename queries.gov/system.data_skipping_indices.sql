-- 每张表的数据跳过索引 —— 脱敏（gov）变体。database、table 和
-- 索引名用与 system.tables 相同的盐哈希；expr 被丢弃，
-- 因为它指名列。type 和 granularity 是 ClickHouse 常量。
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
    hex(SHA256(concat(name, '%salt%')))     AS name,
    type,
    granularity
FROM system.data_skipping_indices
