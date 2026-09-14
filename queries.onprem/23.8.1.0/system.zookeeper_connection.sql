-- Which Keeper node this server is connected to and for how long. A
-- session_uptime_elapsed_seconds far below server uptime means the session was
-- re-established recently (expiry, Keeper leader change, network); is_expired = 1
-- means it is gone right now. Table added in 23.8 — no root file, skipped below.
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
