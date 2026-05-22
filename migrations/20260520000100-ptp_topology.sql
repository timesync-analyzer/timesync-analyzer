
-- +migrate Up

CREATE TABLE IF NOT EXISTS timesync.ptp_clocks (
    clock_id SERIAL PRIMARY KEY,
    clock_identity VARCHAR(64) NOT NULL UNIQUE,
    node_id INTEGER,
    kind VARCHAR(32) NOT NULL DEFAULT 'unknown',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (node_id) REFERENCES timesync.nodes(node_id)
);

COMMENT ON TABLE timesync.ptp_clocks IS 'PTP clock identities observed in topology snapshots';
COMMENT ON COLUMN timesync.ptp_clocks.clock_id IS 'Internal numeric PTP clock identifier';
COMMENT ON COLUMN timesync.ptp_clocks.clock_identity IS 'External PTP clockIdentity';
COMMENT ON COLUMN timesync.ptp_clocks.node_id IS 'Known analyzer node mapped to this clockIdentity, when available';
COMMENT ON COLUMN timesync.ptp_clocks.kind IS 'Observed role hint for this clock: local, parent, grandmaster, or unknown';
COMMENT ON COLUMN timesync.ptp_clocks.first_seen_at IS 'First time this clockIdentity was observed';
COMMENT ON COLUMN timesync.ptp_clocks.last_seen_at IS 'Last time this clockIdentity was observed';

CREATE TABLE IF NOT EXISTS timesync.ptp_topology_edges (
    time TIMESTAMPTZ NOT NULL,
    edge_kind VARCHAR(16) NOT NULL,
    child_clock_id INTEGER NOT NULL,
    child_port INT,
    parent_clock_id INTEGER NOT NULL,
    parent_port INT,
    grandmaster_clock_id INTEGER,
    steps_removed INT,
    path_delay_ns BIGINT,
    source VARCHAR(32) NOT NULL DEFAULT 'ptp4l',
    CHECK (edge_kind IN ('observed', 'inferred')),
    FOREIGN KEY (child_clock_id) REFERENCES timesync.ptp_clocks(clock_id),
    FOREIGN KEY (parent_clock_id) REFERENCES timesync.ptp_clocks(clock_id),
    FOREIGN KEY (grandmaster_clock_id) REFERENCES timesync.ptp_clocks(clock_id)
);

SELECT create_hypertable('timesync.ptp_topology_edges', 'time',
                         chunk_time_interval => INTERVAL '1 hour');

ALTER TABLE timesync.ptp_topology_edges SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'child_clock_id, edge_kind',
    timescaledb.compress_orderby = 'time DESC'
);
SELECT add_compression_policy('timesync.ptp_topology_edges', INTERVAL '1 hour');

COMMENT ON TABLE timesync.ptp_topology_edges IS 'Observed and inferred PTP topology edges over time';
COMMENT ON COLUMN timesync.ptp_topology_edges.time IS 'Snapshot timestamp';
COMMENT ON COLUMN timesync.ptp_topology_edges.edge_kind IS 'observed for parent->local, inferred for grandmaster->parent hint';
COMMENT ON COLUMN timesync.ptp_topology_edges.child_clock_id IS 'Downstream PTP clock internal identifier';
COMMENT ON COLUMN timesync.ptp_topology_edges.child_port IS 'Local child/slave port number, when known';
COMMENT ON COLUMN timesync.ptp_topology_edges.parent_clock_id IS 'Upstream PTP parent clock internal identifier';
COMMENT ON COLUMN timesync.ptp_topology_edges.parent_port IS 'Upstream parent port number, when known';
COMMENT ON COLUMN timesync.ptp_topology_edges.grandmaster_clock_id IS 'Grandmaster clock internal identifier reported by the child clock';
COMMENT ON COLUMN timesync.ptp_topology_edges.steps_removed IS 'PTP stepsRemoved value reported by the child clock';
COMMENT ON COLUMN timesync.ptp_topology_edges.path_delay_ns IS 'Mean path delay to the parent clock in nanoseconds';
COMMENT ON COLUMN timesync.ptp_topology_edges.source IS 'Topology data source';

CREATE UNIQUE INDEX idx_ptp_topology_edges ON timesync.ptp_topology_edges (time DESC, child_clock_id, parent_clock_id, edge_kind);
CREATE INDEX idx_ptp_topology_edges_current ON timesync.ptp_topology_edges (edge_kind, child_clock_id, time DESC);
CREATE INDEX idx_ptp_topology_edges_parent ON timesync.ptp_topology_edges (parent_clock_id, time DESC);

