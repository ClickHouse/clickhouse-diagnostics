-- Keeper connection per replica: replicas pinned to one Keeper host, or with a
-- much younger session than their peers, are the ones that lost the session.
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
