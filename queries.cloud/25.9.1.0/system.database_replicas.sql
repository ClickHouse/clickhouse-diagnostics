-- tier: essential
-- needs: keeper
-- 服务中每个副本上的 Replicated database 副本（25.9+）：每个是否可写，
-- 以及是否拥有 Keeper 会话。这是
-- system.clusters.is_active 的廉价替代方案，后者会让服务器
-- 向每个 Replicated database 副本询问状态。
-- max_log_ptr、log_ptr、total_replicas 和 zookeeper_exception 被有意排除：
-- 请求其中任何一个都会从
-- Keeper 读取每个数据库的状态（zookeeper_exception 于 2026 年 10 月在上游加入该列表，
-- ClickHouse/ClickHouse a6a695d）。DDL 延迟见
-- system.distributed_ddl_queue。
-- 仅限特定版本：该表在 25.9 出现；更旧的服务器会跳过本文件。
SELECT
    hostName() AS hostname,
    database,
    is_readonly,
    is_session_expired,
    shard_name,
    replica_name
FROM clusterAllReplicas(default, system.database_replicas)
ORDER BY hostname, is_readonly DESC, is_session_expired DESC, database
