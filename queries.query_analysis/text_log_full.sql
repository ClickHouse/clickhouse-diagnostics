-- Every text_log row for the focus query_id, in chronological order.
-- Verbose but invaluable when the targeted snapshots above do not
-- surface the issue. Cap at 5000 rows to keep the file size sane.
-- Run when --query-id is set.
--
-- Root variant for the oldest supported servers (22.8+):
-- message_format_string was added in 23.1 — see 23.1.1.0/.
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
FORMAT Native
