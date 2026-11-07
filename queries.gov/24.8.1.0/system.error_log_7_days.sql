-- tier: essential
-- system.error_log（24.8+）：system.errors 背后的历史 —— 服务器中
-- 任何地方抛出的每个错误码（查询以及后台 merge、fetch、
-- Keeper 客户端、DDL worker），按分钟记录，这里按小时汇总。正是这个
-- 文件把 999 / 242 / 252 / 107 放到时间线上，即使 query_log
-- 从未看到它们。remote = 1 表示该错误来自另一台服务器。
SELECT
    toStartOfHour(event_time) AS time,
    code,
    error,
    remote,
    sum(value)                AS errors
FROM system.error_log
WHERE event_date >= toDate({from:7d}, timezone()) AND event_date <= toDate({to:now}, timezone())
  AND event_time > {from:7d} AND event_time <= {to:now}
GROUP BY time, code, error, remote
-- 依赖 Keeper 的错误码排在前面，所以上限永远不会截掉它们（
-- dashboard 的 Keeper 面板和 Keeper 健康检查会读取它们；最多
-- 168 小时 x 5 个错误码 x 2 个 remote 值 = 1680 行）：嘈杂服务器上
-- 其他错误码会因上限丢掉最近几小时，Keeper 序列则永远不会。
ORDER BY code IN (999, 242, 319, 571, 252) DESC, time, errors DESC
LIMIT 20000
