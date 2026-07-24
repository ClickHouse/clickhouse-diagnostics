-- 23.1+ variant: filters on message_format_string exactly (added in 23.1); the root file falls back to message LIKE patterns.
-- Targeted text_log slice: only the messages that describe data
-- selection — how many parts / marks / streams / rows the query
-- actually scanned. Answers "is the part-pruning effective?" and
-- "is this a full table scan?".
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
  AND (
    message_format_string = 'Selected {}/{} parts by partition key, {} parts by primary key, {}/{} marks by primary key, {} marks to read from {} ranges'
    OR message_format_string = 'Reading approx. {} rows with {} streams'
  )
ORDER BY event_time_microseconds ASC
LIMIT 300
FORMAT Native
