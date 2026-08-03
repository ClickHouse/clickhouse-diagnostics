-- 23.1+ variant: filters on message_format_string (added in 23.1) rather
-- than the rendered text, and keeps the column in the projection — that
-- precision is this rung's value.
--
-- Matched with LIKE on a stable prefix, NOT equality. This rung serves
-- every server from 23.1 upward, so it is where the risk is highest: one
-- upstream edit to either format string would make the panel return zero
-- rows silently, and zero rows is not an error, so neither the tool nor
-- the version matrix would report anything. (ClickHouse has extended the
-- "Selected … parts" message before — the same reasoning the root file
-- carries for its rendered-text patterns.)
--
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
    message_format_string LIKE 'Selected %parts by partition key%marks to read from%'
    OR message_format_string LIKE 'Reading approx.%rows with%streams%'
  )
ORDER BY event_time_microseconds ASC
LIMIT 300
FORMAT Native
