
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.network_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    rx_packets BIGINT,
    tx_packets BIGINT,
    rx_dropped BIGINT,
    tx_dropped BIGINT,
    rx_errors BIGINT,
    tx_errors BIGINT,
    collisions BIGINT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.network_metrics', 'time',
                         chunk_time_interval => INTERVAL '6 hours');

ALTER TABLE timesync.network_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.network_metrics', INTERVAL '1 hour');

COMMENT ON TABLE timesync.network_metrics IS 'Сетевые метрики';
COMMENT ON COLUMN timesync.network_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.network_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.network_metrics.rx_packets IS 'Количество входных пакетов';
COMMENT ON COLUMN timesync.network_metrics.tx_packets IS 'Количество выходных пакетов';
COMMENT ON COLUMN timesync.network_metrics.rx_dropped IS 'Количество отброшенных входных пакетов';
COMMENT ON COLUMN timesync.network_metrics.tx_dropped IS 'Количество отброшенных выходных пакетов';
COMMENT ON COLUMN timesync.network_metrics.rx_errors IS 'Количество входных пакетов с ошибками';
COMMENT ON COLUMN timesync.network_metrics.tx_errors IS 'Количество выходных пакетов с ошибками';
COMMENT ON COLUMN timesync.network_metrics.collisions IS 'Количество коллизий пакетов';

CREATE UNIQUE INDEX idx_network_node_time ON timesync.network_metrics (node_id, time DESC);

GRANT SELECT, INSERT, UPDATE ON timesync.network_metrics TO timesync_app;
GRANT SELECT ON timesync.network_metrics TO timesync_user;
GRANT ALL ON timesync.network_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.network_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.network_metrics CASCADE;