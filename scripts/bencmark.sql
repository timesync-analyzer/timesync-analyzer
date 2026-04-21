-- =============================================================================
-- Benchmark: Grafana dashboard queries + Window Slider — hypertable vs plain PG
-- =============================================================================
-- Измеряет реальные профили запросов из продакшна и сравнивает TimescaleDB
-- hypertable (со сжатием) с обычными PostgreSQL-таблицами с теми же индексами.
--
--   Раздел 1: Grafana-агрегации (avg / max / stddev / range + time_bucket + JOIN)
--   Раздел 2: GetOffsets из window_metrics.go (MetricsWindowSlider)
--   Раздел 3: Чтение quality_metrics (MTIE / TDEV / ADEV)
--   Раздел 4: Антипаттерны — запросы, которые намеренно работают хуже
--
-- Использование:
--   psql -U timesync_admin -d timesync -f benchmark_grafana_queries.sql
--
-- Создаёт схему bench_grf с plain-таблицами и результатами.
-- Данные в timesync.* не изменяются.
-- =============================================================================

-- ------------------------------------------------------------------
-- 0. Схема, таблица результатов, вспомогательная функция
-- ------------------------------------------------------------------
DROP SCHEMA IF EXISTS bench_grf CASCADE;
CREATE SCHEMA bench_grf;

