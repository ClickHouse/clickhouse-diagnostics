-- tier: essential
-- needs: keeper
-- 每个副本的 Keeper 连接：固定连接到单个 Keeper 主机的副本，或
-- 会话比同类新得多的副本，就是丢失会话的副本。
SELECT
    hostName() AS hostname,
    name,
    host,
    port,
    index,
    connected_time,
    session_uptime_elapsed_seconds,
    is_expired,
    keeper_api_version,
    client_id,
    enabled_feature_flags
FROM clusterAllReplicas(default, system.zookeeper_connection)
ORDER BY hostname
