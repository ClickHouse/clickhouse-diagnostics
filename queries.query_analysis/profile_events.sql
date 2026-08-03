-- ProfileEvents for the focus query_id, sorted by value descending.
-- This is the single most useful artifact for understanding what the
-- query actually spent its time on (memory, CPU, file I/O, mark cache,
-- S3 reads, network …).
-- Run when --query-id is set.
--
-- ProfileEvents is a Map(String, UInt64). We ARRAY JOIN its keys and
-- values rather than `ARRAY JOIN ProfileEvents AS PE` + PE.1/PE.2:
-- array-joining a Map into a tuple is a newer feature and errors on
-- 22.8 (TYPE_MISMATCH: ARRAY JOIN requires array argument). mapKeys /
-- mapValues works across the whole supported range (22.8 → latest).
SELECT
    metric,
    value
FROM {sys.query_log}
ARRAY JOIN
    mapKeys(ProfileEvents)   AS metric,
    mapValues(ProfileEvents) AS value
WHERE query_id = {query_id}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
ORDER BY value DESC
LIMIT 1000
FORMAT Native
