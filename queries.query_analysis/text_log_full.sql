-- 按时间顺序给出目标 query_id 的每一行 text_log。
-- 内容冗长，但当上面的定向快照无法
-- 暴露问题时非常有价值。上限 5000 行，以免文件过大。
-- 当设置了 --query-id 时运行。
--
-- 面向最旧受支持服务器（22.8+）的根变体：
-- message_format_string 在 23.1 新增——见 23.1.1.0/。
SELECT
    event_time_microseconds                                AS ts,
    thread_id,
    level,
    logger_name,
    message,
    source_file,
    source_line
FROM {sys.text_log}
WHERE query_id = {query_id}
  AND event_time >= {from}
  AND event_time <= {to}
ORDER BY event_time_microseconds ASC
LIMIT 5000
