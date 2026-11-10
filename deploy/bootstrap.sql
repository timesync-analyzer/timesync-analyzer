\getenv app_user APP_USER
\getenv app_password APP_PASSWORD
\getenv admin_user ADMIN_USER
\getenv admin_password ADMIN_PASSWORD
\getenv readonly_user READONLY_USER
\getenv readonly_password READONLY_PASSWORD

BEGIN;
SELECT format('CREATE ROLE %I', role_name)
FROM (VALUES ('timesync_app'), ('timesync_admin'), ('timesync_user')) AS roles(role_name)
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name)
\gexec

-- Preserve existing passwords when restarting or upgrading.
SELECT format('CREATE USER %I PASSWORD %L', user_name, user_password)
FROM (VALUES (:'app_user', :'app_password'),
             (:'admin_user', :'admin_password'),
             (:'readonly_user', :'readonly_password')) AS users(user_name, user_password)
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = user_name)
\gexec

GRANT timesync_app TO :"app_user";
GRANT timesync_admin TO :"admin_user";
GRANT timesync_user TO :"readonly_user";
CREATE EXTENSION IF NOT EXISTS timescaledb;
COMMIT;
