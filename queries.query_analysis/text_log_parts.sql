-- Targeted text_log slice: only the messages that describe data
-- selection — how many parts / marks / streams / rows the query
-- actually scanned. Answers "is the part-pruning effective?" and
-- "is this a full table scan?".
-- Run when --query-id is set.
--
-- Root variant for the oldest supported servers (22.8+):
-- message_format_string was added in 23.1, so this filters on the
-- rendered message text instead — see 23.1.1.0/ for the exact
-- format-string match used on newer servers.
--
-- Patterns are anchored on a stable prefix and left open-ended (trailing
-- %) so a ClickHouse version that appends extra fields to either log line
-- (it has extended the "Selected … parts" message before) still matches
-- rather than silently returning zero rows.
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
