-- Side-by-side ProfileEvents for the slowest vs the fastest execution
-- of this normalized_query_hash, plus the percentage delta.
-- This is the single most diagnostic query in the bundle when the
-- question is "why was THIS execution slower than usual?".
-- Run when --normalized-query-hash is set (auto-derived from --query-id).
WITH
    (SELECT argMax(query_id, query_duration_ms) FROM {sys.query_log}
        WHERE normalized_query_hash = {normalized_query_hash}
          AND event_time >= {from} AND event_time <= {to}
          AND type != 'QueryStart'
          AND NOT has(databases, 'system')) AS slow_id,
    (SELECT argMin(query_id, query_duration_ms) FROM {sys.query_log}
        WHERE normalized_query_hash = {normalized_query_hash}
          AND event_time >= {from} AND event_time <= {to}
          AND type != 'QueryStart'
          AND NOT has(databases, 'system')) AS fast_id
SELECT
    PE.1                                                                                  AS metric,
    anyIf(PE.2, query_id = slow_id)                                                       AS slow_value,
    anyIf(PE.2, query_id = fast_id)                                                       AS fast_value,
    toInt64(slow_value) - toInt64(fast_value)                                             AS delta,
    round(
        if(greatest(slow_value, fast_value) > 0,
           ((toFloat64(slow_value) - toFloat64(fast_value))
            / toFloat64(greatest(slow_value, fast_value))) * 100,
           0),
        2
    )                                                                                     AS percentage_diff
FROM {sys.query_log}
ARRAY JOIN ProfileEvents AS PE
WHERE (query_id = slow_id OR query_id = fast_id)
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
GROUP BY metric
ORDER BY abs(delta) DESC, metric ASC
LIMIT 1000
FORMAT Native
