-- tier: essential
-- needs: keeper
-- 本服务器上的 Replicated 数据库 replica（25.9+）：每一个是否可写、
-- 是否有 Keeper 会话。这是
-- 对 system.clusters.is_active 的廉价替代，后者会让服务器
-- 向每个 Replicated 数据库的 replica 询问状态。
-- max_log_ptr、log_ptr、total_replicas 和 zookeeper_exception 被特意
-- 排除：请求其中任何一个都会从 Keeper 读取每个数据库的状态
-- （zookeeper_exception 于 2026 年 10 月在上游加入了这个列表，
-- ClickHouse/ClickHouse a6a695d）。DDL 延迟见
-- system.distributed_ddl_queue。
-- 仅版本目录：该表在 25.9 出现；更旧的服务器会跳过这个文件。
SELECT
    database,
    is_readonly,
    is_session_expired,
    shard_name,
    replica_name
FROM system.database_replicas
ORDER BY is_readonly DESC, is_session_expired DESC, database
