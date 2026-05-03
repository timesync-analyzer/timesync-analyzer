
-- +migrate Up

CREATE UNIQUE INDEX IF NOT EXISTS idx_pps_node_time
ON timesync.pps_metrics (node_id,time DESC);

DROP INDEX IF EXISTS timesync.idx_pps_time_node;

-- +migrate Down

CREATE UNIQUE INDEX IF NOT EXISTS idx_pps_time_node
ON timesync.pps_metrics (time DESC,node_id);

DROP INDEX IF EXISTS timesync.idx_pps_node_time;