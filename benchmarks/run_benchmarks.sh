#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
BENCHMARK_DIR=${BENCHMARK_DIR:-"$SCRIPT_DIR/benchmarks_db"}
DB_SERVICE=${DB_SERVICE:-timescaledb}
DB_USER=${DB_USER:-postgres}
DB_NAME=${DB_NAME:-timesync}
ENV_FILE=${ENV_FILE:-"$PROJECT_DIR/config/.env"}

cd "$PROJECT_DIR"

for sql_file in "$BENCHMARK_DIR"/[0-9][0-9]_*.sql; do
    if [ ! -f "$sql_file" ]; then
        echo "No benchmark SQL files found in $BENCHMARK_DIR" >&2
        exit 1
    fi

    echo "==> Running $(basename "$sql_file")"
    docker compose --env-file "$ENV_FILE" exec -T "$DB_SERVICE" \
        psql -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 < "$sql_file"
done
