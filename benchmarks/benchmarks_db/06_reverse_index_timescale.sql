-- =============================================================================
-- РАЗДЕЛ 6: Grafana-like запросы на TimescaleDB со сжатыми чанками
--            с обратным индексом (time DESC, node_id) и без него
-- =============================================================================
-- Для каждой проверяемой таблицы создаются две Timescale-копии в bench_grf:
--   *_no_rev   — только индекс (node_id, time DESC)
--   *_with_rev — индекс (node_id, time DESC) + индекс (time DESC, node_id)
--
-- Обе копии являются hypertable, получают те же данные из timesync.* и затем
-- принудительно сжимаются через compress_chunk(). Это измеряет именно Timescale
-- compressed-chunk поведение, а не plain PostgreSQL.
-- =============================================================================

SELECT '=== SECTION 6: Reverse index on compressed Timescale copies ===' AS section;

CREATE OR REPLACE PROCEDURE bench_grf.prepare_compressed_metric_copy(
    p_source_table TEXT,
    p_copy_table TEXT,
    p_chunk_interval INTERVAL,
    p_with_reverse_index BOOLEAN
)
LANGUAGE plpgsql AS $$
DECLARE
    src REGCLASS := format('timesync.%I', p_source_table)::REGCLASS;
    dst TEXT := format('bench_grf.%I', p_copy_table);
BEGIN
    EXECUTE format('DROP TABLE IF EXISTS %s CASCADE', dst);
    EXECUTE format('CREATE TABLE %s (LIKE %s INCLUDING DEFAULTS INCLUDING CONSTRAINTS)', dst, src);

    EXECUTE format(
        'SELECT create_hypertable(%L, %L, chunk_time_interval => %L::interval, if_not_exists => TRUE)',
        dst,
        'time',
        p_chunk_interval::TEXT
    );

    EXECUTE format(
        'ALTER TABLE %s SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = %L,
            timescaledb.compress_orderby = %L
        )',
        dst,
        'node_id',
        'time DESC'
    );

    EXECUTE format('INSERT INTO %s SELECT * FROM %s ORDER BY time', dst, src);

    EXECUTE format(
        'CREATE INDEX %I ON %s (node_id, time DESC)',
        p_copy_table || '_node_time_idx',
        dst
    );

    IF p_with_reverse_index THEN
        EXECUTE format(
            'CREATE INDEX %I ON %s (time DESC, node_id)',
            p_copy_table || '_time_node_idx',
            dst
        );
    END IF;

    EXECUTE format('ANALYZE %s', dst);

    EXECUTE format(
        'SELECT compress_chunk(c, if_not_compressed => TRUE)
         FROM show_chunks(%L::regclass) c',
        dst
    );

    EXECUTE format('ANALYZE %s', dst);
END$$;

CREATE OR REPLACE PROCEDURE bench_grf.bench_reverse_index_pair(
    p_scenario TEXT,
    p_sql_no_reverse TEXT,
    p_sql_with_reverse TEXT,
    p_note TEXT,
    p_runs INT DEFAULT 7
)
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '6_reverse_index_timescale', p_scenario, 'timescale_no_reverse',
           bq.median_ms, bq.p90_ms, bq.p99_ms, p_note
    FROM bench_grf.bench_query(p_sql_no_reverse, p_runs) bq;

    INSERT INTO bench_grf.results (section, scenario, kind, median_ms, p90_ms, p99_ms, note)
    SELECT '6_reverse_index_timescale', p_scenario, 'timescale_with_reverse',
           bq.median_ms, bq.p90_ms, bq.p99_ms, p_note
    FROM bench_grf.bench_query(p_sql_with_reverse, p_runs) bq;
END$$;

DO $$
DECLARE
    t RECORD;
BEGIN
    FOR t IN
        SELECT *
        FROM (VALUES
            ('phc2sys_metrics', '1 hour'::INTERVAL),
            ('ptp4l_metrics',   '1 hour'::INTERVAL),
            ('pps_metrics',     '1 hour'::INTERVAL),
            ('network_metrics', '6 hours'::INTERVAL),
            ('cpu_metrics',     '6 hours'::INTERVAL),
            ('memory_metrics',  '6 hours'::INTERVAL)
        ) AS v(table_name, chunk_interval)
    LOOP
        CALL bench_grf.prepare_compressed_metric_copy(
            t.table_name,
            t.table_name || '_no_rev',
            t.chunk_interval,
            FALSE
        );

        CALL bench_grf.prepare_compressed_metric_copy(
            t.table_name,
            t.table_name || '_with_rev',
            t.chunk_interval,
            TRUE
        );
    END LOOP;
