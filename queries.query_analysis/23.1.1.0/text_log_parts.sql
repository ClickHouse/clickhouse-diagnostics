-- 23.1+ 变体：按 message_format_string（23.1 新增）过滤，而不是
-- 按渲染后的文本过滤，并在投影中保留该列——这种
-- 精确性正是这一层级的价值所在。
--
-- 使用 LIKE 在稳定前缀上匹配，而不是相等匹配。这一层级服务于
-- 从 23.1 起的每个服务器，因此风险最高：一旦上游
-- 改动任一格式字符串，面板就会静默返回零
-- 行，而零行并不是错误，所以工具和
-- 版本矩阵都不会报告任何内容。（ClickHouse 以前扩展过
-- "Selected … parts" 消息——根文件对渲染文本模式
-- 也保留了同样的推理。）
--
-- 定向的 text_log 切片：只保留描述数据
-- 选择过程的消息——查询实际扫描了多少 parts / marks / streams / rows
-- 的问题。回答 "part 剪枝是否有效？" 以及
-- "这是否为全表扫描？"。
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
  AND (
    message_format_string LIKE 'Selected %parts by partition key%marks to read from%'
    OR message_format_string LIKE 'Reading approx.%rows with%streams%'
  )
ORDER BY event_time_microseconds ASC
LIMIT 300
