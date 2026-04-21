-- =============================================================================
-- Benchmark: TimescaleDB hypertables vs. plain PostgreSQL tables
-- =============================================================================
-- Запускать в той же БД, где уже лежат hypertable'ы.
-- Создаёт схему bench_plain с "близнецами" без Timescale, копирует туда
-- данные из hypertable'ов, затем сравнивает размер и время запросов.
--
-- Использование:
--   psql -U timesync_admin -d timesync -f benchmark_timescale_vs_pg.sql
--
-- Перед запуском желательно: CREATE EXTENSION IF NOT EXISTS pg_prewarm;
-- Если расширения нет — прогрев будет пропущен (скрипт не упадёт).
-- =============================================================================

-- ------------------------------------------------------------------
-- 0. Подготовка: отдельная схема, чтобы ничего не перепутать
-- ------------------------------------------------------------------
DROP SCHEMA IF EXISTS bench_plain CASCADE;
CREATE SCHEMA bench_plain;

-- Таблица для накопления результатов
CREATE TABLE bench_plain.results (
    metric         TEXT,
    table_name     TEXT,
    kind           TEXT,      -- 'hypertable' | 'plain'
    value_text     TEXT,
    value_numeric  NUMERIC,
    captured_at    TIMESTAMPTZ DEFAULT NOW()
);

-- ------------------------------------------------------------------
-- 1. Создание plain-близнецов и копирование данных
-- ------------------------------------------------------------------
-- LIKE ... INCLUDING ALL не копирует FK, но копирует индексы и дефолты.
-- FK нам тут не нужен — мы сравниваем движки хранения.

CREATE TABLE bench_plain.ptp4l_metrics       (LIKE timesync.ptp4l_metrics       INCLUDING ALL);
CREATE TABLE bench_plain.phc2sys_metrics     (LIKE timesync.phc2sys_metrics     INCLUDING ALL);
CREATE TABLE bench_plain.network_metrics     (LIKE timesync.network_metrics     INCLUDING ALL);
CREATE TABLE bench_plain.cpu_metrics         (LIKE timesync.cpu_metrics         INCLUDING ALL);
CREATE TABLE bench_plain.memory_metrics      (LIKE timesync.memory_metrics      INCLUDING ALL);
CREATE TABLE bench_plain.temperature_metrics (LIKE timesync.temperature_metrics INCLUDING ALL);
CREATE TABLE bench_plain.pps_metrics         (LIKE timesync.pps_metrics         INCLUDING ALL);

-- Копируем данные. ORDER BY time — чтобы plain-таблица имела ту же
-- физическую сортировку, что и чанки hypertable (честнее для диапазонных сканов).
INSERT INTO bench_plain.ptp4l_metrics       SELECT * FROM timesync.ptp4l_metrics       ORDER BY time;
INSERT INTO bench_plain.phc2sys_metrics     SELECT * FROM timesync.phc2sys_metrics     ORDER BY time;
INSERT INTO bench_plain.network_metrics     SELECT * FROM timesync.network_metrics     ORDER BY time;
INSERT INTO bench_plain.cpu_metrics         SELECT * FROM timesync.cpu_metrics         ORDER BY time;
INSERT INTO bench_plain.memory_metrics      SELECT * FROM timesync.memory_metrics      ORDER BY time;
INSERT INTO bench_plain.temperature_metrics SELECT * FROM timesync.temperature_metrics ORDER BY time;
INSERT INTO bench_plain.pps_metrics         SELECT * FROM timesync.pps_metrics         ORDER BY time;

ANALYZE bench_plain.ptp4l_metrics;
ANALYZE bench_plain.phc2sys_metrics;
ANALYZE bench_plain.network_metrics;
ANALYZE bench_plain.cpu_metrics;
ANALYZE bench_plain.memory_metrics;
ANALYZE bench_plain.temperature_metrics;
ANALYZE bench_plain.pps_metrics;

