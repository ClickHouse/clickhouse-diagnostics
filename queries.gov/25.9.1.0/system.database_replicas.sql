-- tier: essential
-- needs: keeper
-- 本服务器上的 Replicated 数据库副本（25.9+）：每个副本是否可写，
-- 以及它是否有 Keeper 会话。这是
-- 对 system.clusters.is_active 的廉价替代，后者会让服务器
-- 向每个 Replicated 数据库副本询问状态。
-- 有意排除 max_log_ptr、log_ptr、total_replicas 和 zookeeper_exception：
-- 请求其中任何一个都会从 Keeper 读取每个数据库的状态
-- （zookeeper_exception 于 2026 年 10 月在上游加入该列表，
-- ClickHouse/ClickHouse a6a695d）。DDL 延迟在
-- system.distributed_ddl_queue。
-- 仅版本目录：该表在 25.9 出现；旧服务器跳过本文件。
SELECT
    hex(SHA256(concat(database, '%salt%')))     AS database,
    is_readonly,
    is_session_expired,
    hex(SHA256(concat(shard_name, '%salt%')))   AS shard_name,
    hex(SHA256(concat(replica_name, '%salt%'))) AS replica_name
FROM system.database_replicas
ORDER BY is_readonly DESC, is_session_expired DESC, database
