-- Runs first, when the Postgres data volume is created. Two roles for each
-- service: the owner, which its migrations run as, and the app, which the
-- service itself runs as. Development passwords: in production, they come
-- from a secret manager.
CREATE ROLE wallet_owner LOGIN PASSWORD 'wallet_owner';
CREATE ROLE wallet_app LOGIN PASSWORD 'wallet_app';
CREATE ROLE auth_owner LOGIN PASSWORD 'auth_owner';
CREATE ROLE auth_app LOGIN PASSWORD 'auth_app';
CREATE ROLE transfer_owner LOGIN PASSWORD 'transfer_owner';
CREATE ROLE transfer_app LOGIN PASSWORD 'transfer_app';
CREATE ROLE notification_owner LOGIN PASSWORD 'notification_owner';
CREATE ROLE notification_app LOGIN PASSWORD 'notification_app';
CREATE ROLE webhook_owner LOGIN PASSWORD 'webhook_owner';
CREATE ROLE webhook_app LOGIN PASSWORD 'webhook_app';
