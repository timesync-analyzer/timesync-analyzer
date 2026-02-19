
-- +migrate Up
ALTER TABLE timesync.nodes ADD COLUMN last_seen_at TIMESTAMPTZ;
ALTER TABLE timesync.nodes ADD CONSTRAINT nodes_hostname_unique UNIQUE (hostname);

-- +migrate Down
ALTER TABLE timesync.nodes DROP CONSTRAINT IF EXISTS nodes_hostname_unique;
ALTER TABLE timesync.nodes DROP COLUMN IF EXISTS last_seen_at;
