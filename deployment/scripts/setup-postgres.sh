#!/bin/sh
# set -eu

# SQL_PLUGIN="${SQL_PLUGIN:-postgres12}"
# SQL_HOST="${POSTGRES_SEEDS:-postgresql}"
# SQL_PORT="${DB_PORT:-5432}"
# SQL_USER="temporal"
# SQL_PASSWORD="temporal"

# export SQL_PLUGIN SQL_HOST SQL_PORT SQL_USER SQL_PASSWORD

# setup_db() {
#   database_name="$1"
#   schema_name="$2"

#   temporal-sql-tool --database "${database_name}" create-database || true
#   SQL_DATABASE="${database_name}" temporal-sql-tool setup-schema -v 0.0
#   # Use the schema embedded in the Temporal admin-tools image instead of
#   # relying on a relative filesystem path inside the container.
#   SQL_DATABASE="${database_name}" temporal-sql-tool update-schema --schema-name "${schema_name}"
# }

# setup_db temporal postgresql/v12/temporal
# setup_db temporal_visibility postgresql/v12/visibility
