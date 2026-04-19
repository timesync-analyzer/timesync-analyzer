
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.ptp4l_port_events (
    time TIMESTAMPTZ NOT NULL,
    node_id INTEGER NOT NULL,
    port INT,
    interface VARCHAR(35),
    from_state VARCHAR(35),
    to_state VARCHAR(35),
    event_trigger VARCHAR(35),
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

SELECT create_hypertable('timesync.ptp4l_port_events', 'time',
                         chunk_time_interval => INTERVAL '1 hour');

ALTER TABLE timesync.ptp4l_port_events SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'node_id',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.ptp4l_port_events', INTERVAL '1 hour');

COMMENT ON TABLE timesync.ptp4l_port_events IS 'События, происходящие при ptp4l синхронизации';
COMMENT ON COLUMN timesync.ptp4l_port_events.time IS 'Время события';
COMMENT ON COLUMN timesync.ptp4l_port_events.node_id IS 'Уникальный идентификатор узла, на котором случилось событие';
COMMENT ON COLUMN timesync.ptp4l_port_events.port IS 'Порт, на котором случилось событие';
COMMENT ON COLUMN timesync.ptp4l_port_events.interface IS 'Интерфейс, на котором случилось событие';
COMMENT ON COLUMN timesync.ptp4l_port_events.from_state IS 'Изначальное состояние';
COMMENT ON COLUMN timesync.ptp4l_port_events.to_state IS 'Конечное состояние';
COMMENT ON COLUMN timesync.ptp4l_port_events.event_trigger IS 'Что послужило триггером';

CREATE UNIQUE INDEX idx_ptp4l_port_events ON timesync.ptp4l_port_events (time DESC, node_id, port);

GRANT SELECT, INSERT, UPDATE ON timesync.ptp4l_port_events TO timesync_app;
GRANT SELECT ON timesync.ptp4l_port_events TO timesync_user;
GRANT ALL ON timesync.ptp4l_port_events TO timesync_admin;

SELECT add_retention_policy('timesync.ptp4l_port_events', INTERVAL '3 days');

-- +migrate Down

DROP TABLE IF EXISTS timesync.ptp4l_port_events CASCADE;