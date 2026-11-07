-- tier: essential
SELECT
    hostName() AS hostname,
    event_time,
    level,
    logger_name,
    -- 用 leftUTF8 而不是 left：left() 按字节计数，因此截断落在
    -- 多字节字符中间会在 .jsonl 输出中留下无效 UTF-8。
    leftUTF8(message, 500) AS message
FROM clusterAllReplicas(default, system.text_log)
-- event_date 用于裁剪分区；timezone() 把窗口端点转换为
-- 服务器的日历，这样裁剪不会排除午夜附近的行。
-- event_time 随后精确限定窗口——仅按日期过滤会把
-- --from/--to 舍入到整天。
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  -- 在 level Enum8 中 Critical 位于 Fatal 和 Error 之间；“Warning 及
  -- 更严重”缺少它就不完整。
  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
-- 先按严重程度（level Enum 的顺序是 Fatal < Critical < Error < Warning），
-- 再按最新；每个 (level, logger) 最多 200 行，这样一个话多的 logger——
-- 例如每分钟数千条相同 Warning 行——无法占满上限
-- 并掩盖少数重要的 Error 行。LIMIT BY 在 LIMIT 之前执行。
ORDER BY level ASC, event_time DESC
LIMIT 200 BY level, logger_name
LIMIT 2000
