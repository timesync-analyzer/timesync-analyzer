-- =============================================================================
-- РАЗДЕЛ 5: Sweep по временным окнам и bucket interval
-- =============================================================================
-- Цель: отдельно замерить влияние размера окна и размера bucket на запросы
-- Grafana-агрегаций.
--
-- 5a. Для каждой пары [window, bucket] считается avg(offset_ns).
-- 5b. Для каждого window отдельно считается min/max/avg/stddev/range без bucket.
--
-- Hypertable: time_bucket(...), таблица timesync.phc2sys_metrics.
-- Plain:      date_bin(...), таблица bench_grf.phc2sys_metrics.
-- =============================================================================

SELECT '=== SECTION 5: Bucket sweep and interval statistics ===' AS section;

DO $$
DECLARE
    node_type TEXT        := (SELECT val FROM bench_grf.ctx WHERE key = 'node_type');
    t_max     TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc');
    windows   TEXT[][]    := ARRAY[
        ARRAY['5 minutes',  '5m'],
        ARRAY['15 minutes', '15m'],
        ARRAY['30 minutes', '30m'],
        ARRAY['1 hour',     '1h'],
        ARRAY['3 hours',    '3h'],
        ARRAY['6 hours',    '6h'],
        ARRAY['12 hours',   '12h'],
        ARRAY['1 day',      '1d'],
        ARRAY['3 days',     '3d']
    ];
    buckets   TEXT[][]    := ARRAY[
        ARRAY['1 second',   '1s'],
        ARRAY['5 seconds',  '5s'],
        ARRAY['1 minute',   '1m'],
        ARRAY['5 minutes',  '5m'],
        ARRAY['15 minutes', '15m'],
        ARRAY['30 minutes', '30m'],
        ARRAY['1 hour',     '1h'],
        ARRAY['3 hours',    '3h'],
        ARRAY['6 hours',    '6h'],
        ARRAY['12 hours',   '12h']
    ];
    win       TEXT[];
    bucket    TEXT[];
    scenario  TEXT;
    note      TEXT;
    q_ht      TEXT;
    q_pl      TEXT;
BEGIN
    IF t_max IS NULL THEN
        RAISE NOTICE 'phc2sys_metrics пустая — пропускаю раздел 5';
        RETURN;
    END IF;

    -- 5a. Матрица window x bucket: avg(offset_ns)
    FOREACH win SLICE 1 IN ARRAY windows LOOP
        FOREACH bucket SLICE 1 IN ARRAY buckets LOOP
            scenario := format('avg_%s_bucket_%s', win[2], bucket[2]);
            note := format('avg offset_ns, window=%s, bucket=%s, JOIN nodes, type filter',
                           win[2], bucket[2]);

            q_ht := format($q$
                SELECT time_bucket(INTERVAL %L, m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns) AS avg_offset
                FROM timesync.phc2sys_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                bucket[1], t_max, win[1], t_max, node_type);

            q_pl := format($q$
                SELECT date_bin(INTERVAL %L, m.time, TIMESTAMPTZ '1970-01-01') AS bucket,
                       n.hostname,
                       avg(m.offset_ns) AS avg_offset
                FROM bench_grf.phc2sys_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                bucket[1], t_max, win[1], t_max, node_type);

            INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
            SELECT '5_bucket_sweep', scenario, 'hypertable',
                   bq.median_ms, bq.p90_ms, bq.p99_ms, note
            FROM bench_grf.bench_query(q_ht) bq;

            INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
            SELECT '5_bucket_sweep', scenario, 'plain',
                   bq.median_ms, bq.p90_ms, bq.p99_ms, note
            FROM bench_grf.bench_query(q_pl) bq;
        END LOOP;
    END LOOP;

    -- 5b. По каждому window: min/max/avg/stddev/range(offset_ns), без bucket.
    FOREACH win SLICE 1 IN ARRAY windows LOOP
        scenario := format('stats_%s', win[2]);
        note := format('min+max+avg+stddev+range offset_ns, window=%s, no bucket, JOIN nodes, type filter',
                       win[2]);

        q_ht := format($q$
            SELECT n.hostname,
                   min(m.offset_ns) AS min_offset,
                   max(m.offset_ns) AS max_offset,
                   avg(m.offset_ns) AS avg_offset,
                   stddev(m.offset_ns) AS stddev_offset,
                   max(m.offset_ns) - min(m.offset_ns) AS range_offset
            FROM timesync.phc2sys_metrics m
            JOIN timesync.nodes n ON n.node_id = m.node_id
            WHERE m.time > %L::timestamptz - INTERVAL %L
              AND m.time <= %L::timestamptz
              AND n.type = %L
            GROUP BY 1
            ORDER BY 1 $q$,
            t_max, win[1], t_max, node_type);

        q_pl := format($q$
            SELECT n.hostname,
                   min(m.offset_ns) AS min_offset,
                   max(m.offset_ns) AS max_offset,
                   avg(m.offset_ns) AS avg_offset,
                   stddev(m.offset_ns) AS stddev_offset,
                   max(m.offset_ns) - min(m.offset_ns) AS range_offset
            FROM bench_grf.phc2sys_metrics m
            JOIN timesync.nodes n ON n.node_id = m.node_id
            WHERE m.time > %L::timestamptz - INTERVAL %L
              AND m.time <= %L::timestamptz
              AND n.type = %L
            GROUP BY 1
            ORDER BY 1 $q$,
            t_max, win[1], t_max, node_type);

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '5_bucket_sweep', scenario, 'hypertable',
               bq.median_ms, bq.p90_ms, bq.p99_ms, note
        FROM bench_grf.bench_query(q_ht) bq;

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '5_bucket_sweep', scenario, 'plain',
               bq.median_ms, bq.p90_ms, bq.p99_ms, note
        FROM bench_grf.bench_query(q_pl) bq;
    END LOOP;
END$$;
