-- tier: essential
-- needs: keeper
-- The Keeper-backed columns of system.replicas, for the tables that need
-- them. Asking system.replicas for log_max_index, log_pointer,
-- total_replicas, active_replicas or zookeeper_exception makes the server
-- read EVERY table's status from Keeper — one round trip per replicated
-- table, minutes on a server with tens of thousands of tables (seen at
-- 18–97 s per run, plus the same again in alerts). system.replicas filters
-- on database/table BEFORE reading statuses, so this file asks for them only
-- for up to 200 tables chosen by the cheap columns: every table that is
-- read-only, session-expired, queued, delayed or checking parts first, then
-- a sample of healthy ones — enough to see a stale replica still registered
-- in Keeper (total_replicas above the servers that exist, P-43) and a
-- replica lagging the log (log_pointer far behind log_max_index).
-- The cheap columns for every table are in system.replicas.
-- Cloud: per replica. The IN subquery has no GLOBAL, so each replica picks
-- its own up-to-200 tables from its own system.replicas.
SELECT
    hostName() AS hostname,
    database,
    table,
    is_readonly,
    is_session_expired,
    queue_size,
    absolute_delay,
    log_max_index,
    log_pointer,
    total_replicas,
    active_replicas,
    zookeeper_exception
FROM clusterAllReplicas(default, system.replicas)
WHERE (database, table) IN
(
    SELECT database, table
    FROM system.replicas
    ORDER BY (is_readonly OR is_session_expired OR queue_size > 0 OR absolute_delay > 0
              OR future_parts > 0 OR parts_to_check > 0) DESC,
             absolute_delay DESC, database, table
    LIMIT 200
)
ORDER BY hostname, absolute_delay DESC, database, table
