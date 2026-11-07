-- tier: essential
-- 最近一天每小时的 Warning 及更严重日志量，按 level 和 logger 类别统计，
-- 附带一行示例。logger 名称被归一到它的类别
-- （"db.t (uuid)(PartsUpdateElection)" → PartsUpdateElection，"DDLWorker(db)" →
-- DDLWorker），这样 25 000 张表不会变成 25 000 行。正是这个文件
-- 说明错误何时开始、来自哪个组件；system.text_log（2000 行）
-- 只显示最新的几行。
--
-- Gov：类别保持可读 —— 它是 ClickHouse 组件名，不是
-- 客户数据。三层守卫覆盖原始标识符可能在归一化后
-- 残留的形状：类别正则接受数字/空格
-- （S3ObjectStorage、"KafkaConsumer 1"），"<disk>::" 前缀被掩码为 "*::"
-- （磁盘名在 gov 其他所有地方都会哈希），而任何仍包含
-- 点的内容（未被任何模式识别的 db.table）会用盐哈希。
-- 示例行始终哈希。
SELECT
    toStartOfHour(event_time)          AS time,
    level,
    if(masked LIKE '%.%', hex(SHA256(concat(masked, '%salt%'))), masked)   AS logger_class,
    count()                            AS count,
    hex(SHA256(concat(anyLast(leftUTF8(message, 300)), '%salt%'))) AS example
FROM
(
    SELECT
        event_time,
        level,
        message,
        replaceRegexpOne(
            multiIf(match(logger_name, '\\)\\(([A-Za-z0-9_ ]+)\\)$'),        extract(logger_name, '\\)\\(([A-Za-z0-9_ ]+)\\)$'),
                    match(logger_name, '^[A-Za-z0-9_:/]+ ?\\(.*\\)$'),          extract(logger_name, '^([A-Za-z0-9_:/]+) ?\\('),
                    match(logger_name, ' \\(([A-Za-z0-9_ ]+)\\)$'),             extract(logger_name, ' \\(([A-Za-z0-9_ ]+)\\)$'),
                    logger_name),
            '^[^:\\s]+::', '*::')                                       AS masked
    FROM system.text_log
    WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
      AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
)
GROUP BY time, level, logger_class
ORDER BY time, count DESC
LIMIT 10000
