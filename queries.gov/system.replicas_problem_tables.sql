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
-- Gov: names and the Keeper error are hashed in the OUTER query, so the
-- filter below compares real names (an alias named database would otherwise
-- shadow the column inside WHERE).
SELECT
    hex(SHA256(concat(database, '%salt%'))) AS database,
    hex(SHA256(concat(table, '%salt%')))    AS table,
    is_readonly,
    is_session_expired,
    queue_size,
    absolute_delay,
    log_max_index,
    log_pointer,
    total_replicas,
    active_replicas,
    -- Keeper error text names paths and tables, so it is hashed; empty stays
    -- empty, or every healthy row would carry the same hash of the salt.
    if(zookeeper_exception = '', '', hex(SHA256(concat(zookeeper_exception, '%salt%')))) AS zookeeper_exception
FROM
(
    SELECT
        database, table, is_readonly, is_session_expired, queue_size, absolute_delay,
        log_max_index, log_pointer, total_replicas, active_replicas, zookeeper_exception
    FROM system.replicas
    WHERE (database, table) IN
    (
        SELECT database, table
        FROM system.replicas
        ORDER BY (is_readonly OR is_session_expired OR queue_size > 0 OR absolute_delay > 0
                  OR future_parts > 0 OR parts_to_check > 0) DESC,
                 absolute_delay DESC, database, table
        LIMIT 200
    )
)
ORDER BY absolute_delay DESC, database, table
