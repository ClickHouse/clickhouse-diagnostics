-- 本采集器回答的问题：
--   * 一个物化视图“成功”却什么都没写（read_rows > 0、written_rows = 0、
--     exception_code = 0）—— 静默丢行这一类。query_log 里看不到，
--     因为父 INSERT 报告的是成功。
--   * 一次 MV 推送抛错，而基础 part 已经提交，导致基表与 MV 目标
--     不一致（ExceptionWhileProcessing）。
--   * 按视图跟踪内存和耗时 —— 借此发现某次升级让一个 MV 的峰值内存
--     翻了好几倍，而行数却持平。
--   * 通过比较每个桶的 written_rows，发现链中两个视图之间的分叉。
--
-- 脱敏（gov）变体。与 onprem 文件有两处不同：
--   * 不采集异常文本。MV 失败消息会嵌入失败所涉及的标识符
--     ("Unknown table expression identifier 'db.lookup'")，所以只采集
--     数值 exception_code —— 与 part_log 和 query_log 遵循同一规则。
--   * view_name 和 view_target 是 'database.table' 字符串，在哈希前
--     先拆分。PrintGovNameMapping 从 system.tables 构建反向 CSV，所以
--     它分别以 database 和 table 名称为键；对拼接后的字符串做哈希
--     在映射中匹配不到任何东西，分析人员也无法解读。
--
-- 哈希放在外层 select 中、作用于聚合结果，因此 SHA256 每个输出行
-- 只运行一次，而不是每个源行一次。
--
-- 有意不采集 view_uuid：已在 26.7.5.10 上验证 ClickHouse
-- 对每个视图都写入零 UUID，而 system.tables 保存的是真实值。
-- 它只会是一列常量。
SELECT
    time,
    -- view_name / view_target 是服务器打印的 'db.table'
    -- （getFullTableName）：两半在需要时都会加反引号 —— 名为 a.b 的
    -- 数据库会打印成 `a.b`.mv，用 ENGINE 声明的 MV 写入
    -- db.`.inner_id.<uuid>` —— 所以简单地按 '.' 拆分在两个方向上都是错的。
    -- 名称不以引号开头时，分隔符是第一个 '.'；以引号开头时，
    -- 是闭合反引号后面的那个 '.'；然后去掉反引号，使每个被哈希的
    -- 半段都等于 system.tables 中哈希后的 database / name，
    -- 映射 CSV 即可将其反转。
    hex(SHA256(concat(trim(BOTH '`' FROM substring(view_name, 1, if(startsWith(view_name, '`'), position(view_name, '`.'), position(view_name, '.') - 1))), '%salt%'))) AS view_database,
    hex(SHA256(concat(trim(BOTH '`' FROM substring(view_name, if(startsWith(view_name, '`'), position(view_name, '`.') + 2, position(view_name, '.') + 1))), '%salt%')))    AS view_table,
    if(view_target = '', '', hex(SHA256(concat(trim(BOTH '`' FROM substring(view_target, 1, if(startsWith(view_target, '`'), position(view_target, '`.'), position(view_target, '.') - 1))), '%salt%')))) AS target_database,
    if(view_target = '', '', hex(SHA256(concat(trim(BOTH '`' FROM substring(view_target, if(startsWith(view_target, '`'), position(view_target, '`.') + 2, position(view_target, '.') + 1))), '%salt%'))))    AS target_table,
    view_type,
    status,
    exception_code,
    executions,
    zero_write_executions,
    read_rows,
    read_bytes,
    written_rows,
    written_bytes,
    view_duration_ms,
    max_view_duration_ms,
    avg_peak_memory_usage,
    median_peak_memory_usage,
    max_peak_memory_usage,
    minDate,
    maxDate
FROM (
    SELECT
        toStartOfInterval(event_time, toIntervalHour(1)) AS time,
        view_name,
        view_target,
        view_type,
        status,
        exception_code,
        count()                                             AS executions,
        -- 本身并不代表故障：SELECT 带 WHERE 子句的 MV 在大多数块上
        -- 合法地什么都不写。应把它当作执行次数的比例来读。
        countIf(qvl.read_rows > 0 AND qvl.written_rows = 0) AS zero_write_executions,
        sum(qvl.read_rows)                                  AS read_rows,
        sum(qvl.read_bytes)                                 AS read_bytes,
        sum(qvl.written_rows)                               AS written_rows,
        sum(qvl.written_bytes)                              AS written_bytes,
        sum(qvl.view_duration_ms)                           AS view_duration_ms,
        max(qvl.view_duration_ms)                           AS max_view_duration_ms,
        -- 每个聚合参数都特意通过 `qvl.` 别名读取：
        -- `sum(view_duration_ms) AS view_duration_ms` 会把该名称放进查询的
        -- 全局别名作用域，于是后面裸写的 max(view_duration_ms) 会解析成那个
        -- 聚合，服务器会拒绝该查询（ILLEGAL_AGGREGATION 184）。
        avg(qvl.peak_memory_usage)                          AS avg_peak_memory_usage,
        quantile(0.5)(qvl.peak_memory_usage)                AS median_peak_memory_usage,
        max(qvl.peak_memory_usage)                          AS max_peak_memory_usage,
        min(qvl.event_time)                                 AS minDate,
        max(qvl.event_time)                                 AS maxDate
    FROM merge(system, '^query_views_log') AS qvl
    WHERE (event_time > {from:3d} AND event_time <= {to:now})
    -- 显式列出键，而不用 GROUP BY ALL：该语法需要 22.12+，而这个根目录
    -- 文件必须能在所有受支持的服务器（22.8+）上运行。
    GROUP BY time, view_name, view_target, view_type, status, exception_code
)