END$$;

DO $$
DECLARE
    hostname TEXT := (SELECT val FROM bench_grf.ctx WHERE key = 'hostname');
    windows TEXT[][] := ARRAY[
        ARRAY['1 hour', '1m', '1 minute'],
        ARRAY['6 hours', '5m', '5 minutes'],
        ARRAY['24 hours', '30m', '30 minutes']
    ];
    w TEXT[];
    t RECORD;
    t_max TIMESTAMPTZ;
    t_max_ptp_pps TIMESTAMPTZ;
    t_max_phc_pps TIMESTAMPTZ;
BEGIN
    FOR t IN
        SELECT *
        FROM (VALUES
            ('phc2sys_metrics', 't_max_phc'),
            ('ptp4l_metrics',   't_max_ptp'),
            ('pps_metrics',     't_max_pps')
        ) AS v(table_name, ctx_key)
    LOOP
        SELECT val::TIMESTAMPTZ INTO t_max
        FROM bench_grf.ctx
        WHERE key = t.ctx_key;

        IF t_max IS NULL THEN
            CONTINUE;
        END IF;

        FOREACH w SLICE 1 IN ARRAY windows LOOP
            CALL bench_grf.bench_reverse_index_pair(
                format('%s_avg_all_nodes_%s', replace(t.table_name, '_metrics', ''), replace(w[1], ' ', '_')),
                format($q$
                    SELECT time_bucket(INTERVAL %L, m.time) AS bucket,
                           n.hostname,
                           avg(m.offset_ns) AS avg_offset
                    FROM bench_grf.%I m
                    JOIN timesync.nodes n ON n.node_id = m.node_id
                    WHERE m.time > %L::timestamptz - INTERVAL %L
                      AND m.time <= %L::timestamptz
                      AND n.hostname ~ '.*'
                    GROUP BY 1, 2
                    ORDER BY 1 $q$,
                    w[3], t.table_name || '_no_rev', t_max, w[1], t_max
                ),
                format($q$
                    SELECT time_bucket(INTERVAL %L, m.time) AS bucket,
                           n.hostname,
                           avg(m.offset_ns) AS avg_offset
                    FROM bench_grf.%I m
                    JOIN timesync.nodes n ON n.node_id = m.node_id
                    WHERE m.time > %L::timestamptz - INTERVAL %L
                      AND m.time <= %L::timestamptz
                      AND n.hostname ~ '.*'
                    GROUP BY 1, 2
                    ORDER BY 1 $q$,
                    w[3], t.table_name || '_with_rev', t_max, w[1], t_max
                ),
                format('Grafana avg offset, all nodes, window=%s, bucket=%s', w[1], w[2])
            );
        END LOOP;

        CALL bench_grf.bench_reverse_index_pair(
            format('%s_avg_selected_node_1h', replace(t.table_name, '_metrics', '')),
            format($q$
                SELECT time_bucket(INTERVAL '1 minute', m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns) AS avg_offset
                FROM bench_grf.%I m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL '1 hour'
                  AND m.time <= %L::timestamptz
                  AND n.hostname ~ %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t.table_name || '_no_rev', t_max, t_max, hostname
            ),
            format($q$
                SELECT time_bucket(INTERVAL '1 minute', m.time) AS bucket,
                       n.hostname,
                       avg(m.offset_ns) AS avg_offset
                FROM bench_grf.%I m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL '1 hour'
                  AND m.time <= %L::timestamptz
                  AND n.hostname ~ %L
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t.table_name || '_with_rev', t_max, t_max, hostname
            ),
            'Grafana avg offset, selected node regex, 1h, bucket=1m'
        );
    END LOOP;

    SELECT LEAST(
        (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_ptp'),
        (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_pps')
    ) INTO t_max_ptp_pps;

    IF t_max_ptp_pps IS NOT NULL THEN
        CALL bench_grf.bench_reverse_index_pair(
            'ptp4l_minus_pps_all_nodes_6h',
            format($q$
                WITH ptp AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.ptp4l_metrics_no_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                ),
                pps AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.pps_metrics_no_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                )
                SELECT ptp.t AS time,
                       n.hostname,
                       ptp.offset - pps.offset AS delta_ns
                FROM ptp
                JOIN pps ON pps.node_id = ptp.node_id AND pps.t = ptp.t
                JOIN timesync.nodes n ON n.node_id = ptp.node_id
                WHERE n.hostname ~ '.*'
                ORDER BY 1 $q$,
                t_max_ptp_pps, t_max_ptp_pps, t_max_ptp_pps, t_max_ptp_pps
            ),
            format($q$
                WITH ptp AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.ptp4l_metrics_with_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                ),
                pps AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.pps_metrics_with_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                )
                SELECT ptp.t AS time,
                       n.hostname,
                       ptp.offset - pps.offset AS delta_ns
                FROM ptp
                JOIN pps ON pps.node_id = ptp.node_id AND pps.t = ptp.t
                JOIN timesync.nodes n ON n.node_id = ptp.node_id
                WHERE n.hostname ~ '.*'
                ORDER BY 1 $q$,
                t_max_ptp_pps, t_max_ptp_pps, t_max_ptp_pps, t_max_ptp_pps
            ),
            'Grafana CTE diff ptp4l - PPS, all nodes, 6h, bucket=5m'
        );
    END IF;

    SELECT LEAST(
        (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_phc'),
        (SELECT val::TIMESTAMPTZ FROM bench_grf.ctx WHERE key = 't_max_pps')
    ) INTO t_max_phc_pps;

    IF t_max_phc_pps IS NOT NULL THEN
        CALL bench_grf.bench_reverse_index_pair(
            'phc2sys_minus_pps_all_nodes_6h',
            format($q$
                WITH phc AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.phc2sys_metrics_no_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                ),
                pps AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.pps_metrics_no_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                )
                SELECT phc.t AS time,
                       n.hostname,
                       phc.offset - pps.offset AS delta_ns
                FROM phc
                JOIN pps ON pps.node_id = phc.node_id AND pps.t = phc.t
                JOIN timesync.nodes n ON n.node_id = phc.node_id
                WHERE n.hostname ~ '.*'
                ORDER BY 1 $q$,
                t_max_phc_pps, t_max_phc_pps, t_max_phc_pps, t_max_phc_pps
            ),
            format($q$
                WITH phc AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.phc2sys_metrics_with_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                ),
                pps AS (
                    SELECT time_bucket(INTERVAL '5 minutes', time) AS t,
                           node_id,
                           avg(offset_ns) AS offset
                    FROM bench_grf.pps_metrics_with_rev
                    WHERE time > %L::timestamptz - INTERVAL '6 hours'
                      AND time <= %L::timestamptz
                    GROUP BY 1, 2
                )
                SELECT phc.t AS time,
                       n.hostname,
                       phc.offset - pps.offset AS delta_ns
                FROM phc
                JOIN pps ON pps.node_id = phc.node_id AND pps.t = phc.t
                JOIN timesync.nodes n ON n.node_id = phc.node_id
                WHERE n.hostname ~ '.*'
                ORDER BY 1 $q$,
                t_max_phc_pps, t_max_phc_pps, t_max_phc_pps, t_max_phc_pps
            ),
            'Grafana CTE diff phc2sys - PPS, all nodes, 6h, bucket=5m'
        );
    END IF;
