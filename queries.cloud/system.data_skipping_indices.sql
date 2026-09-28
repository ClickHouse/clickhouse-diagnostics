-- Data-skipping indices per table: which columns/expressions have a minmax,
-- set, bloom_filter or ngram index and at what granularity. The only evidence
-- for "the index exists but the query does not use it" (P-53) and for the
-- schema graph's index markers.
--
-- Read directly, not through clusterAllReplicas: these are definition columns,
-- identical on every replica, and the fan-out would return each index once per
-- replica. The per-replica size columns are deliberately not selected.
SELECT
    database,
    table,
    name,
    type,
    expr,
    granularity
FROM system.data_skipping_indices
