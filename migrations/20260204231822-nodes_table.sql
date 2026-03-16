
-- +migrate Up

CREATE SCHEMA timesync;

GRANT USAGE ON SCHEMA timesync TO timesync_app;
GRANT USAGE ON SCHEMA timesync TO readonly_user;
GRANT SELECT ON ALL TABLES IN SCHEMA timesync TO readonly_user;

CREATE TYPE NODE_TYPE AS ENUM ('master', 'slave');

CREATE TABLE timesync.nodes (
    node_id SERIAL PRIMARY KEY,
    hostname VARCHAR(30) NOT NULL,
    interface VARCHAR(10) NOT NULL,
    ip_address INET NOT NULL,
    type NODE_TYPE NOT NULL,
    is_active BOOLEAN DEFAULT FALSE
);

COMMENT ON TABLE timesync.nodes IS 'Информация о вычислительных узлах кластера';
COMMENT ON COLUMN timesync.nodes.node_id IS 'Уникальный идентификатор узла';
COMMENT ON COLUMN timesync.nodes.hostname IS 'Имя хоста узла';
COMMENT ON COLUMN timesync.nodes.ip_address IS 'IP адрес узла';
COMMENT ON COLUMN timesync.nodes.type IS 'Тип узла: master, slave';
COMMENT ON COLUMN timesync.nodes.is_active IS 'Активен ли узел в данный момент';

GRANT SELECT, INSERT, UPDATE ON timesync.nodes TO timesync_app;
GRANT USAGE, SELECT ON SEQUENCE timesync.nodes_node_id_seq TO timesync_app;
GRANT SELECT ON timesync.nodes TO timesync_user;
GRANT ALL ON timesync.nodes TO timesync_admin;

-- +migrate Down

DROP TABLE IF EXISTS timesync.nodes CASCADE;

DROP SCHEMA IF EXISTS timesync;

DROP TYPE IF EXISTS  NODE_TYPE;
