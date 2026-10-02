-- 24.3 override of the 23.8 file: adds xid, last_zxid_seen and
-- availability_zone. xid is the session's request counter (Int32); at 2^31
-- the client renews the session, which shows up as a burst of hardware
-- exceptions and a reconnect on every replica whose counter wraps — every
-- ~63 h at 8–12k transactions/s. (2^31 - xid) / (transactions per second)
-- is the headroom; the alert keeper_xid_renewal_due computes it. Cheap:
-- one row per Keeper connection.
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
    xid,
    last_zxid_seen,
    availability_zone,
    enabled_feature_flags
FROM system.zookeeper_connection
