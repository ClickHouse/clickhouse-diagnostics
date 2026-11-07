-- tier: essential
-- needs: keeper
-- 24.3 对 23.8 文件的覆盖：增加 xid、last_zxid_seen 和
-- availability_zone。xid 是会话的请求计数器（Int32）；达到 2^31 时
-- 客户端会续期会话，表现为一连串 hardware
-- 异常，以及每个计数器回绕的副本重新连接——在每秒 8–12k
-- 事务下大约每 63 小时一次。(2^31 - xid) /（每秒事务数）
-- 就是余量；alert rule（告警规则）keeper_xid_renewal_due 会计算它。开销很低：
-- 每个 Keeper 连接一行。
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
    xid,
    last_zxid_seen,
    availability_zone,
    enabled_feature_flags
FROM clusterAllReplicas(default, system.zookeeper_connection)
ORDER BY hostname
