SELECT
    name,
    code,
    value,
    toString(last_error_time)    AS last_error_time,
    hex(SHA256(concat(last_error_message, '%salt%'))) AS last_error_message,
    '' AS last_error_trace
FROM system.errors
WHERE value > 0
ORDER BY value DESC
LIMIT 100
