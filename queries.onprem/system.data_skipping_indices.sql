-- Data-skipping indices per table: which columns/expressions have a minmax,
-- set, bloom_filter or ngram index and at what granularity. The only evidence
-- for "the index exists but the query does not use it" (P-53) and for the
-- schema graph's index markers. Definition columns only — the per-replica
-- size columns are not needed here. Table exists since well before 22.8.
SELECT
    database,
    table,
    name,
    type,
    expr,
    granularity
FROM system.data_skipping_indices
