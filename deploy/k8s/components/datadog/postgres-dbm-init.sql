-- Datadog Database Monitoring (DBM) bootstrap for a Postgres instance.
--
-- Mounted into /docker-entrypoint-initdb.d by the datadog Kustomize component,
-- so it runs once on first boot against this instance's POSTGRES_DB (auth_db /
-- inventory_db / shipment_db). It is written to be fully IDEMPOTENT so it can
-- also be re-applied by hand against an already-initialised volume (initdb
-- scripts only fire on an empty data dir):
--
--   kubectl -n logistics exec -i postgres-auth-0 -- \
--     psql -U "$POSTGRES_USER" -d auth_db < postgres-dbm-init.sql
--
-- Creates the `datadog` login role the Agent's postgres check authenticates as,
-- plus the schema + explain helper + pg_stat_statements that DBM needs. The
-- pg_stat_statements *library* is loaded separately via -c
-- shared_preload_libraries on the StatefulSet (a server start flag), which is
-- why the matching rollout recreates the pod.
--
-- Local-dev password only (matches POSTGRES_PASSWORD=logistics living in the
-- overlay); do not reuse this pattern for a shared/cloud database.

-- Login role the Agent connects as. pg_monitor bundles pg_read_all_settings,
-- pg_read_all_stats and pg_stat_scan_tables — everything the check reads.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'datadog') THEN
    CREATE ROLE datadog WITH LOGIN PASSWORD 'datadog';
  END IF;
END
$$;
ALTER ROLE datadog INHERIT;
GRANT pg_monitor TO datadog;

-- DBM stores its explain helper in a dedicated schema; the check also needs to
-- read object names out of public.
CREATE SCHEMA IF NOT EXISTS datadog AUTHORIZATION datadog;
GRANT USAGE ON SCHEMA datadog TO datadog;
GRANT USAGE ON SCHEMA public TO datadog;

-- Per-statement metrics source for DBM.
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

-- SECURITY DEFINER so the unprivileged datadog role can obtain EXPLAIN plans.
-- Owned by the bootstrapping superuser (POSTGRES_USER), which is what makes the
-- elevated EXPLAIN possible.
CREATE OR REPLACE FUNCTION datadog.explain_statement(
  l_query TEXT,
  OUT explain JSON
)
RETURNS SETOF JSON AS
$$
DECLARE
  curs REFCURSOR;
  plan JSON;
BEGIN
  OPEN curs FOR EXECUTE pg_catalog.concat('EXPLAIN (FORMAT JSON) ', l_query);
  FETCH curs INTO plan;
  CLOSE curs;
  RETURN QUERY SELECT plan;
END;
$$
LANGUAGE 'plpgsql'
RETURNS NULL ON NULL INPUT
SECURITY DEFINER;
