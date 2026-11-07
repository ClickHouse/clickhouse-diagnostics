-- tier: essential
-- 服务器崩溃（该表只存在于发生过崩溃的服务器上）。当时正在运行的
-- 查询像每个 gov 标识符一样哈希；堆栈跟踪被排除 —— 它们是构建地址，
-- 判断服务器是否崩溃、何时崩溃并不需要，而且 crash_log_entries
-- 告警只读取这些内容。
SELECT
    event_time,
    signal,
    thread_id,
    hex(SHA256(concat(query_id, '%salt%'))) AS query_id,
    version,
    revision,
    build_id
FROM system.crash_log
ORDER BY event_time DESC
LIMIT 100
