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

