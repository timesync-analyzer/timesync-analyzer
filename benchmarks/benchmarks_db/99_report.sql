-- =============================================================================
-- ИТОГОВЫЕ ТАБЛИЦЫ
-- =============================================================================

SELECT '=== RESULTS: Section 1 — Grafana aggregations ===' AS report;
SELECT
    scenario,
    round(ht.median_ms, 2)                                      AS ht_median_ms,
    round(pl.median_ms, 2)                                      AS pl_median_ms,
    round(pl.median_ms / NULLIF(ht.median_ms, 0), 2)            AS speedup_x,
    ht.note
FROM
    (SELECT scenario, median_ms, note FROM bench_grf.results
     WHERE section = '1_grafana' AND kind = 'hypertable') ht
    JOIN
    (SELECT scenario, median_ms       FROM bench_grf.results
     WHERE section = '1_grafana' AND kind = 'plain') pl
    USING (scenario)
ORDER BY scenario;

SELECT '=== RESULTS: Section 2 — Window Slider GetOffsets ===' AS report;
SELECT
    scenario,
    round(ht.median_ms, 2)                           AS ht_median_ms,
    round(pl.median_ms, 2)                           AS pl_median_ms,
    round(pl.median_ms / NULLIF(ht.median_ms, 0), 2) AS speedup_x,
    ht.note
FROM
    (SELECT scenario, median_ms, note FROM bench_grf.results
     WHERE section = '2_window_slider' AND kind = 'hypertable') ht
    JOIN
    (SELECT scenario, median_ms       FROM bench_grf.results
     WHERE section = '2_window_slider' AND kind = 'plain') pl
    USING (scenario)
ORDER BY scenario;

SELECT '=== RESULTS: Section 3 — Quality metrics ===' AS report;
SELECT
    scenario,
    round(ht.median_ms, 2)                           AS ht_median_ms,
    round(pl.median_ms, 2)                           AS pl_median_ms,
    round(pl.median_ms / NULLIF(ht.median_ms, 0), 2) AS speedup_x,
    ht.note
FROM
    (SELECT scenario, median_ms, note FROM bench_grf.results
     WHERE section = '3_quality' AND kind = 'hypertable') ht
    JOIN
    (SELECT scenario, median_ms       FROM bench_grf.results
     WHERE section = '3_quality' AND kind = 'plain') pl
    USING (scenario)
ORDER BY scenario;

SELECT '=== RESULTS: Section 4 — Anti-patterns ===' AS report;
SELECT
    scenario,
    round(ht.median_ms, 2)                           AS ht_median_ms,
    round(pl.median_ms, 2)                           AS pl_median_ms,
    round(pl.median_ms / NULLIF(ht.median_ms, 0), 2) AS speedup_x,
    ht.note
FROM
    (SELECT scenario, median_ms, note FROM bench_grf.results
     WHERE section = '4_antipattern' AND kind = 'hypertable') ht
    JOIN
    (SELECT scenario, median_ms       FROM bench_grf.results
     WHERE section = '4_antipattern' AND kind = 'plain') pl
    USING (scenario)
ORDER BY scenario;

SELECT '=== RESULTS: Section 5 — Bucket sweep and interval statistics ===' AS report;
SELECT
    scenario,
    round(ht.median_ms, 2)                           AS ht_median_ms,
    round(pl.median_ms, 2)                           AS pl_median_ms,
    round(pl.median_ms / NULLIF(ht.median_ms, 0), 2) AS speedup_x,
    ht.note
FROM
    (SELECT scenario, median_ms, note FROM bench_grf.results
     WHERE section = '5_bucket_sweep' AND kind = 'hypertable') ht
    JOIN
    (SELECT scenario, median_ms       FROM bench_grf.results
     WHERE section = '5_bucket_sweep' AND kind = 'plain') pl
    USING (scenario)
ORDER BY speedup_x DESC NULLS LAST, scenario;

SELECT '=== RESULTS: Section 6 — Reverse index on compressed Timescale copies ===' AS report;
SELECT
    scenario,
    round(no_rev.median_ms, 2)                                      AS no_reverse_ms,
    round(with_rev.median_ms, 2)                                    AS with_reverse_ms,
    round(no_rev.median_ms / NULLIF(with_rev.median_ms, 0), 2)      AS reverse_index_speedup_x,
    round(no_rev.p90_ms, 2)                                         AS no_reverse_p90_ms,
    round(with_rev.p90_ms, 2)                                       AS with_reverse_p90_ms,
    no_rev.note
FROM
    (SELECT scenario, median_ms, p90_ms, note FROM bench_grf.results
     WHERE section = '6_reverse_index_timescale' AND kind = 'timescale_no_reverse') no_rev
    JOIN
    (SELECT scenario, median_ms, p90_ms FROM bench_grf.results
     WHERE section = '6_reverse_index_timescale' AND kind = 'timescale_with_reverse') with_rev
    USING (scenario)
ORDER BY reverse_index_speedup_x DESC NULLS LAST, scenario;

-- Сводка: топ-10 случаев, где hypertable быстрее plain (speedup_x > 1)
SELECT '=== TOP-10: hypertable wins ===' AS report;
SELECT
    ht.section,
    ht.scenario,
    round(ht.median_ms, 2)                           AS ht_ms,
    round(pl.median_ms, 2)                           AS pl_ms,
    round(pl.median_ms / NULLIF(ht.median_ms, 0), 2) AS speedup_x
FROM bench_grf.results ht
JOIN bench_grf.results pl USING (section, scenario)
WHERE ht.kind = 'hypertable' AND pl.kind = 'plain'
ORDER BY speedup_x DESC NULLS LAST
LIMIT 10;

-- Сводка: случаи, где plain быстрее hypertable (speedup_x < 1)
SELECT '=== Cases where plain PG outperforms hypertable ===' AS report;
SELECT
    ht.section,
    ht.scenario,
    round(ht.median_ms, 2)                           AS ht_ms,
    round(pl.median_ms, 2)                           AS pl_ms,
    round(pl.median_ms / NULLIF(ht.median_ms, 0), 2) AS speedup_x
FROM bench_grf.results ht
JOIN bench_grf.results pl USING (section, scenario)
WHERE ht.kind = 'hypertable' AND pl.kind = 'plain'
  AND pl.median_ms < ht.median_ms
ORDER BY speedup_x ASC;

-- =============================================================================
-- Очистка (раскомментировать после снятия результатов)
-- =============================================================================
-- DROP SCHEMA bench_grf CASCADE;
