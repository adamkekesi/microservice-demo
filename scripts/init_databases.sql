-- Runs once on first Postgres init (mounted into /docker-entrypoint-initdb.d).
-- Creates one logical database per service, satisfying "separate logical DB
-- per service" for local development.
CREATE DATABASE auth_db;
CREATE DATABASE inventory_db;
CREATE DATABASE shipment_db;
