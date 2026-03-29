
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.ptp4l_quality_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    window_size INT NOT NULL,
    mtie_ns BIGINT,
    tdev_ns FLOAT,
    adev_ns FLOAT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.ptp4l_quality_metrics', 'time');

ALTER TABLE timesync.ptp4l_quality_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.ptp4l_quality_metrics', INTERVAL '10 minutes');

COMMENT ON TABLE timesync.ptp4l_quality_metrics IS 'Расширенные метрики с временными окнами по ptp4l синхронизации';
COMMENT ON COLUMN timesync.ptp4l_quality_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.ptp4l_quality_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.ptp4l_quality_metrics.window_size IS 'Размер окна вычисления метрик';
COMMENT ON COLUMN timesync.ptp4l_quality_metrics.mtie_ns IS 'Максимальный временной сдвиг за скользящее окно длиной tau для ptp4l';
COMMENT ON COLUMN timesync.ptp4l_quality_metrics.tdev_ns IS 'Стандартизированная мера нестабильности, производная от MTIE для ptp4l';
COMMENT ON COLUMN timesync.ptp4l_quality_metrics.adev_ns IS 'Allan Deviation для ptp4l';

CREATE UNIQUE INDEX idx_ptp4l_quality_metrics_time_node ON timesync.ptp4l_quality_metrics (time DESC, node_id, window_size);

GRANT SELECT, INSERT, UPDATE ON timesync.ptp4l_quality_metrics TO timesync_app;
GRANT SELECT ON timesync.ptp4l_quality_metrics TO timesync_user;
GRANT ALL ON timesync.ptp4l_quality_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.ptp4l_quality_metrics', INTERVAL '3 days');

CREATE TABLE IF NOT EXISTS timesync.phc2sys_quality_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    window_size INT NOT NULL,
    mtie_ns BIGINT,
    tdev_ns FLOAT,
    adev_ns FLOAT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.phc2sys_quality_metrics', 'time');

ALTER TABLE timesync.phc2sys_quality_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.phc2sys_quality_metrics', INTERVAL '10 minutes');

COMMENT ON TABLE timesync.phc2sys_quality_metrics IS 'Расширенные метрики с временными окнами по phc2sys синхронизации';
COMMENT ON COLUMN timesync.phc2sys_quality_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.phc2sys_quality_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.phc2sys_quality_metrics.window_size IS 'Размер окна вычисления метрик';
COMMENT ON COLUMN timesync.phc2sys_quality_metrics.mtie_ns IS 'Максимальный временной сдвиг за скользящее окно длиной tau для phc2sys';
COMMENT ON COLUMN timesync.phc2sys_quality_metrics.tdev_ns IS 'Стандартизированная мера нестабильности, производная от MTIE для phc2sys';
COMMENT ON COLUMN timesync.phc2sys_quality_metrics.adev_ns IS 'Allan Deviation для phc2sys';

CREATE UNIQUE INDEX idx_phc2sys_quality_metrics_time_node ON timesync.phc2sys_quality_metrics (time DESC, node_id, window_size);

GRANT SELECT, INSERT, UPDATE ON timesync.phc2sys_quality_metrics TO timesync_app;
GRANT SELECT ON timesync.phc2sys_quality_metrics TO timesync_user;
GRANT ALL ON timesync.phc2sys_quality_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.phc2sys_quality_metrics', INTERVAL '3 days');

CREATE TABLE IF NOT EXISTS timesync.pps_quality_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    window_size INT NOT NULL,
    mtie_ns BIGINT,
    tdev_ns FLOAT,
    adev_ns FLOAT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.pps_quality_metrics', 'time');

ALTER TABLE timesync.pps_quality_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.pps_quality_metrics', INTERVAL '10 minutes');

COMMENT ON TABLE timesync.pps_quality_metrics IS 'Расширенные метрики с временными окнами по pps синхронизации';
COMMENT ON COLUMN timesync.pps_quality_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.pps_quality_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.pps_quality_metrics.window_size IS 'Размер окна вычисления метрик';
COMMENT ON COLUMN timesync.pps_quality_metrics.mtie_ns IS 'Максимальный временной сдвиг за скользящее окно длиной tau для pps';
COMMENT ON COLUMN timesync.pps_quality_metrics.tdev_ns IS 'Стандартизированная мера нестабильности, производная от MTIE для pps';
COMMENT ON COLUMN timesync.pps_quality_metrics.adev_ns IS 'Allan Deviation для pps';

CREATE UNIQUE INDEX idx_pps_quality_metrics_time_node ON timesync.pps_quality_metrics (time DESC, node_id, window_size);

GRANT SELECT, INSERT, UPDATE ON timesync.pps_quality_metrics TO timesync_app;
GRANT SELECT ON timesync.pps_quality_metrics TO timesync_user;
GRANT ALL ON timesync.pps_quality_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.pps_quality_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.ptp4l_quality_metrics CASCADE;
DROP TABLE IF EXISTS timesync.phc2sys_quality_metrics CASCADE;
DROP TABLE IF EXISTS timesync.pps_quality_metrics CASCADE;


