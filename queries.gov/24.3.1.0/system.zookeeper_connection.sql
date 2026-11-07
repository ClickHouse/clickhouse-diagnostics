-- tier: essential
-- needs: keeper
-- 24.3 对 23.8 文件的覆盖：增加 xid、last_zxid_seen 和
-- availability_zone。xid 是会话的请求计数器（Int32）；到 2^31
-- 客户端会续期会话，表现为一批硬件异常以及每个计数器回绕的副本
-- 重连 —— 在 8–12k 事务/秒下大约每 ~63 小时一次。
-- (2^31 - xid) / （每秒事务数）就是余量；
-- alert keeper_xid_renewal_due 会计算它。代价很低：
-- 每个 Keeper 连接一行。
-- Gov：name 和 host 像 23.8 文件一样哈希；availability_zone 指名
-- 客户基础设施，也会哈希；xid 和 last_zxid_seen 是
-- 计数器。
SELECT
    hex(SHA256(concat(name, '%salt%'))) AS name,
    hex(SHA256(concat(host, '%salt%'))) AS host,
    port,
    index,
    connected_time,
    session_uptime_elapsed_seconds,
    is_expired,
    keeper_api_version,
    xid,
    last_zxid_seen,
    if(availability_zone = '', '', hex(SHA256(concat(availability_zone, '%salt%')))) AS availability_zone
FROM system.zookeeper_connection
