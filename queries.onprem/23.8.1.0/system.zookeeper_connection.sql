-- tier: essential
-- needs: keeper
-- 本服务器连接的是哪个 Keeper 节点，以及连接了多久。
-- session_uptime_elapsed_seconds 远低于服务器 uptime，说明会话是
-- 最近才重新建立的（过期、Keeper leader 变更、网络）；is_expired = 1
-- 表示会话当下已经消失。该表在 23.8 加入——没有根文件，在更低版本上跳过。
SELECT
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
FROM system.zookeeper_connection
