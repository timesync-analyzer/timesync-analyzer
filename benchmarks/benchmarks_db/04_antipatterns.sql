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

