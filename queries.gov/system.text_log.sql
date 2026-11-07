-- tier: essential
SELECT
    event_time,
    level,
    hex(SHA256(concat(logger_name, '%salt%'))) AS logger_name,
    hex(SHA256(concat(left(message, 500), '%salt%'))) AS message
FROM system.text_log
-- event_date 用于裁剪分区；timezone() 把窗口端点转换到
-- 服务器的日历，这样裁剪不会排除午夜附近的行。
-- 随后 event_time 精确限定窗口 —— 仅按日期过滤会把
-- --from/--to 舍入到整天。
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  -- 在 level Enum8 中 Critical 介于 Fatal 和 Error 之间；没有它，
  -- “Warning 及更严重”就不完整。
  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
-- 先按严重程度（level Enum 的顺序是 Fatal < Critical < Error < Warning），
-- 再按最新；每个 (level, logger) 最多 200 行，这样一个话多的 logger ——
-- 例如每分钟数千条相同的 Warning 行 —— 就无法占满上限，
-- 掩盖少数重要的 Error 行。LIMIT BY 先于 LIMIT 执行。
ORDER BY level ASC, event_time DESC
LIMIT 200 BY level, logger_name
LIMIT 2000
