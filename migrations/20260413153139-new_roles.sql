
-- +migrate Up

ALTER TYPE NODE_TYPE ADD VALUE 'grand_master';
ALTER TYPE NODE_TYPE ADD VALUE 'listening';
ALTER TYPE NODE_TYPE ADD VALUE 'uncalibrated';

-- +migrate Down
