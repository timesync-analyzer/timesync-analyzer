#!/bin/bash
set -e

echo "Creating database roles and users..."

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    -- Создание ролей
    DO \$\$
    BEGIN
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'timesync_admin') THEN
            CREATE ROLE timesync_admin;
        END IF;

        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'timesync_app') THEN
            CREATE ROLE timesync_app;
        END IF;

        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'timesync_user') THEN
            CREATE ROLE timesync_user;
        END IF;
    END
    \$\$;

    -- Создание пользователей
    DO \$\$
    BEGIN
        -- App user
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${APP_USER}') THEN
            CREATE USER ${APP_USER} WITH PASSWORD '${APP_PASSWORD}';
        END IF;
        GRANT timesync_app TO ${APP_USER};

        -- Admin user
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${ADMIN_USER}') THEN
            CREATE USER ${ADMIN_USER} WITH PASSWORD '${ADMIN_PASSWORD}';
        END IF;
        GRANT timesync_admin TO ${ADMIN_USER};

        -- Readonly user
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${READONLY_USER}') THEN
            CREATE USER ${READONLY_USER} WITH PASSWORD '${READONLY_PASSWORD}';
        END IF;
        GRANT timesync_user TO ${READONLY_USER};
    END
    \$\$;

EOSQL

echo "Users created successfully!"