END$$;

DO $$
DECLARE
    t_max_network TIMESTAMPTZ := (SELECT max(time) FROM timesync.network_metrics);
    t_max_cpu TIMESTAMPTZ := (SELECT max(time) FROM timesync.cpu_metrics);
    t_max_memory TIMESTAMPTZ := (SELECT max(time) FROM timesync.memory_metrics);
BEGIN
    IF t_max_network IS NOT NULL THEN
        CALL bench_grf.bench_reverse_index_pair(
            'network_rx_rate_all_nodes_1h',
            format($q$
                SELECT time_bucket(INTERVAL '5 seconds', time) AS bucket,
                       hostname,
                       avg(diff_per_sec) AS rx_packets_per_second
                FROM (
                    SELECT m.time,
                           n.hostname,
                           (m.rx_packets - lag(m.rx_packets) OVER (PARTITION BY m.node_id ORDER BY m.time)) /
                           NULLIF(extract(epoch FROM (m.time - lag(m.time) OVER (PARTITION BY m.node_id ORDER BY m.time))), 0) AS diff_per_sec
                    FROM bench_grf.network_metrics_no_rev m
                    JOIN timesync.nodes n ON n.node_id = m.node_id
                    WHERE m.time > %L::timestamptz - INTERVAL '1 hour'
                      AND m.time <= %L::timestamptz
                      AND n.hostname ~ '.*'
                ) sub
                WHERE diff_per_sec >= 0
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t_max_network, t_max_network
            ),
            format($q$
                SELECT time_bucket(INTERVAL '5 seconds', time) AS bucket,
                       hostname,
                       avg(diff_per_sec) AS rx_packets_per_second
                FROM (
                    SELECT m.time,
                           n.hostname,
                           (m.rx_packets - lag(m.rx_packets) OVER (PARTITION BY m.node_id ORDER BY m.time)) /
                           NULLIF(extract(epoch FROM (m.time - lag(m.time) OVER (PARTITION BY m.node_id ORDER BY m.time))), 0) AS diff_per_sec
                    FROM bench_grf.network_metrics_with_rev m
                    JOIN timesync.nodes n ON n.node_id = m.node_id
                    WHERE m.time > %L::timestamptz - INTERVAL '1 hour'
                      AND m.time <= %L::timestamptz
                      AND n.hostname ~ '.*'
                ) sub
                WHERE diff_per_sec >= 0
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t_max_network, t_max_network
            ),
            'Grafana network RX rate with lag(), all nodes, 1h, bucket=5s'
        );
    END IF;

    IF t_max_cpu IS NOT NULL THEN
        CALL bench_grf.bench_reverse_index_pair(
            'cpu_usage_all_nodes_6h',
            format($q$
                SELECT time_bucket(INTERVAL '15 seconds', m.time) AS bucket,
                       n.hostname,
                       avg(m.usage_percent) AS cpu_usage
                FROM bench_grf.cpu_metrics_no_rev m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
                  AND m.time <= %L::timestamptz
                  AND n.hostname ~ '.*'
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t_max_cpu, t_max_cpu
            ),
            format($q$
                SELECT time_bucket(INTERVAL '15 seconds', m.time) AS bucket,
                       n.hostname,
                       avg(m.usage_percent) AS cpu_usage
                FROM bench_grf.cpu_metrics_with_rev m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
                  AND m.time <= %L::timestamptz
                  AND n.hostname ~ '.*'
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t_max_cpu, t_max_cpu
            ),
            'Grafana CPU usage, all nodes, 6h, bucket=15s'
        );
    END IF;

    IF t_max_memory IS NOT NULL THEN
        CALL bench_grf.bench_reverse_index_pair(
            'memory_available_all_nodes_6h',
            format($q$
                SELECT time_bucket(INTERVAL '15 seconds', m.time) AS bucket,
                       n.hostname,
                       avg(m.mem_available_kb) / 1024 AS available_mb
                FROM bench_grf.memory_metrics_no_rev m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
                  AND m.time <= %L::timestamptz
                  AND n.hostname ~ '.*'
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t_max_memory, t_max_memory
            ),
            format($q$
                SELECT time_bucket(INTERVAL '15 seconds', m.time) AS bucket,
                       n.hostname,
                       avg(m.mem_available_kb) / 1024 AS available_mb
                FROM bench_grf.memory_metrics_with_rev m
                JOIN timesync.nodes n ON n.node_id = m.node_id
                WHERE m.time > %L::timestamptz - INTERVAL '6 hours'
                  AND m.time <= %L::timestamptz
                  AND n.hostname ~ '.*'
                GROUP BY 1, 2
                ORDER BY 1 $q$,
                t_max_memory, t_max_memory
            ),
            'Grafana memory available, all nodes, 6h, bucket=15s'
        );
    END IF;
END$$;
