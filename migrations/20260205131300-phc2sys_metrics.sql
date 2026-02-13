
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.phc2sys_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    offset_ns BIGINT,
    frequency BIGINT,
    path_delay BIGINT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.phc2sys_metrics', 'time');

ALTER TABLE timesync.phc2sys_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.phc2sys_metrics', INTERVAL '10 minutes');

COMMENT ON TABLE timesync.phc2sys_metrics IS 'Метрики по phc2sys синхронизации';
COMMENT ON COLUMN timesync.phc2sys_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.phc2sys_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.phc2sys_metrics.offset_ns IS 'Смещение в наносекундах';
COMMENT ON COLUMN timesync.phc2sys_metrics.frequency IS 'Частота';
COMMENT ON COLUMN timesync.phc2sys_metrics.path_delay IS 'Задержка пути в наносекундах';

CREATE UNIQUE INDEX idx_phc2sys_node_time ON timesync.phc2sys_metrics (node_id, time DESC);

GRANT SELECT, INSERT, UPDATE ON timesync.phc2sys_metrics TO timesync_app;
GRANT SELECT ON timesync.phc2sys_metrics TO timesync_user;
GRANT ALL ON timesync.phc2sys_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.phc2sys_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.phc2sys_metrics CASCADE;