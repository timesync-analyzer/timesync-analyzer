#!/bin/sh
set -eu

export PGHOST="${DB_HOST:-timescaledb}" PGPORT="${DB_PORT:-5432}"
export PGUSER="${POSTGRES_USER:?}" PGPASSWORD="${POSTGRES_PASSWORD:?}"
export PGDATABASE="${POSTGRES_DB:?}" PGSSLMODE=disable PGCONNECT_TIMEOUT=10

case "${1:-up}" in
    up)
        psql -X -v ON_ERROR_STOP=1 -f /app/deploy/bootstrap.sql
        exec sql-migrate up -config=/app/deploy/migrations.yaml -env=production
        ;;
    status)
        exec sql-migrate status -config=/app/deploy/migrations.yaml -env=production
        ;;
    *) echo 'Usage: migrate.sh [up|status]' >&2; exit 2 ;;
esac
