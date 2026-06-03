-- Every text_log row for the focus query_id, in chronological order.
-- Verbose but invaluable when the targeted snapshots above do not
-- surface the issue. Cap at 5000 rows to keep the file size sane.
-- Run when --query-id is set.
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
FORMAT Native
