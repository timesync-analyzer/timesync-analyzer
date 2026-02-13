
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.cpu_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    usage_percent FLOAT,
    context_switches BIGINT,
    interrupts BIGINT,
    softirqs BIGINT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.cpu_metrics', 'time');

ALTER TABLE timesync.cpu_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.cpu_metrics', INTERVAL '10 minutes');

COMMENT ON TABLE timesync.cpu_metrics IS 'Метрики CPU';
COMMENT ON COLUMN timesync.cpu_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.cpu_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.cpu_metrics.usage_percent IS 'Использование CPU в процентах';
COMMENT ON COLUMN timesync.cpu_metrics.context_switches IS 'Количество переключения контекстов';
COMMENT ON COLUMN timesync.cpu_metrics.interrupts IS 'Количество прерываний';
COMMENT ON COLUMN timesync.cpu_metrics.softirqs IS 'Количество программных прерываний';

CREATE UNIQUE INDEX idx_cpu_node_time ON timesync.cpu_metrics (node_id, time DESC);

GRANT SELECT, INSERT, UPDATE ON timesync.cpu_metrics TO timesync_app;
GRANT SELECT ON timesync.cpu_metrics TO timesync_user;
GRANT ALL ON timesync.cpu_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.cpu_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.cpu_metrics CASCADE;