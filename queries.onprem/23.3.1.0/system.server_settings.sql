-- system.server_settings first appeared in 23.3, so this collector
-- lives only in a version directory and has no root counterpart: on an
-- older server the finder skips it instead of failing the run. Gated at
-- 23.3.1.0, the release that added the table: background_*_pool_size moved
-- here from system.settings in 23.3, and system.settings keeps listing the
-- names with their old session defaults, so a 23.3 bundle without this file
-- would read the wrong pool sizes.
--
-- Server-level settings — max_server_memory_usage, the background
-- pools, the paths. system.settings covers the session; this covers
-- the process, and the two answer different questions.
SELECT
    name,
    value,
    `default`,
    changed,
    type
FROM system.server_settings
ORDER BY name
