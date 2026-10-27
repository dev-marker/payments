-- Runs once, when the Postgres data volume is first created.
-- One database per service: each service owns its data, and its owner role
-- owns its database.
CREATE DATABASE wallet OWNER wallet_owner;
CREATE DATABASE auth OWNER auth_owner;
CREATE DATABASE transfer OWNER transfer_owner;
CREATE DATABASE notification OWNER notification_owner;
CREATE DATABASE webhook OWNER webhook_owner;

-- Every role may connect to every database, by default. Not here: each
-- service connects to its own database, and only to its own.
REVOKE CONNECT ON DATABASE wallet, auth, transfer, notification, webhook FROM PUBLIC;
GRANT CONNECT ON DATABASE wallet TO wallet_app;
GRANT CONNECT ON DATABASE auth TO auth_app;
GRANT CONNECT ON DATABASE transfer TO transfer_app;
GRANT CONNECT ON DATABASE notification TO notification_app;
GRANT CONNECT ON DATABASE webhook TO webhook_app;

-- Every table an owner creates, now or in a later migration, its app role may
-- read and write: data, never schema.
\connect wallet
ALTER DEFAULT PRIVILEGES FOR ROLE wallet_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO wallet_app;
\connect auth
ALTER DEFAULT PRIVILEGES FOR ROLE auth_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO auth_app;
\connect transfer
ALTER DEFAULT PRIVILEGES FOR ROLE transfer_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO transfer_app;
\connect notification
ALTER DEFAULT PRIVILEGES FOR ROLE notification_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO notification_app;
\connect webhook
ALTER DEFAULT PRIVILEGES FOR ROLE webhook_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO webhook_app;
