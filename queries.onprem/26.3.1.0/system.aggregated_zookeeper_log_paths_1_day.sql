-- Which Keeper paths took the load over the last day, and which are growing:
-- the 50 path classes with the most requests plus the 50 with the most net
-- creates (creates − removes), one row each. Built from
-- system.aggregated_zookeeper_log (on by default since 25.9).
--   path        parent path with every purely numeric segment replaced by N,
--               so thousands of children (…/_objects/15536/.dir,
--               …/block_numbers/20260918) add up to one class; table UUIDs
--               and names are kept — they say WHICH table (shard numbers
--               such as /tables/01/ also become N)
--   raw_paths   how many distinct paths the class merges
--   ops         every request on the class, sub-requests inside Multi
--               included (a batched read storm lives there); in_multi the
--               part that came inside Multi / MultiRead
--   creates / removes / net_creates   znode growth: a class with creates far
--               above removes is what fills Keeper's memory and snapshot
--   avg_latency_us  weighted, standalone requests only
-- Cost: one scan of the day, grouped by raw path first (memory is per
-- distinct path, not per row), normalised on the distinct paths only.
-- Multi / MultiRead envelopes carry no path and are left out.
-- Operations are compared by Enum16 code, not name — the query check refuses
-- the word CREATE anywhere outside comments: creates = Create (1),
-- CreateIfNotExists (502); removes = Remove (2), RemoveRecursive (503),
-- TryRemove (505); reads = Exists (3), Get (4), List (12), FilteredList
-- (500), FilteredListWithStatsAndData (506), ListRecursive (507).
-- Version-only (26.3+, needs `is_subrequest`): older servers skip this file.
SELECT path, ops, in_multi, creates, removes, net_creates, reads, avg_latency_us, raw_paths
FROM
(
    SELECT
        path,
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
