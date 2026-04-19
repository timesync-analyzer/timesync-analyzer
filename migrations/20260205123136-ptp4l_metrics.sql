
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.ptp4l_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    offset_ns BIGINT,
    frequency BIGINT,
    path_delay BIGINT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.ptp4l_metrics', 'time',
                         chunk_time_interval => INTERVAL '1 hour');

ALTER TABLE timesync.ptp4l_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.ptp4l_metrics', INTERVAL '1 hour');

COMMENT ON TABLE timesync.ptp4l_metrics IS 'Метрики по ptp4l синхронизации';
COMMENT ON COLUMN timesync.ptp4l_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.ptp4l_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.ptp4l_metrics.offset_ns IS 'Смещение в наносекундах';
COMMENT ON COLUMN timesync.ptp4l_metrics.frequency IS 'Частота';
COMMENT ON COLUMN timesync.ptp4l_metrics.path_delay IS 'Задержка пути в наносекундах';

CREATE UNIQUE INDEX idx_ptp4l_time_node ON timesync.ptp4l_metrics (node_id, time DESC);

GRANT SELECT, INSERT, UPDATE ON timesync.ptp4l_metrics TO timesync_app;
GRANT SELECT ON timesync.ptp4l_metrics TO timesync_user;
GRANT ALL ON timesync.ptp4l_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.ptp4l_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.ptp4l_metrics CASCADE;