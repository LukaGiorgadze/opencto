#!/bin/sh
# @@@SNIPSTART compose-postgres-setup
set -eu

# Validate required environment variables
: "${POSTGRES_SEEDS:?ERROR: POSTGRES_SEEDS environment variable is required}"
: "${POSTGRES_USER:?ERROR: POSTGRES_USER environment variable is required}"
: "${POSTGRES_PWD:?ERROR: POSTGRES_PWD environment variable is required}"

SQL_PLUGIN="${SQL_PLUGIN:-postgres12}"
SQL_PORT="${DB_PORT:-5432}"

run_sql_tool() {
  database="$1"
  shift

  temporal-sql-tool \
    --plugin "${SQL_PLUGIN}" \
    --ep "${POSTGRES_SEEDS}" \
    -u "${POSTGRES_USER}" \
    --pw "${POSTGRES_PWD}" \
    -p "${SQL_PORT}" \
    --db "${database}" \
    "$@"
}

echo 'Starting PostgreSQL schema setup...'
echo 'Waiting for PostgreSQL port to be available...'
nc -z -w 10 "${POSTGRES_SEEDS}" "${SQL_PORT}"
echo 'PostgreSQL port is available'

# Create and setup temporal database
run_sql_tool temporal --quiet create
run_sql_tool temporal --quiet setup-schema -v 0.0
run_sql_tool temporal update-schema -d /etc/temporal/schema/postgresql/v12/temporal/versioned

# Create and setup visibility database
run_sql_tool temporal_visibility --quiet create
run_sql_tool temporal_visibility --quiet setup-schema -v 0.0
run_sql_tool temporal_visibility update-schema -d /etc/temporal/schema/postgresql/v12/visibility/versioned

echo 'PostgreSQL schema setup complete'
# @@@SNIPEND