CREATE OR REPLACE VIEW timesync.current_ptp_topology_edges AS
SELECT DISTINCT ON (e.edge_kind, e.child_clock_id)
    e.time,
    e.edge_kind,
    e.child_clock_id,
    child.clock_identity AS child_clock_identity,
    child.node_id AS child_node_id,
    e.child_port,
    e.parent_clock_id,
    parent.clock_identity AS parent_clock_identity,
    parent.node_id AS parent_node_id,
    e.parent_port,
    e.grandmaster_clock_id,
    grandmaster.clock_identity AS grandmaster_identity,
    e.steps_removed,
    e.path_delay_ns,
    e.source
FROM timesync.ptp_topology_edges e
JOIN timesync.ptp_clocks child ON child.clock_id = e.child_clock_id
JOIN timesync.ptp_clocks parent ON parent.clock_id = e.parent_clock_id
LEFT JOIN timesync.ptp_clocks grandmaster ON grandmaster.clock_id = e.grandmaster_clock_id
ORDER BY e.edge_kind, e.child_clock_id, e.time DESC;

CREATE OR REPLACE VIEW timesync.latest_ptp_port_states AS
SELECT DISTINCT ON (node_id, port)
    time,
    node_id,
    port,
    interface,
    from_state,
    to_state,
    event_trigger
FROM timesync.ptp4l_port_events
WHERE port > 0
ORDER BY node_id, port, time DESC;

CREATE OR REPLACE VIEW timesync.ptp_node_roles AS
WITH states AS (
    SELECT
        node_id,
        bool_or(to_state = 'SLAVE') AS has_slave_port,
        bool_or(to_state = 'MASTER') AS has_master_port,
        bool_or(to_state = 'FAULTY') AS has_faulty_port,
        bool_or(to_state = 'UNCALIBRATED') AS has_uncalibrated_port,
        bool_or(to_state = 'LISTENING') AS has_listening_port
    FROM timesync.latest_ptp_port_states
    GROUP BY node_id
)
SELECT
    n.node_id,
    n.hostname,
    CASE
        WHEN s.has_slave_port AND s.has_master_port THEN 'boundary_clock'
        WHEN s.has_slave_port THEN 'ordinary_slave'
        WHEN s.has_master_port THEN 'master_or_grandmaster'
        WHEN s.has_faulty_port THEN 'faulty'
        WHEN s.has_uncalibrated_port THEN 'uncalibrated'
        WHEN s.has_listening_port THEN 'listening'
        ELSE 'unknown'
    END AS inferred_role
FROM timesync.nodes n
LEFT JOIN states s ON s.node_id = n.node_id;

GRANT SELECT, INSERT, UPDATE ON timesync.ptp_clocks TO timesync_app;
GRANT SELECT, INSERT, UPDATE ON timesync.ptp_topology_edges TO timesync_app;
GRANT USAGE, SELECT ON SEQUENCE timesync.ptp_clocks_clock_id_seq TO timesync_app;
GRANT SELECT ON timesync.ptp_clocks TO timesync_user;
GRANT SELECT ON timesync.ptp_topology_edges TO timesync_user;
GRANT SELECT ON timesync.current_ptp_topology_edges TO timesync_user;
GRANT SELECT ON timesync.latest_ptp_port_states TO timesync_user;
GRANT SELECT ON timesync.ptp_node_roles TO timesync_user;
GRANT ALL ON timesync.ptp_clocks TO timesync_admin;
GRANT ALL ON timesync.ptp_topology_edges TO timesync_admin;
GRANT ALL ON SEQUENCE timesync.ptp_clocks_clock_id_seq TO timesync_admin;
GRANT ALL ON timesync.current_ptp_topology_edges TO timesync_admin;
GRANT ALL ON timesync.latest_ptp_port_states TO timesync_admin;
GRANT ALL ON timesync.ptp_node_roles TO timesync_admin;

SELECT add_retention_policy('timesync.ptp_topology_edges', INTERVAL '3 days');

-- +migrate Down

DROP VIEW IF EXISTS timesync.ptp_node_roles;
DROP VIEW IF EXISTS timesync.latest_ptp_port_states;
DROP VIEW IF EXISTS timesync.current_ptp_topology_edges;
DROP TABLE IF EXISTS timesync.ptp_topology_edges CASCADE;
DROP TABLE IF EXISTS timesync.ptp_clocks CASCADE;
