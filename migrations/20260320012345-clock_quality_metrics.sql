
-- +migrate Up

CREATE TYPE PROTOCOL AS ENUM ('ptp4l', 'pps', 'phc2sys');

CREATE TABLE IF NOT EXISTS timesync.quality_metrics (
    time TIMESTAMPTZ NOT NULL,
    sync_protocol PROTOCOL NOT NULL,
    node_id INTEGER NOT NULL,
    window_size_s INT NOT NULL,
    mtie_ns BIGINT,
    tdev_ns FLOAT,
    adev_ns FLOAT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.quality_metrics', 'time',
                         chunk_time_interval => INTERVAL '1 hour');

ALTER TABLE timesync.quality_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id, sync_protocol',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.quality_metrics', INTERVAL '1 hour');

COMMENT ON TABLE timesync.quality_metrics IS 'Расширенные метрики с временными окнами по синхронизации';
COMMENT ON COLUMN timesync.quality_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.quality_metrics.sync_protocol IS 'Протокол синхронизации, для которого рассчитана метрика';
COMMENT ON COLUMN timesync.quality_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.quality_metrics.window_size_s IS 'Размер окна вычисления метрик';
COMMENT ON COLUMN timesync.quality_metrics.mtie_ns IS 'Максимальный временной сдвиг за скользящее окно длиной tau';
COMMENT ON COLUMN timesync.quality_metrics.tdev_ns IS 'Стандартизированная мера нестабильности, производная от MTIE';
COMMENT ON COLUMN timesync.quality_metrics.adev_ns IS 'Allan Deviation';

CREATE UNIQUE INDEX idx_quality_metrics_time_node ON timesync.quality_metrics (time DESC, node_id, window_size_s, sync_protocol);

GRANT SELECT, INSERT, UPDATE ON timesync.quality_metrics TO timesync_app;
GRANT SELECT ON timesync.quality_metrics TO timesync_user;
GRANT ALL ON timesync.quality_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.quality_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.quality_metrics CASCADE;
DROP TYPE IF EXISTS PROTOCOL;
