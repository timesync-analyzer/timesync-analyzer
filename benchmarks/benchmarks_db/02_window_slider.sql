-- =============================================================================
-- РАЗДЕЛ 2: Window Slider — GetOffsets (window_metrics.go)
-- =============================================================================
-- postgres.go:GetOffsets:
--   SELECT time, offset_ns
--   FROM timesync.{table}
--   WHERE node_id = $1 AND time > now() - $2::interval
--   ORDER BY time ASC
-- =============================================================================

SELECT '=== SECTION 2: Window Slider — GetOffsets (hypertable vs plain) ===' AS section;

DO $$
DECLARE
    node_id INTEGER := (SELECT val::INTEGER FROM bench_grf.ctx WHERE key = 'node_id');
    -- [metrics_table_suffix, hypertable_schema, plain_schema]
    tables  TEXT[][] := ARRAY[
        ARRAY['ptp4l_metrics',   'timesync', 'bench_grf'],
        ARRAY['phc2sys_metrics', 'timesync', 'bench_grf'],
        ARRAY['pps_metrics',     'timesync', 'bench_grf']
    ];
    -- observationPeriod: типовые значения из конфига (max tau = 400s)
    periods TEXT[]  := ARRAY['300 seconds', '400 seconds', '10 minutes', '30 minutes', '1 hour'];
    t       TEXT[];
    per     TEXT;
    q_ht    TEXT;
    q_pl    TEXT;
BEGIN
    IF node_id IS NULL THEN
        RAISE NOTICE 'nodes пустая — пропускаю раздел 2';
        RETURN;
    END IF;

    FOREACH t SLICE 1 IN ARRAY tables LOOP
        FOREACH per IN ARRAY periods LOOP
            q_ht := format(
                $q$ SELECT time, offset_ns
                    FROM %I.%I
                    WHERE node_id = %L
                      AND time > now() - INTERVAL %L
                    ORDER BY time ASC $q$,
                t[2], t[1], node_id, per);

            q_pl := format(
                $q$ SELECT time, offset_ns
                    FROM %I.%I
                    WHERE node_id = %L
                      AND time > now() - INTERVAL %L
                    ORDER BY time ASC $q$,
                t[3], t[1], node_id, per);

            INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
            SELECT '2_window_slider',
                   format('get_offsets_%s_%s', replace(t[1], '_metrics', ''), replace(per, ' ', '_')),
                   'hypertable', bq.median_ms, bq.p90_ms, bq.p99_ms,
                   format('GetOffsets, node_id=%s, period=%s', node_id, per)
            FROM bench_grf.bench_query(q_ht) bq;

            INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
            SELECT '2_window_slider',
                   format('get_offsets_%s_%s', replace(t[1], '_metrics', ''), replace(per, ' ', '_')),
                   'plain', bq.median_ms, bq.p90_ms, bq.p99_ms,
                   format('GetOffsets, node_id=%s, period=%s', node_id, per)
            FROM bench_grf.bench_query(q_pl) bq;
        END LOOP;
    END LOOP;
END$$;