CREATE TABLE bench_grf.results (
    section      TEXT,
    scenario     TEXT,
    kind         TEXT,      -- 'hypertable' | 'plain'
    median_ms    NUMERIC,
    p90_ms       NUMERIC,
    p99_ms       NUMERIC,
    note         TEXT,
    captured_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION bench_grf.bench_query(
    p_sql  TEXT,
    p_runs INT DEFAULT 7
) RETURNS TABLE(median_ms NUMERIC, p90_ms NUMERIC, p99_ms NUMERIC)
LANGUAGE plpgsql AS $$
DECLARE
    t0  TIMESTAMPTZ;
    arr DOUBLE PRECISION[] := '{}';
BEGIN
    FOR i IN 1..p_runs LOOP
        t0 := clock_timestamp();
        EXECUTE p_sql;
        arr := arr || (EXTRACT(EPOCH FROM (clock_timestamp() - t0)) * 1000.0);
    END LOOP;
    RETURN QUERY
    SELECT
        percentile_cont(0.50) WITHIN GROUP (ORDER BY v)::NUMERIC,
        percentile_cont(0.90) WITHIN GROUP (ORDER BY v)::NUMERIC,
        percentile_cont(0.99) WITHIN GROUP (ORDER BY v)::NUMERIC
    FROM unnest(arr) v;
END$$;

-- ------------------------------------------------------------------
-- 1. Plain-таблицы: копируем структуру (с индексами) и данные
--
-- LIKE ... INCLUDING ALL копирует индексы, constraints, defaults, но не FK —
-- они нам не нужны, мы сравниваем движки хранения, а не целостность.
-- ORDER BY time при копировании даёт plain-таблице ту же физическую
-- сортировку, что и чанки hypertable (честнее для range-scan).
-- ------------------------------------------------------------------
CREATE TABLE bench_grf.phc2sys_metrics  (LIKE timesync.phc2sys_metrics  INCLUDING ALL);
CREATE TABLE bench_grf.ptp4l_metrics    (LIKE timesync.ptp4l_metrics    INCLUDING ALL);
CREATE TABLE bench_grf.pps_metrics      (LIKE timesync.pps_metrics      INCLUDING ALL);
CREATE TABLE bench_grf.quality_metrics  (LIKE timesync.quality_metrics  INCLUDING ALL);

INSERT INTO bench_grf.phc2sys_metrics  SELECT * FROM timesync.phc2sys_metrics  ORDER BY time;
INSERT INTO bench_grf.ptp4l_metrics    SELECT * FROM timesync.ptp4l_metrics    ORDER BY time;
INSERT INTO bench_grf.pps_metrics      SELECT * FROM timesync.pps_metrics      ORDER BY time;
INSERT INTO bench_grf.quality_metrics  SELECT * FROM timesync.quality_metrics  ORDER BY time;

ANALYZE bench_grf.phc2sys_metrics;
ANALYZE bench_grf.ptp4l_metrics;
ANALYZE bench_grf.pps_metrics;
ANALYZE bench_grf.quality_metrics;

-- Принудительно сжимаем все "зрелые" чанки hypertable, чтобы сравнение
-- шло между сжатыми данными (hypertable) и plain-таблицей с теми же индексами.
DO $$
DECLARE
    ht TEXT;
BEGIN
    FOR ht IN
        SELECT format('%I.%I', hypertable_schema, hypertable_name)
        FROM timescaledb_information.hypertables
        WHERE hypertable_schema = 'timesync'
          AND hypertable_name IN ('phc2sys_metrics','ptp4l_metrics','pps_metrics','quality_metrics')
    LOOP
        BEGIN
            PERFORM compress_chunk(c, if_not_compressed => true)
            FROM show_chunks(ht::regclass, older_than => INTERVAL '10 minutes') c;
        EXCEPTION WHEN OTHERS THEN
            RAISE NOTICE 'compress_chunk failed for %: %', ht, SQLERRM;
        END;
    END LOOP;
END$$;

-- ------------------------------------------------------------------
-- 2. Контекст: реальные значения из БД
-- ------------------------------------------------------------------
DO $$
DECLARE
    v_node_type TEXT;
    v_node_id   INTEGER;
    v_hostname  TEXT;
    v_t_max_phc TIMESTAMPTZ;
    v_t_max_ptp TIMESTAMPTZ;
    v_t_max_pps TIMESTAMPTZ;
    v_t_max_qm  TIMESTAMPTZ;
BEGIN
    SELECT type INTO v_node_type
    FROM timesync.nodes
    GROUP BY type ORDER BY count(*) DESC NULLS LAST LIMIT 1;

    SELECT node_id, hostname INTO v_node_id, v_hostname
    FROM timesync.nodes ORDER BY node_id LIMIT 1;

    SELECT max(time) INTO v_t_max_phc FROM timesync.phc2sys_metrics;
    SELECT max(time) INTO v_t_max_ptp FROM timesync.ptp4l_metrics;
    SELECT max(time) INTO v_t_max_pps FROM timesync.pps_metrics;
    SELECT max(time) INTO v_t_max_qm  FROM timesync.quality_metrics;

    CREATE TABLE bench_grf.ctx (key TEXT PRIMARY KEY, val TEXT);
    INSERT INTO bench_grf.ctx VALUES
        ('node_type', v_node_type),
        ('node_id',   v_node_id::TEXT),
        ('hostname',  v_hostname),
        ('t_max_phc', v_t_max_phc::TEXT),
        ('t_max_ptp', v_t_max_ptp::TEXT),
        ('t_max_pps', v_t_max_pps::TEXT),
        ('t_max_qm',  v_t_max_qm::TEXT);
END$$;

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

-- =============================================================================
-- РАЗДЕЛ 3: Quality metrics — чтение MTIE/TDEV/ADEV
-- =============================================================================

SELECT '=== SECTION 3: Quality metrics (hypertable vs plain) ===' AS section;

DO $$
DECLARE
    node_id INTEGER    := (SELECT val::INTEGER   FROM bench_grf.ctx WHERE key = 'node_id');
    t_max   TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_qm');
    q_ht    TEXT;
    q_pl    TEXT;
BEGIN
    IF t_max IS NULL THEN
        RAISE NOTICE 'quality_metrics пустая — пропускаю раздел 3';
        RETURN;
    END IF;

    -- 3a. Одна нода, один tau, один протокол
    q_ht := format($q$
        SELECT time, mtie_ns, tdev_ns, adev_ns
        FROM timesync.quality_metrics
        WHERE node_id = %L
          AND sync_protocol = 'ptp4l'
          AND window_size_s = 10
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        ORDER BY time $q$, node_id, t_max, t_max);

    q_pl := format($q$
        SELECT time, mtie_ns, tdev_ns, adev_ns
        FROM bench_grf.quality_metrics
        WHERE node_id = %L
          AND sync_protocol = 'ptp4l'
          AND window_size_s = 10
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        ORDER BY time $q$, node_id, t_max, t_max);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_single_node_tau10_24h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'node+protocol+tau filter, 24h, ORDER BY time'
    FROM bench_grf.bench_query(q_ht) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_single_node_tau10_24h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'node+protocol+tau filter, 24h, ORDER BY time'
    FROM bench_grf.bench_query(q_pl) bq;

    -- 3b. Одна нода, все tau, один протокол — кривая MTIE в Grafana
    q_ht := format($q$
        SELECT window_size_s,
               avg(mtie_ns) AS avg_mtie,
               avg(tdev_ns) AS avg_tdev,
               avg(adev_ns) AS avg_adev
        FROM timesync.quality_metrics
        WHERE node_id = %L
          AND sync_protocol = 'ptp4l'
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY window_size_s
        ORDER BY window_size_s $q$, node_id, t_max, t_max);

    q_pl := format($q$
        SELECT window_size_s,
               avg(mtie_ns) AS avg_mtie,
               avg(tdev_ns) AS avg_tdev,
               avg(adev_ns) AS avg_adev
        FROM bench_grf.quality_metrics
        WHERE node_id = %L
          AND sync_protocol = 'ptp4l'
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY window_size_s
        ORDER BY window_size_s $q$, node_id, t_max, t_max);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_single_node_all_taus_24h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'all tau aggregation for MTIE curve, 24h'
    FROM bench_grf.bench_query(q_ht) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_single_node_all_taus_24h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'all tau aggregation for MTIE curve, 24h'
    FROM bench_grf.bench_query(q_pl) bq;

    -- 3c. Все ноды, один tau, time_bucket — сравнение нод в Grafana
    q_ht := format($q$
        SELECT time_bucket('5 minutes', time) AS bucket,
               node_id,
               avg(mtie_ns) AS avg_mtie
        FROM timesync.quality_metrics
        WHERE sync_protocol = 'ptp4l'
          AND window_size_s = 100
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max);

    q_pl := format($q$
        SELECT date_trunc('minute', time) AS bucket,
               node_id,
               avg(mtie_ns) AS avg_mtie
        FROM bench_grf.quality_metrics
        WHERE sync_protocol = 'ptp4l'
          AND window_size_s = 100
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_all_nodes_tau100_bucket5m_24h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'all nodes, single tau, bucket 5m, 24h'
    FROM bench_grf.bench_query(q_ht) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_all_nodes_tau100_bucket5m_24h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'all nodes, single tau, bucket 5m, 24h'
    FROM bench_grf.bench_query(q_pl) bq;

    -- 3d. Все ноды, все tau, 72 ч — полный скан (worst case)
    q_ht := format($q$
        SELECT node_id, window_size_s, sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM timesync.quality_metrics
        WHERE time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2, 3
        ORDER BY 1, 2 $q$, t_max, t_max);

    q_pl := format($q$
        SELECT node_id, window_size_s, sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM bench_grf.quality_metrics
        WHERE time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2, 3
        ORDER BY 1, 2 $q$, t_max, t_max);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_all_nodes_all_taus_72h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'full scan all nodes+taus+protocols, 72h'
    FROM bench_grf.bench_query(q_ht) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_all_nodes_all_taus_72h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'full scan all nodes+taus+protocols, 72h'
    FROM bench_grf.bench_query(q_pl) bq;
END$$;

-- =============================================================================
-- РАЗДЕЛ 4: Антипаттерны
-- =============================================================================
-- Для каждого антипаттерна: hypertable + plain, чтобы видеть, когда
-- TimescaleDB даёт выигрыш (chunk exclusion, segmentby) или проигрывает
-- (деcompression overhead при ORDER BY против compress_orderby).
-- =============================================================================

SELECT '=== SECTION 4: Anti-patterns (hypertable vs plain) ===' AS section;

-- ------------------------------------------------------------------
-- AP1. Слишком мелкий бакет: time_bucket('1 second') / date_trunc('second')
--      за 24 часа → ~86 400 групп вместо ~1 440 при 1 minute.
--
-- ПОЧЕМУ медленнее: Hash Agg на 60× большем числе групп + сортировка.
-- КАК ИСПРАВИТЬ: не переопределять $__interval ниже 1 minute для суточных окон.
-- ------------------------------------------------------------------
DO $$
DECLARE
    node_type TEXT        := (SELECT val FROM bench_grf.ctx WHERE key = 'node_type');
    t_max     TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc');
    q_ht      TEXT;
    q_pl      TEXT;
BEGIN
    IF t_max IS NULL THEN RETURN; END IF;

    q_ht := format($q$
        SELECT time_bucket('1 second', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM timesync.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '24 hours'
          AND m.time <= %L::timestamptz
          AND n.type = %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, node_type);

    q_pl := format($q$
        SELECT date_trunc('second', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM bench_grf.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '24 hours'
          AND m.time <= %L::timestamptz
          AND n.type = %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, node_type);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP1_bucket_1s_24h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ANTIPATTERN: bucket=1s over 24h → 86400 groups'
    FROM bench_grf.bench_query(q_ht) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP1_bucket_1s_24h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ANTIPATTERN: bucket=1s over 24h → 86400 groups'
    FROM bench_grf.bench_query(q_pl) bq;
END$$;

-- ------------------------------------------------------------------
-- AP2. Нет фильтра по node_id.
--
-- Hypertable: compress_segmentby='node_id' — без предиката все сегменты
--   декомпрессируются; теряется главный бонус сегментации.
-- Plain: обычный Seq Scan без фильтрации индексом (node_id, time DESC).
-- Разница покажет реальную цену потери segmentby.
-- ------------------------------------------------------------------
DO $$
DECLARE
    t_max TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc');
    q_ht  TEXT;
    q_pl  TEXT;
BEGIN
    IF t_max IS NULL THEN RETURN; END IF;

    q_ht := format($q$
        SELECT time_bucket('1 minute', time) AS bucket,
               avg(offset_ns) AS avg_offset,
               stddev(offset_ns) AS stddev_offset
        FROM timesync.phc2sys_metrics
        WHERE time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1
        ORDER BY 1 $q$, t_max, t_max);

    q_pl := format($q$
        SELECT date_trunc('minute', time) AS bucket,
               avg(offset_ns) AS avg_offset,
               stddev(offset_ns) AS stddev_offset
        FROM bench_grf.phc2sys_metrics
        WHERE time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1
        ORDER BY 1 $q$, t_max, t_max);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP2_no_node_id_filter_24h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ANTIPATTERN: no node_id — all segments decompressed (hypertable loses segmentby)'
    FROM bench_grf.bench_query(q_ht) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP2_no_node_id_filter_24h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ANTIPATTERN: no node_id — Seq Scan on plain table'
    FROM bench_grf.bench_query(q_pl) bq;
END$$;

-- ------------------------------------------------------------------
-- AP3. ORDER BY time ASC vs DESC на 1 час данных.
--
-- compress_orderby='time DESC': чанки хранятся по убыванию.
-- ASC-запрос требует либо reverse-scan, либо sort-node после декомпрессии.
-- Plain: оба направления одинаково дороги (btree-индекс двунаправленный).
-- ------------------------------------------------------------------
DO $$
DECLARE
    node_id  INTEGER     := (SELECT val::INTEGER   FROM bench_grf.ctx WHERE key = 'node_id');
    q_ht_asc  TEXT;
    q_ht_desc TEXT;
    q_pl_asc  TEXT;
    q_pl_desc TEXT;
BEGIN
    IF node_id IS NULL THEN RETURN; END IF;

    q_ht_asc := format($q$
        SELECT time, offset_ns FROM timesync.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '1 hour'
        ORDER BY time ASC $q$, node_id);

    q_ht_desc := format($q$
        SELECT time, offset_ns FROM timesync.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '1 hour'
        ORDER BY time DESC $q$, node_id);

    q_pl_asc := format($q$
        SELECT time, offset_ns FROM bench_grf.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '1 hour'
        ORDER BY time ASC $q$, node_id);

    q_pl_desc := format($q$
        SELECT time, offset_ns FROM bench_grf.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '1 hour'
        ORDER BY time DESC $q$, node_id);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP3a_order_asc_1h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ORDER BY ASC — against compress_orderby=DESC'
    FROM bench_grf.bench_query(q_ht_asc) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP3a_order_asc_1h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ORDER BY ASC — btree index (node_id, time DESC) still usable'
    FROM bench_grf.bench_query(q_pl_asc) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP3b_order_desc_1h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ORDER BY DESC — aligned with compress_orderby=DESC (baseline)'
    FROM bench_grf.bench_query(q_ht_desc) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP3b_order_desc_1h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ORDER BY DESC — uses btree index directly (baseline)'
    FROM bench_grf.bench_query(q_pl_desc) bq;
END$$;

-- ------------------------------------------------------------------
-- AP4. Regex ~ hostname без индекса vs точное совпадение.
--
-- nodes мала — разница сейчас незначительна, но при росте кластера
-- SeqScan nodes с regex даст O(N) vs O(1) при PK-lookup.
-- КАК ИСПРАВИТЬ: CREATE INDEX ... USING gin (hostname gin_trgm_ops).
-- ------------------------------------------------------------------
DO $$
DECLARE
    t_max    TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc');
    hostname TEXT        := (SELECT val FROM bench_grf.ctx WHERE key = 'hostname');
    q_ht_rx  TEXT;
    q_ht_eq  TEXT;
    q_pl_rx  TEXT;
    q_pl_eq  TEXT;
BEGIN
    IF t_max IS NULL OR hostname IS NULL THEN RETURN; END IF;

    q_ht_rx := format($q$
        SELECT time_bucket('5 minutes', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM timesync.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
          AND m.time <= %L::timestamptz
          AND n.hostname ~ %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, hostname);

    q_ht_eq := format($q$
        SELECT time_bucket('5 minutes', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM timesync.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
          AND m.time <= %L::timestamptz
          AND n.hostname = %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, hostname);

    q_pl_rx := format($q$
        SELECT date_trunc('minute', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM bench_grf.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
          AND m.time <= %L::timestamptz
          AND n.hostname ~ %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, hostname);

    q_pl_eq := format($q$
        SELECT date_trunc('minute', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM bench_grf.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
          AND m.time <= %L::timestamptz
          AND n.hostname = %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, hostname);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP4a_hostname_regex_6h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'ANTIPATTERN: hostname ~ regex'
    FROM bench_grf.bench_query(q_ht_rx) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP4a_hostname_regex_6h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'ANTIPATTERN: hostname ~ regex'
    FROM bench_grf.bench_query(q_pl_rx) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP4b_hostname_exact_6h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'BASELINE: hostname = exact'
    FROM bench_grf.bench_query(q_ht_eq) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP4b_hostname_exact_6h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'BASELINE: hostname = exact'
    FROM bench_grf.bench_query(q_pl_eq) bq;
END$$;

-- ------------------------------------------------------------------
-- AP5. IN(subquery) вместо JOIN.
--
-- Мешает планировщику использовать Hash Join; в PG IN обычно
-- rewrite-ится в semi-join, но не всегда — зависит от статистики.
-- ------------------------------------------------------------------
DO $$
DECLARE
    node_type TEXT        := (SELECT val FROM bench_grf.ctx WHERE key = 'node_type');
    t_max     TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc');
    q_ht_sub  TEXT;
    q_ht_jn   TEXT;
    q_pl_sub  TEXT;
    q_pl_jn   TEXT;
BEGIN
    IF t_max IS NULL THEN RETURN; END IF;

    q_ht_sub := format($q$
        SELECT time_bucket('5 minutes', time) AS bucket,
               node_id, avg(offset_ns) AS avg_offset
        FROM timesync.phc2sys_metrics
        WHERE time > %L::timestamptz - INTERVAL '6 hours'
          AND time <= %L::timestamptz
          AND node_id IN (SELECT node_id FROM timesync.nodes WHERE type = %L)
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, node_type);

    q_ht_jn := format($q$
        SELECT time_bucket('5 minutes', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM timesync.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
          AND m.time <= %L::timestamptz
          AND n.type = %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, node_type);

    q_pl_sub := format($q$
        SELECT date_trunc('minute', time) AS bucket,
               node_id, avg(offset_ns) AS avg_offset
        FROM bench_grf.phc2sys_metrics
        WHERE time > %L::timestamptz - INTERVAL '6 hours'
          AND time <= %L::timestamptz
          AND node_id IN (SELECT node_id FROM timesync.nodes WHERE type = %L)
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, node_type);

    q_pl_jn := format($q$
        SELECT date_trunc('minute', m.time) AS bucket,
               n.hostname, avg(m.offset_ns) AS avg_offset
        FROM bench_grf.phc2sys_metrics m
        JOIN timesync.nodes n ON n.node_id = m.node_id
        WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
          AND m.time <= %L::timestamptz
          AND n.type = %L
        GROUP BY 1, 2
        ORDER BY 1 $q$, t_max, t_max, node_type);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP5a_subquery_6h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'ANTIPATTERN: IN(subquery)'
    FROM bench_grf.bench_query(q_ht_sub) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP5a_subquery_6h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'ANTIPATTERN: IN(subquery)'
    FROM bench_grf.bench_query(q_pl_sub) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP5b_join_6h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'BASELINE: explicit JOIN'
    FROM bench_grf.bench_query(q_ht_jn) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP5b_join_6h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'BASELINE: explicit JOIN'
    FROM bench_grf.bench_query(q_pl_jn) bq;
END$$;

-- ------------------------------------------------------------------
-- AP6. UNION ALL без предиката времени.
--
-- Hypertable: нет chunk exclusion — читаются все чанки за 3 дня.
-- Plain: полный Seq Scan трёх таблиц — аналогичный штраф, но без
--   оверхеда декомпрессии; показывает, когда plain быстрее.
-- ------------------------------------------------------------------
DO $$
DECLARE
    node_id   INTEGER     := (SELECT val::INTEGER   FROM bench_grf.ctx WHERE key = 'node_id');
    t_max_phc TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc');
    t_max_ptp TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_ptp');
    t_max_pps TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_pps');
    q_ht_bad  TEXT;
    q_ht_good TEXT;
    q_pl_bad  TEXT;
    q_pl_good TEXT;
BEGIN
    IF node_id IS NULL THEN RETURN; END IF;

    q_ht_bad := format($q$
        SELECT 'ptp4l'   AS proto, time, offset_ns FROM timesync.ptp4l_metrics   WHERE node_id = %L
        UNION ALL
        SELECT 'phc2sys' AS proto, time, offset_ns FROM timesync.phc2sys_metrics  WHERE node_id = %L
        UNION ALL
        SELECT 'pps'     AS proto, time, offset_ns FROM timesync.pps_metrics      WHERE node_id = %L
        ORDER BY time DESC $q$, node_id, node_id, node_id);

    q_ht_good := format($q$
        SELECT 'ptp4l'   AS proto, time, offset_ns FROM timesync.ptp4l_metrics
        WHERE node_id = %L AND time > %L::timestamptz - INTERVAL '1 hour'
        UNION ALL
        SELECT 'phc2sys' AS proto, time, offset_ns FROM timesync.phc2sys_metrics
        WHERE node_id = %L AND time > %L::timestamptz - INTERVAL '1 hour'
        UNION ALL
        SELECT 'pps'     AS proto, time, offset_ns FROM timesync.pps_metrics
        WHERE node_id = %L AND time > %L::timestamptz - INTERVAL '1 hour'
        ORDER BY time DESC $q$,
        node_id, t_max_ptp, node_id, t_max_phc, node_id, t_max_pps);

    q_pl_bad := format($q$
        SELECT 'ptp4l'   AS proto, time, offset_ns FROM bench_grf.ptp4l_metrics   WHERE node_id = %L
        UNION ALL
        SELECT 'phc2sys' AS proto, time, offset_ns FROM bench_grf.phc2sys_metrics  WHERE node_id = %L
        UNION ALL
        SELECT 'pps'     AS proto, time, offset_ns FROM bench_grf.pps_metrics      WHERE node_id = %L
        ORDER BY time DESC $q$, node_id, node_id, node_id);

    q_pl_good := format($q$
        SELECT 'ptp4l'   AS proto, time, offset_ns FROM bench_grf.ptp4l_metrics
        WHERE node_id = %L AND time > %L::timestamptz - INTERVAL '1 hour'
        UNION ALL
        SELECT 'phc2sys' AS proto, time, offset_ns FROM bench_grf.phc2sys_metrics
        WHERE node_id = %L AND time > %L::timestamptz - INTERVAL '1 hour'
        UNION ALL
        SELECT 'pps'     AS proto, time, offset_ns FROM bench_grf.pps_metrics
        WHERE node_id = %L AND time > %L::timestamptz - INTERVAL '1 hour'
        ORDER BY time DESC $q$,
        node_id, t_max_ptp, node_id, t_max_phc, node_id, t_max_pps);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP6a_union_all_no_time', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ANTIPATTERN: UNION ALL without time filter — full 3-day scan + decompression'
    FROM bench_grf.bench_query(q_ht_bad, 3) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP6a_union_all_no_time', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'ANTIPATTERN: UNION ALL without time filter — full Seq Scan 3 tables'
    FROM bench_grf.bench_query(q_pl_bad, 3) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP6b_union_all_with_time', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'GOOD: UNION ALL with time filter — chunk exclusion active'
    FROM bench_grf.bench_query(q_ht_good) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP6b_union_all_with_time', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'GOOD: UNION ALL with time filter — index scan on time range'
    FROM bench_grf.bench_query(q_pl_good) bq;
END$$;

-- ------------------------------------------------------------------
-- AP7. GetOffsets: ORDER BY time ASC vs без сортировки.
--
-- Цена сортировки на сжатых данных с compress_orderby='time DESC'
-- vs plain-таблицей с btree-индексом (node_id, time DESC).
-- ------------------------------------------------------------------
DO $$
DECLARE
    node_id     INTEGER := (SELECT val::INTEGER FROM bench_grf.ctx WHERE key = 'node_id');
    q_ht_sort   TEXT;
    q_ht_unsort TEXT;
    q_pl_sort   TEXT;
    q_pl_unsort TEXT;
BEGIN
    IF node_id IS NULL THEN RETURN; END IF;

    q_ht_sort := format($q$
        SELECT time, offset_ns FROM timesync.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '400 seconds'
        ORDER BY time ASC $q$, node_id);

    q_ht_unsort := format($q$
        SELECT time, offset_ns FROM timesync.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '400 seconds' $q$, node_id);

    q_pl_sort := format($q$
        SELECT time, offset_ns FROM bench_grf.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '400 seconds'
        ORDER BY time ASC $q$, node_id);

    q_pl_unsort := format($q$
        SELECT time, offset_ns FROM bench_grf.phc2sys_metrics
        WHERE node_id = %L AND time > now() - INTERVAL '400 seconds' $q$, node_id);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP7a_getoffsets_sorted_400s', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'GetOffsets ORDER BY ASC (window_metrics.go pattern)'
    FROM bench_grf.bench_query(q_ht_sort) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP7a_getoffsets_sorted_400s', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'GetOffsets ORDER BY ASC on plain table'
    FROM bench_grf.bench_query(q_pl_sort) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP7b_getoffsets_unsorted_400s', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'GetOffsets without ORDER BY — sort cost baseline'
    FROM bench_grf.bench_query(q_ht_unsort) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP7b_getoffsets_unsorted_400s', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms,
           'GetOffsets without ORDER BY on plain table'
    FROM bench_grf.bench_query(q_pl_unsort) bq;
END$$;

-- ------------------------------------------------------------------
-- AP8. quality_metrics без фильтра по window_size_s.
--
-- 18 tau × N нод × 3 протокола за тик — без фильтра читается в 18×
-- больше строк. Индекс idx_quality_metrics_time_node покрывает
-- window_size_s, поэтому фильтр сужает результат за счёт Index Scan.
-- ------------------------------------------------------------------
DO $$
DECLARE
    node_id      INTEGER     := (SELECT val::INTEGER   FROM bench_grf.ctx WHERE key = 'node_id');
    t_max        TIMESTAMPTZ := (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_qm');
    q_ht_with    TEXT;
    q_ht_without TEXT;
    q_pl_with    TEXT;
    q_pl_without TEXT;
BEGIN
    IF t_max IS NULL THEN RETURN; END IF;

    q_ht_with := format($q$
        SELECT time_bucket('5 minutes', time) AS bucket, avg(mtie_ns) AS avg_mtie
        FROM timesync.quality_metrics
        WHERE node_id = %L AND sync_protocol = 'ptp4l' AND window_size_s = 10
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1
        ORDER BY 1 $q$, node_id, t_max, t_max);

    q_ht_without := format($q$
        SELECT time_bucket('5 minutes', time) AS bucket,
               window_size_s, avg(mtie_ns) AS avg_mtie
        FROM timesync.quality_metrics
        WHERE node_id = %L AND sync_protocol = 'ptp4l'
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        ORDER BY 1, 2 $q$, node_id, t_max, t_max);

    q_pl_with := format($q$
        SELECT date_trunc('minute', time) AS bucket, avg(mtie_ns) AS avg_mtie
        FROM bench_grf.quality_metrics
        WHERE node_id = %L AND sync_protocol = 'ptp4l' AND window_size_s = 10
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1
        ORDER BY 1 $q$, node_id, t_max, t_max);

    q_pl_without := format($q$
        SELECT date_trunc('minute', time) AS bucket,
               window_size_s, avg(mtie_ns) AS avg_mtie
        FROM bench_grf.quality_metrics
        WHERE node_id = %L AND sync_protocol = 'ptp4l'
          AND time > %L::timestamptz - INTERVAL '24 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        ORDER BY 1, 2 $q$, node_id, t_max, t_max);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP8a_qm_with_tau_24h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'GOOD: with window_size_s filter'
    FROM bench_grf.bench_query(q_ht_with) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP8a_qm_with_tau_24h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'GOOD: with window_size_s filter'
    FROM bench_grf.bench_query(q_pl_with) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP8b_qm_without_tau_24h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'ANTIPATTERN: no window_size_s — 18x rows'
    FROM bench_grf.bench_query(q_ht_without) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '4_antipattern', 'AP8b_qm_without_tau_24h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'ANTIPATTERN: no window_size_s — 18x rows'
    FROM bench_grf.bench_query(q_pl_without) bq;
END$$;

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
