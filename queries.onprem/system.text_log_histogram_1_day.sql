-- tier: essential
-- 过去一天中按小时、level 和 logger 类别统计的 Warning 及更严重日志量，
-- 附带一行示例。logger 名称会被归一化成它的类别
-- （"db.t (uuid)(PartsUpdateElection)" → PartsUpdateElection、"DDLWorker(db)" →
-- DDLWorker），这样 25 000 张表不会变成 25 000 行。这个文件回答
-- 错误是从何时开始的、来自哪个组件；system.text_log（2000 行）
-- 只展示最新的行。
SELECT
    toStartOfHour(event_time)          AS time,
    level,
    multiIf(match(logger_name, '\\)\\(([A-Za-z]+)\\)$'),           extract(logger_name, '\\)\\(([A-Za-z]+)\\)$'),
            match(logger_name, '^[A-Za-z0-9_:/]+ ?\\(.*\\)$'),       extract(logger_name, '^([A-Za-z0-9_:/]+) ?\\('),
            match(logger_name, ' \\(([A-Za-z]+)\\)$'),              extract(logger_name, ' \\(([A-Za-z]+)\\)$'),
            logger_name) AS logger_class,
    count()                            AS count,
    anyLast(leftUTF8(message, 300))    AS example
FROM system.text_log
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
GROUP BY time, level, logger_class
ORDER BY time, count DESC
LIMIT 10000
