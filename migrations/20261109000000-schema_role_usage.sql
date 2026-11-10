-- +migrate Up
-- Older installations granted schema access to a concrete readonly_user login.
GRANT USAGE ON SCHEMA timesync TO timesync_user;
GRANT USAGE ON SCHEMA timesync TO timesync_admin;

-- +migrate Down
-- Keep role access when rolling back this permission repair.
