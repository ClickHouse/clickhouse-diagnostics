SELECT
    name,
    code,
    value,
    toString(last_error_time)    AS last_error_time,
    last_error_message,
    left(last_error_trace, 500)  AS last_error_trace
FROM system.errors
WHERE value > 0
ORDER BY value DESC
LIMIT 100
FORMAT Native
