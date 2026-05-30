-- ProfileEvents for the focus query_id, sorted by value descending.
-- This is the single most useful artifact for understanding what the
-- query actually spent its time on (memory, CPU, file I/O, mark cache,
-- S3 reads, network …).
-- Run when --query-id is set.
SELECT
    PE.1                                    AS metric,
    PE.2                                    AS value
FROM {sys.query_log}
ARRAY JOIN ProfileEvents AS PE
WHERE query_id = {query_id}
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
ORDER BY value DESC
LIMIT 1000
FORMAT Native
