-- 定向的 text_log 切片：只保留描述数据
-- 选择过程的消息——查询实际扫描了多少 parts / marks / streams / rows
-- 的问题。回答 "part 剪枝是否有效？" 以及
-- "这是否为全表扫描？"。
-- 当设置了 --query-id 时运行。
--
-- 面向最旧受支持服务器（22.8+）的根变体：
-- message_format_string 在 23.1 新增，因此这里改为按渲染后的
-- 消息文本过滤——精确的格式字符串匹配用于较新服务器，
-- 见 23.1.1.0/。
--
-- 模式锚定在稳定前缀上，并保持开放结尾（末尾的
-- %），这样即使某个 ClickHouse 版本在任一日志行后追加额外字段
-- （它以前扩展过 "Selected … parts" 消息），仍然能够匹配，
-- 而不是静默返回零行。
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
  AND (
    message LIKE 'Selected %parts by partition key%marks to read from%'
    OR message LIKE 'Reading approx. %rows with %streams%'
  )
ORDER BY event_time_microseconds ASC
LIMIT 300
