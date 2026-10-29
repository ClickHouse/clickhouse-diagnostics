-- Which Keeper paths took the load over the last day, and which are growing
-- — gov variant. Paths name databases, tables and replicas, so none of that
-- leaves the server: each path is reduced to its SHAPE (segments that are
-- ClickHouse's own Keeper vocabulary are kept, purely numeric ones become N,
-- everything else becomes *) plus path_id, a salted hash of the normalised
-- path that tells two tables of the same shape apart without naming them.
-- The 50 classes with the most requests plus the 50 with the most net creates
-- (creates − removes), one row each. Built from
-- system.aggregated_zookeeper_log (on by default since 25.9).
--   ops         every request on the class, sub-requests inside Multi
--               included; in_multi the part that came inside Multi / MultiRead
--   creates / removes / net_creates   znode growth
--   avg_latency_us  weighted, standalone requests only
--   raw_paths   how many distinct paths the class merges
-- Multi / MultiRead envelopes carry no path and are left out.
-- Operations are compared by Enum16 code, not name — the query check refuses
-- the word CREATE anywhere outside comments: creates = Create (1),
-- CreateIfNotExists (502); removes = Remove (2), RemoveRecursive (503),
-- TryRemove (505); reads = Exists (3), Get (4), List (12), FilteredList
-- (500), FilteredListWithStatsAndData (506), ListRecursive (507).
-- Version-only (26.3+, needs `is_subrequest`): older servers skip this file.
SELECT shape, path_id, ops, in_multi, creates, removes, net_creates, reads, avg_latency_us, raw_paths
FROM
(
    SELECT
        arrayStringConcat(arrayMap(seg -> if(seg = '' OR seg = 'N' OR seg IN (
            'clickhouse', 'tables', 'replicas', 'shared', 'zero_copy', 'zero_copy_s3', 'zero_copy_hdfs', 'mergetree',
            'blocks', 'async_blocks', 'deduplication_hashes', 'block_numbers', 'temp', 'quorum', 'parallel', 'pinned_part_uuids',
            'log', 'queue', 'parts', 'columns', 'metadata', 'mutations', 'leader_election', 'is_active', 'is_lost', 'host',
            'log_pointer', 'flags', 'max_processed_insert_time', 'min_unprocessed_insert_time', 'mutation_pointer',
            'lost_part_count', 'alter_partition_version', 'nonincrement_block_numbers', 'last_part',
            'outdated_parts', 'condemned_parts', 'parts_update_leaders', 'parts_cleanup', 'part_moves_shard',
            '_objects', '_deleted_objects', '_transactions', '_blob_manager', 'dead', '.dir',
            'task_queue', 'ddl', 'active', 'finished', 'synced', 'max_log_ptr', 'counter', 'logs', 'first_replica_database_name',
            'keeper', 'api_version', 'feature_flags', 'session_id'), seg, '*'), splitByChar('/', path)), '/') AS shape,
        hex(SHA256(concat(path, '%salt%')))            AS path_id,
        sum(p_ops)                                     AS ops,
        sum(p_in_multi)                                AS in_multi,
        sum(p_creates)                                 AS creates,
        sum(p_removes)                                 AS removes,
        toInt64(creates) - toInt64(removes)            AS net_creates,
        sum(p_reads)                                   AS reads,
        round(sum(p_lat) / nullIf(sum(p_top_ops), 0))  AS avg_latency_us,
        count()                                        AS raw_paths,
        row_number() OVER (ORDER BY ops DESC)          AS rank_ops,
        row_number() OVER (ORDER BY net_creates DESC)  AS rank_growth
    FROM
    (
        SELECT
            replaceRegexpAll(replaceRegexpAll(parent_path, '/[0-9]+(/|$)', '/N\\1'), '/[0-9]+(/|$)', '/N\\1') AS path,
            p_ops, p_in_multi, p_creates, p_removes, p_reads, p_lat, p_top_ops
        FROM
        (
            SELECT
                parent_path,
                sum(count)                                                              AS p_ops,
                sumIf(count, is_subrequest = 1)                                         AS p_in_multi,
                sumIf(count, toInt16(operation) IN (1, 502))                           AS p_creates,
                sumIf(count, toInt16(operation) IN (2, 503, 505))                      AS p_removes,
                sumIf(count, toInt16(operation) IN (3, 4, 12, 500, 506, 507))          AS p_reads,
                sumIf(average_latency * count, is_subrequest = 0)                       AS p_lat,
                sumIf(count, is_subrequest = 0)                                         AS p_top_ops
            FROM system.aggregated_zookeeper_log
            WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
              AND event_time > {from:1d} AND event_time <= {to:now}
              AND parent_path != ''
            GROUP BY parent_path
        )
    )
    GROUP BY path
)
WHERE rank_ops <= 50 OR rank_growth <= 50
ORDER BY ops DESC
