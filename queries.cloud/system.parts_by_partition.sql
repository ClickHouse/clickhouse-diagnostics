-- tier: essential
-- Active parts per partition, for every partition with more than 100 —
-- the count TOO_MANY_PARTS (252) is about. system.parts keeps only the
-- 50 000 largest parts (it is a per-part listing), which drops exactly the
-- small fresh parts that pile up in such a partition, so this file counts
-- them instead: cheap columns only, no part is read. Essential, so the
-- too_many_parts alert still has its source when the run is cut short.
SELECT
    database,
    table,
    partition_id,
    count()           AS parts,
    min(level)        AS min_level,
    sum(rows)         AS rows
FROM system.parts
WHERE active AND database NOT IN ('system', 'information_schema', 'INFORMATION_SCHEMA')
GROUP BY database, table, partition_id
HAVING parts > 100
ORDER BY parts DESC
LIMIT 2000
