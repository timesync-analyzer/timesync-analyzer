
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.pps_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    offset_ns BIGINT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.pps_metrics', 'time',
                         chunk_time_interval => INTERVAL '1 hour');

ALTER TABLE timesync.pps_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.pps_metrics', INTERVAL '1 hour');

COMMENT ON TABLE timesync.pps_metrics IS 'Метрики по pps синхронизации';
COMMENT ON COLUMN timesync.pps_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.pps_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.pps_metrics.offset_ns IS 'Смещение в наносекундах';

CREATE UNIQUE INDEX idx_pps_time_node ON timesync.pps_metrics (time DESC, node_id);

GRANT SELECT, INSERT, UPDATE ON timesync.pps_metrics TO timesync_app;
GRANT SELECT ON timesync.pps_metrics TO timesync_user;
GRANT ALL ON timesync.pps_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.pps_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.pps_metrics CASCADE;

