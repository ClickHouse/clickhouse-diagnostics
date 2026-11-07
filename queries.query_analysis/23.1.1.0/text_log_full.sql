-- 23.1+ 变体：包含 message_format_string（23.1 新增）。22.8 基线见根文件。
-- 按时间顺序给出目标 query_id 的每一行 text_log。
-- 内容冗长，但当上面的定向快照无法
-- 暴露问题时非常有价值。上限 5000 行，以免文件过大。
-- 当设置了 --query-id 时运行。
SELECT
    event_time_microseconds                                AS ts,
    thread_id,
    level,
    logger_name,
    message,
    message_format_string,
    source_file,
    source_line
FROM {sys.text_log}
WHERE query_id = {query_id}
  AND event_time >= {from}
  AND event_time <= {to}
ORDER BY event_time_microseconds ASC
LIMIT 5000
