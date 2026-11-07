-- 该 normalized_query_hash 最慢与最快执行的
-- ProfileEvents 并列对比，以及百分比差值。
-- 当问题为 "为什么这一次执行比平时慢？" 时，
-- 这是 bundle 中最具诊断价值的单个查询。
-- 当设置了 --normalized-query-hash 时运行（由 --query-id 自动推导）。
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
-- ProfileEvents 是一个 Map(String, UInt64)；我们 ARRAY JOIN 它的键和
-- 值，而不是 `ARRAY JOIN ProfileEvents AS PE` + PE.1/PE.2，
-- 后者在 22.8 上会报错（详情见 profile_events.sql）。
SELECT
    metric,
    anyIf(value, query_id = slow_id)                                                      AS slow_value,
    anyIf(value, query_id = fast_id)                                                      AS fast_value,
    toInt64(slow_value) - toInt64(fast_value)                                             AS delta,
    round(
        if(greatest(slow_value, fast_value) > 0,
           ((toFloat64(slow_value) - toFloat64(fast_value))
            / toFloat64(greatest(slow_value, fast_value))) * 100,
           0),
        2
    )                                                                                     AS percentage_diff
FROM {sys.query_log}
ARRAY JOIN
    mapKeys(ProfileEvents)   AS metric,
    mapValues(ProfileEvents) AS value
WHERE (query_id = slow_id OR query_id = fast_id)
  AND event_time >= {from}
  AND event_time <= {to}
  AND type != 'QueryStart'
GROUP BY metric
ORDER BY abs(delta) DESC, metric ASC
LIMIT 1000
