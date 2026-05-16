#!/usr/bin/env bash
set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<'SQL'
DO
$$
BEGIN
   IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'dropoutbox_replication') THEN
      CREATE ROLE dropoutbox_replication LOGIN PASSWORD 'dropoutbox_replication';
   END IF;
END
$$;
SQL
