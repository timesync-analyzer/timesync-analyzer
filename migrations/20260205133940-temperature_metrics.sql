
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.sensors (
    sensor_id SERIAL PRIMARY KEY,
    node_id INTEGER NOT NULL,
    sensor_name VARCHAR(50) NOT NULL,
    sensor_label VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW() NOT NULL,
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id) ON DELETE CASCADE,
    UNIQUE(node_id, sensor_name, sensor_label)
);

COMMENT ON TABLE timesync.sensors IS 'Справочник температурных сенсоров';
COMMENT ON COLUMN timesync.sensors.sensor_id IS 'Уникальный идентификатор сенсора';
COMMENT ON COLUMN timesync.sensors.node_id IS 'Идентификатор узла';
COMMENT ON COLUMN timesync.sensors.sensor_name IS 'Имя сенсора (например, coretemp-isa-0000)';
COMMENT ON COLUMN timesync.sensors.sensor_label IS 'Метка области измерения (например, Core 0, Package id 0)';
COMMENT ON COLUMN timesync.sensors.created_at IS 'Время регистрации сенсора';

CREATE INDEX idx_sensors_node_id ON timesync.sensors(node_id);

GRANT SELECT, INSERT, UPDATE ON timesync.sensors TO timesync_app;
GRANT USAGE, SELECT ON SEQUENCE timesync.sensors_sensor_id_seq TO timesync_app;
GRANT SELECT ON timesync.sensors TO timesync_user;
GRANT ALL ON timesync.sensors TO timesync_admin;

CREATE TABLE IF NOT EXISTS timesync.temperature_metrics (
    time TIMESTAMPTZ NOT NULL,
    sensor_id INTEGER NOT NULL,
    temperature INTEGER NOT NULL,
    FOREIGN KEY (sensor_id) REFERENCES timesync.sensors(sensor_id) ON DELETE CASCADE
);

SELECT create_hypertable('timesync.temperature_metrics', 'time');

ALTER TABLE timesync.temperature_metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'sensor_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.temperature_metrics', INTERVAL '10 minutes');

COMMENT ON TABLE timesync.temperature_metrics IS 'Метрики температуры';
COMMENT ON COLUMN timesync.temperature_metrics.time IS 'Время измерения';
COMMENT ON COLUMN timesync.temperature_metrics.sensor_id IS 'Идентификатор сенсора';
COMMENT ON COLUMN timesync.temperature_metrics.temperature IS 'Температура в градусах Цельсия';

CREATE INDEX idx_temperature_sensor_time ON timesync.temperature_metrics (sensor_id, time DESC);

GRANT SELECT, INSERT, UPDATE ON timesync.temperature_metrics TO timesync_app;
GRANT SELECT ON timesync.temperature_metrics TO timesync_user;
GRANT ALL ON timesync.temperature_metrics TO timesync_admin;
SELECT add_retention_policy('timesync.temperature_metrics', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.temperature_metrics CASCADE;
DROP TABLE IF EXISTS timesync.sensors CASCADE;