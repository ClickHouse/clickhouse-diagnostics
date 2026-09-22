-- Gov: connection name and Keeper host are hashed; index (position in the
-- <zookeeper> node list) and session age stay readable.
SELECT
    hex(SHA256(concat(name, '%salt%'))) AS name,
    hex(SHA256(concat(host, '%salt%'))) AS host,
    index,
    connected_time,
    session_uptime_elapsed_seconds,
    is_expired,
    keeper_api_version
FROM system.zookeeper_connection
