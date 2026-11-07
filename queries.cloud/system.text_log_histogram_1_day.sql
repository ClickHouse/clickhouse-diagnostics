-- tier: essential
-- 过去一天每小时、按 level 和 logger CLASS 统计的 Warning 及更严重日志量，
-- 并附带一条示例行。logger 名称会归一化为其类别
-- ("db.t (uuid)(PartsUpdateElection)" → PartsUpdateElection, "DDLWorker(db)" →
-- DDLWorker），这样 25 000 张表不会变成 25 000 行。本文件
-- 说明错误何时开始、哪个组件；system.text_log（2000 行）
-- 只显示最新的行。
SELECT
    toStartOfHour(event_time)          AS time,
    hostName()                         AS hostname,
    level,
    multiIf(match(logger_name, '\\)\\(([A-Za-z]+)\\)$'),           extract(logger_name, '\\)\\(([A-Za-z]+)\\)$'),
            match(logger_name, '^[A-Za-z0-9_:/]+ ?\\(.*\\)$'),       extract(logger_name, '^([A-Za-z0-9_:/]+) ?\\('),
            match(logger_name, ' \\(([A-Za-z]+)\\)$'),              extract(logger_name, ' \\(([A-Za-z]+)\\)$'),
            logger_name) AS logger_class,
    count()                            AS count,
    anyLast(leftUTF8(message, 300))    AS example
FROM clusterAllReplicas(default, system.text_log)
WHERE event_date >= toDate({from:1d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:1d} AND event_time <= {to:now}
  AND level IN ('Warning', 'Error', 'Critical', 'Fatal')
GROUP BY time, hostname, level, logger_class
ORDER BY time, count DESC
LIMIT 10000
