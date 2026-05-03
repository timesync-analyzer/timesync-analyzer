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
        SELECT node_id, window_size_s, 'ptp4l' AS sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM timesync.quality_metrics
        WHERE sync_protocol = 'ptp4l'
          AND time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        UNION ALL
        SELECT node_id, window_size_s, 'pps' AS sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM timesync.quality_metrics
        WHERE sync_protocol = 'pps'
          AND time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        UNION ALL
        SELECT node_id, window_size_s, 'phc2sys' AS sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM timesync.quality_metrics
        WHERE sync_protocol = 'phc2sys'
          AND time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        ORDER BY 1, 2, 3 $q$, t_max, t_max, t_max, t_max, t_max, t_max);

    q_pl := format($q$
        SELECT node_id, window_size_s, 'ptp4l' AS sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM bench_grf.quality_metrics
        WHERE sync_protocol = 'ptp4l'
          AND time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        UNION ALL
        SELECT node_id, window_size_s, 'pps' AS sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM bench_grf.quality_metrics
        WHERE sync_protocol = 'pps'
          AND time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        UNION ALL
        SELECT node_id, window_size_s, 'phc2sys' AS sync_protocol,
               avg(mtie_ns), avg(tdev_ns), avg(adev_ns)
        FROM bench_grf.quality_metrics
        WHERE sync_protocol = 'phc2sys'
          AND time > %L::timestamptz - INTERVAL '72 hours'
          AND time <= %L::timestamptz
        GROUP BY 1, 2
        ORDER BY 1, 2, 3 $q$, t_max, t_max, t_max, t_max, t_max, t_max);

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_all_nodes_all_taus_72h', 'hypertable',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'full scan all nodes+taus+protocols, 72h'
    FROM bench_grf.bench_query(q_ht) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '3_quality', 'qm_all_nodes_all_taus_72h', 'plain',
           bq.median_ms, bq.p90_ms, bq.p99_ms, 'full scan all nodes+taus+protocols, 72h'
    FROM bench_grf.bench_query(q_pl) bq;
END$$;

