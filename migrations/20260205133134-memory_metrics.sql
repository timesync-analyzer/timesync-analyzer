
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.memory_metrics (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    mem_available_kb BIGINT,
    mem_free_kb BIGINT,
    swap_total_kb BIGINT,
    swap_free_kb BIGINT,
    buffers_kb BIGINT,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.memory_metrics', 'time',
                         chunk_time_interval => INTERVAL '6 hours');

ALTER TABLE timesync.memory_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.memory_metrics', INTERVAL '1 hour');

COMMENT ON TABLE timesync.memory_metrics IS 'Метрики RAM';
COMMENT ON COLUMN timesync.memory_metrics.time IS 'Время прихода метрики';
COMMENT ON COLUMN timesync.memory_metrics.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.memory_metrics.mem_available_kb IS 'Количество доступной памяти';
COMMENT ON COLUMN timesync.memory_metrics.mem_free_kb IS 'Количество свободной памяти';
COMMENT ON COLUMN timesync.memory_metrics.swap_total_kb IS 'Количество выделенной памяти для свопа';
COMMENT ON COLUMN timesync.memory_metrics.swap_free_kb IS 'Количество свободной памяти в свопе';
COMMENT ON COLUMN timesync.memory_metrics.buffers_kb IS 'Количество памяти под буферы';

CREATE UNIQUE INDEX idx_memory_node_time ON timesync.memory_metrics (node_id, time DESC);

GRANT SELECT, INSERT, UPDATE ON timesync.memory_metrics TO timesync_app;
GRANT SELECT ON timesync.memory_metrics TO timesync_user;
GRANT ALL ON timesync.memory_metrics TO timesync_admin;

SELECT add_retention_policy('timesync.memory_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.memory_metrics CASCADE;