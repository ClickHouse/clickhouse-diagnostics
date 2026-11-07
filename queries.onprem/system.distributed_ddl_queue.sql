-- needs: keeper
-- Keeper 中所见的 ON CLUSTER / Replicated 数据库 DDL：每个 (entry, host)
-- 一行，含 status、exception 和耗时。卡住的 DDL（status 在数分钟内
-- 未变为 Finished）、重放 CREATE OR REPLACE 时的 UUID 冲突（code 57）
-- 以及 "session expired" 这类 DDL 失败（code 571/999）只能在这里看到。
-- 读取该表每行要花一次 Keeper 读取；当 Keeper 本身变慢时，
-- LIMIT 和 collector 的 -query-timeout 会对它形成约束。
SELECT
    entry,
    entry_version,
    initiator_host,
    initiator_port,
    cluster,
    leftUTF8(query, 500)          AS query,
    query_create_time,
    host,
    port,
    status,
    exception_code,
    leftUTF8(exception_text, 500) AS exception_text,
    query_finish_time,
    query_duration_ms
FROM system.distributed_ddl_queue
WHERE query_create_time > {from:7d} AND query_create_time <= {to:now}
ORDER BY query_create_time DESC
LIMIT 5000
