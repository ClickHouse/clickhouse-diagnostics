-- tier: essential
SELECT
    event_time,
    level,
    logger_name,
    -- 用 leftUTF8 而不是 left：left() 按字节计数，所以截在多字节
    -- 字符中间会在 .jsonl 输出里留下非法 UTF-8。
    leftUTF8(message, 500) AS message
FROM system.text_log
-- event_date 用于裁掉分区；timezone() 把窗口端点转换成
-- 服务器的日历时间，这样分区裁剪不会排除午夜附近的那些行。
-- 随后再由 event_time 精确限定窗口——只按日期过滤会把
-- --from/--to 四舍五入成整天。
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  -- 在 level Enum8 中 Critical 位于 Fatal 和 Error 之间；“Warning 及
  -- 更严重”不含它就不完整。
  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
-- 先按严重程度（level Enum 的顺序是 Fatal < Critical < Error < Warning），
-- 再按最新排序；每个 (level, logger) 至多 200 行，这样一个话多的 logger——
-- 例如每分钟数千条相同的 Warning——就无法占满上限，
-- 进而掩盖真正重要的少数 Error 行。LIMIT BY 在 LIMIT 之前执行。
ORDER BY level ASC, event_time DESC
LIMIT 200 BY level, logger_name
LIMIT 2000
