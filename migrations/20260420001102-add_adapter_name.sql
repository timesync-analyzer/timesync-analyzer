-- +migrate Up

ALTER TABLE timesync.nodes
    ADD COLUMN adapter_name VARCHAR(255);

COMMENT ON COLUMN timesync.nodes.adapter_name IS 'Человекочитаемое имя PCI-адаптера для текущего интерфейса';

-- +migrate Down

ALTER TABLE timesync.nodes
    DROP COLUMN IF EXISTS adapter_name;