-- ------------------------------------------------------------------
-- 2. Принудительное сжатие всех "зрелых" чанков hypertable
-- ------------------------------------------------------------------
-- Политика compress_chunks срабатывает раз в ~12 ч. Чтобы не ждать —
-- сжимаем всё, что старше 10 минут, вручную.
DO $$
DECLARE
    ht TEXT;
BEGIN
    FOR ht IN
        SELECT format('%I.%I', hypertable_schema, hypertable_name)
        FROM timescaledb_information.hypertables
        WHERE hypertable_schema = 'timesync'
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
-- 3. Сравнение размеров
-- ------------------------------------------------------------------
INSERT INTO bench_plain.results (metric, table_name, kind, value_text, value_numeric)
SELECT 'size_bytes', h.hypertable_name, 'hypertable',
       pg_size_pretty(hypertable_size(format('%I.%I', h.hypertable_schema, h.hypertable_name)::regclass)),
       hypertable_size(format('%I.%I', h.hypertable_schema, h.hypertable_name)::regclass)
FROM timescaledb_information.hypertables h
WHERE h.hypertable_schema = 'timesync'
  AND h.hypertable_name IN ('ptp4l_metrics','phc2sys_metrics','network_metrics',
                            'cpu_metrics','memory_metrics','temperature_metrics','pps_metrics');

INSERT INTO bench_plain.results (metric, table_name, kind, value_text, value_numeric)
SELECT 'size_bytes', t.tablename, 'plain',
       pg_size_pretty(pg_total_relation_size(format('bench_plain.%I', t.tablename)::regclass)),
       pg_total_relation_size(format('bench_plain.%I', t.tablename)::regclass)
FROM pg_tables t
WHERE t.schemaname = 'bench_plain'
  AND t.tablename  IN ('ptp4l_metrics','phc2sys_metrics','network_metrics',
                       'cpu_metrics','memory_metrics','temperature_metrics','pps_metrics');

-- Состояние компрессии для наглядности
SELECT '=== Compression stats (hypertables) ===' AS section;
SELECT t.name AS hypertable_name,
       pg_size_pretty(s.before_compression_total_bytes) AS before_compression,
       pg_size_pretty(s.after_compression_total_bytes)  AS after_compression,
       round(100.0 * (1 - s.after_compression_total_bytes::numeric
                          / NULLIF(s.before_compression_total_bytes, 0)), 1) AS pct_saved
FROM (VALUES
        ('ptp4l_metrics'),
        ('phc2sys_metrics'),
        ('network_metrics'),
        ('cpu_metrics'),
        ('memory_metrics'),
        ('temperature_metrics'),
        ('pps_metrics')
     ) AS t(name)
CROSS JOIN LATERAL hypertable_compression_stats(('timesync.' || t.name)::regclass) s
ORDER BY t.name;

-- ------------------------------------------------------------------
-- 6. Сводная таблица
-- ------------------------------------------------------------------
SELECT '=== SIZE COMPARISON ===' AS section;
SELECT
    h.table_name,
    pg_size_pretty(h.value_numeric::bigint) AS hypertable_size,
    pg_size_pretty(p.value_numeric::bigint) AS plain_size,
    round(100.0 * (1 - h.value_numeric / NULLIF(p.value_numeric, 0)), 1) AS pct_smaller
FROM bench_plain.results h
JOIN bench_plain.results p USING (metric, table_name)
WHERE h.metric = 'size_bytes' AND h.kind = 'hypertable' AND p.kind = 'plain'
ORDER BY h.table_name;

SELECT '=== CHUNK INFO (to confirm partitioning is meaningful) ===' AS section;
SELECT hypertable_name, count(*) AS chunks
FROM timescaledb_information.chunks
WHERE hypertable_schema = 'timesync'
GROUP BY hypertable_name
ORDER BY hypertable_name;

-- ------------------------------------------------------------------
-- 7. Очистка (раскомментировать после снятия результатов)
-- ------------------------------------------------------------------
-- DROP SCHEMA bench_plain CASCADE;
