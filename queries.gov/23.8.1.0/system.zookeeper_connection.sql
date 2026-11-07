-- tier: essential
-- needs: keeper
-- Gov：连接名和 Keeper 主机名会被哈希；index（在 <zookeeper> 节点
-- 列表中的位置）和会话时长保持可读。
SELECT
    hex(SHA256(concat(name, '%salt%'))) AS name,
    hex(SHA256(concat(host, '%salt%'))) AS host,
    index,
    connected_time,
    session_uptime_elapsed_seconds,
    is_expired,
    keeper_api_version
FROM system.zookeeper_connection
