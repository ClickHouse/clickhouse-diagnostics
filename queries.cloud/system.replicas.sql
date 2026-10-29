SELECT
    -- Per replica. Every replica keeps its own system.replicas, and on
    -- SharedMergeTree the queue columns are per replica too: inserts_in_queue
    -- is the number of level-0 parts this replica has not fetched yet,
    -- absolute_delay the age of the oldest of them, oldest_part_to_get /
    -- inserts_oldest_time name and date it. Two replicas of the same table
    -- can therefore disagree, and the host is what tells them apart.
    hostName() AS hostname,
    database,
    table,
    is_leader,
    can_become_leader,
    is_readonly,
    is_session_expired,
    -- log_max_index, log_pointer, total_replicas, active_replicas and
    -- zookeeper_exception are not here: asking for any of them makes the
    -- server read every table's status from Keeper (one round trip per
    -- replicated table — minutes on a server with tens of thousands of
    -- tables; StorageSystemReplicas' with_zk_fields). They are in
    -- system.replicas_problem_tables, for the tables that need them.
    future_parts,
    parts_to_check,
    queue_size,
    inserts_in_queue,
    merges_in_queue,
    part_mutations_in_queue,
    toString(queue_oldest_time) AS queue_oldest_time,
    absolute_delay,
    -- The oldest part this replica is still missing and when it was
    -- written: absolute_delay sizes the lag, these two name it, so the same
    -- part showing up as "oldest missing" on several hosts is one finding.
    oldest_part_to_get,
    toString(inserts_oldest_time) AS inserts_oldest_time,
    -- The local queue-update failure; the Keeper-side zookeeper_exception is
    -- in system.replicas_problem_tables (see the note above).
    last_queue_update_exception
FROM clusterAllReplicas(default, system.replicas)
-- Problem rows first, then a bounded sample of healthy ones. The fan-out is
-- tables × replicas: on a self-hosted SharedMergeTree cluster with tens of
-- thousands of tables and ~30 replicas that is ~750 000 rows (≈ 300 MiB) for
-- one file, of which ~3 % carry any signal. Sorting read-only, expired,
-- delayed and queued rows to the top and keeping 200 per host bounds the
-- file at hosts × 200 rows (≈ 2 MiB at 30 hosts) while every problem row
-- survives until a host has more than 200 of them — the pre-pass reads a
-- host at exactly 200 rows as "capped" (the table count itself is in
-- system.tables). A service with fewer than 200 replicated tables per
-- replica is unaffected. {sys.replicas} in alerts and the dashboard reads
-- system.replicas directly and is not bounded by this.
ORDER BY is_readonly DESC, is_session_expired DESC, absolute_delay DESC, inserts_in_queue DESC, database, table
LIMIT 200 BY hostname
