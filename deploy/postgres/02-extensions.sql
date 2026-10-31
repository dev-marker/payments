-- Statistics on every query, in every database: pg_stat_statements, which
-- docker-compose.yml loads at startup. Only the superuser reads them all.
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
