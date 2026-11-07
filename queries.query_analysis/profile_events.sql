-- 目标 query_id 的 ProfileEvents，按 value 降序排列。
-- 这是理解查询实际把时间花在哪里
-- （memory、CPU、文件 I/O、mark cache、S3 读取、网络 …）
-- 最有用的单个产物。
-- 当设置了 --query-id 时运行。
--
-- ProfileEvents 是一个 Map(String, UInt64)。我们 ARRAY JOIN 它的键和
-- 值，而不是 `ARRAY JOIN ProfileEvents AS PE` + PE.1/PE.2：
-- 将 Map array-join 成 tuple 是较新的特性，在 22.8 上
-- 会报错（TYPE_MISMATCH: ARRAY JOIN requires array argument）。mapKeys /
-- mapValues 在全部受支持范围内都可用（22.8 → 最新版）。
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
