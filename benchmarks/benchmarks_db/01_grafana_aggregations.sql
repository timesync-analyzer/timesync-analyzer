-- =============================================================================
-- РАЗДЕЛ 1: Grafana dashboard — агрегации по time_bucket / date_trunc
-- =============================================================================
-- Шаблон из sync_info.json:
--   SELECT $__timeGroupAlias(time,'$__interval'), n.hostname, avg(offset_ns)
--   FROM timesync.phc2sys_metrics m
--   JOIN timesync.nodes n ON n.node_id = m.node_id
--   WHERE $__timeFilter(time) AND n.hostname ~ '${node:pipe}' AND n.type = '...'
--   GROUP BY 1, 2 ORDER BY 1
--
-- Hypertable: time_bucket (нативная TS-функция, использует chunk metadata).
-- Plain:      date_trunc  (стандартный PostgreSQL-эквивалент).
-- =============================================================================

SELECT '=== SECTION 1: Grafana aggregation queries (hypertable vs plain) ===' AS section;

DO $$
DECLARE
    node_type TEXT      := (SELECT val FROM bench_grf.ctx WHERE key = 'node_type');
    t_max     TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc');
    -- [window, bucket_label, time_bucket_interval, date_trunc_field]
    windows   TEXT[][] := ARRAY[
        ARRAY['1 hour',   '1m',  '1 minute',  'minute'],
        ARRAY['6 hours',  '5m',  '5 minutes', 'minute'],
        ARRAY['24 hours', '30m', '30 minutes','hour'  ],
        ARRAY['72 hours', '1h',  '1 hour',    'hour'  ]
    ];
    w         TEXT[];
    q_ht      TEXT;
    q_pl      TEXT;
BEGIN
    IF t_max IS NULL THEN
        RAISE NOTICE 'phc2sys_metrics пустая — пропускаю раздел 1 (phc2sys)';
        RETURN;
    END IF;

    FOREACH w SLICE 1 IN ARRAY windows LOOP
        -- 1a. phc2sys: avg offset (базовый grafana-запрос)
        q_ht := format(
            $q$ SELECT time_bucket(%L, m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns) AS "Offset (ns)"
                FROM timesync.phc2sys_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
            w[3], t_max, w[1], t_max, node_type);

        q_pl := format(
            $q$ SELECT date_trunc(%L, m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns) AS "Offset (ns)"
                FROM bench_grf.phc2sys_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
            w[4], t_max, w[1], t_max, node_type);

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '1_grafana', format('phc2sys_avg_%s_bucket_%s', w[1], w[2]),
               'hypertable', bq.median_ms, bq.p90_ms, bq.p99_ms,
               'avg offset_ns, JOIN nodes, type filter'
        FROM bench_grf.bench_query(q_ht) bq;

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '1_grafana', format('phc2sys_avg_%s_bucket_%s', w[1], w[2]),
               'plain', bq.median_ms, bq.p90_ms, bq.p99_ms,
               'avg offset_ns, JOIN nodes, type filter'
        FROM bench_grf.bench_query(q_pl) bq;

        -- 1b. phc2sys: avg + max + stddev + range в одном запросе
        q_ht := format(
            $q$ SELECT time_bucket(%L, m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns)                    AS avg_offset,
                       max(abs(m.offset_ns))                AS max_abs_offset,
                       stddev(m.offset_ns)                  AS stddev_offset,
                       max(m.offset_ns) - min(m.offset_ns)  AS range_offset
                FROM timesync.phc2sys_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
            w[3], t_max, w[1], t_max, node_type);

        q_pl := format(
            $q$ SELECT date_trunc(%L, m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns)                    AS avg_offset,
                       max(abs(m.offset_ns))                AS max_abs_offset,
                       stddev(m.offset_ns)                  AS stddev_offset,
                       max(m.offset_ns) - min(m.offset_ns)  AS range_offset
                FROM bench_grf.phc2sys_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
            w[4], t_max, w[1], t_max, node_type);

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '1_grafana', format('phc2sys_4agg_%s_bucket_%s', w[1], w[2]),
               'hypertable', bq.median_ms, bq.p90_ms, bq.p99_ms,
               'avg+max+stddev+range, JOIN nodes, type filter'
        FROM bench_grf.bench_query(q_ht) bq;

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '1_grafana', format('phc2sys_4agg_%s_bucket_%s', w[1], w[2]),
               'plain', bq.median_ms, bq.p90_ms, bq.p99_ms,
               'avg+max+stddev+range, JOIN nodes, type filter'
        FROM bench_grf.bench_query(q_pl) bq;
    END LOOP;
END$$;

DO $$
DECLARE
    node_type TEXT        := (SELECT val FROM bench_grf.ctx WHERE key = 'node_type');
    t_max     TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_ptp');
    windows   TEXT[][]    := ARRAY[
        ARRAY['1 hour',   '1m',  '1 minute',  'minute'],
        ARRAY['6 hours',  '5m',  '5 minutes', 'minute'],
        ARRAY['24 hours', '30m', '30 minutes','hour'  ],
        ARRAY['72 hours', '1h',  '1 hour',    'hour'  ]
    ];
    w    TEXT[];
    q_ht TEXT;
    q_pl TEXT;
BEGIN
    IF t_max IS NULL THEN
        RAISE NOTICE 'ptp4l_metrics пустая — пропускаю раздел 1 (ptp4l)';
        RETURN;
    END IF;

    FOREACH w SLICE 1 IN ARRAY windows LOOP
        -- 1c. ptp4l: avg + max + stddev + range
        q_ht := format(
            $q$ SELECT time_bucket(%L, m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns)                    AS avg_offset,
                       max(abs(m.offset_ns))                AS max_abs_offset,
                       stddev(m.offset_ns)                  AS stddev_offset,
                       max(m.offset_ns) - min(m.offset_ns)  AS range_offset
                FROM timesync.ptp4l_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
            w[3], t_max, w[1], t_max, node_type);

        q_pl := format(
            $q$ SELECT date_trunc(%L, m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns)                    AS avg_offset,
                       max(abs(m.offset_ns))                AS max_abs_offset,
                       stddev(m.offset_ns)                  AS stddev_offset,
                       max(m.offset_ns) - min(m.offset_ns)  AS range_offset
                FROM bench_grf.ptp4l_metrics m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL %L
                  AND m.time <= %L::timestamptz
                  AND n.type = %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
            w[4], t_max, w[1], t_max, node_type);

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '1_grafana', format('ptp4l_4agg_%s_bucket_%s', w[1], w[2]),
               'hypertable', bq.median_ms, bq.p90_ms, bq.p99_ms,
               'avg+max+stddev+range, JOIN nodes, type filter'
        FROM bench_grf.bench_query(q_ht) bq;

        INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
        SELECT '1_grafana', format('ptp4l_4agg_%s_bucket_%s', w[1], w[2]),
               'plain', bq.median_ms, bq.p90_ms, bq.p99_ms,
               'avg+max+stddev+range, JOIN nodes, type filter'
        FROM bench_grf.bench_query(q_pl) bq;
    END LOOP;
END$$;

