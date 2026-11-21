-- Roles for the Atmosidea auth-db cluster.
-- Runs once on a fresh volume via docker-entrypoint-initdb.d/init.sql.
-- auth-service connects as auth_service_user.
-- The password is NOT set here: the db-role-init service (backend/db-init/sync-role-passwords.sh)
-- sets it from AUTH_SERVICE_DB_PASSWORD in .env on every startup.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'auth_service_user') THEN
    CREATE ROLE auth_service_user WITH LOGIN;
  END IF;
END
$$;

-- Allow the app role to use auth_db and create its own tables on a fresh schema.
GRANT CONNECT ON DATABASE auth_db TO auth_service_user;
GRANT USAGE, CREATE ON SCHEMA public TO auth_service_user;

-- users table (used by auth-service)
-- columns required by registerHandler (INSERT), loginHandler (SELECT), googleCallbackHandler (INSERT)
CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY,
    username      VARCHAR(255),
    google_name   VARCHAR(255),
    email         VARCHAR(255) NOT NULL,
    password_hash VARCHAR(255),
    provider      VARCHAR(50) NOT NULL DEFAULT 'local',
    provider_id   VARCHAR(255),
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    status        VARCHAR(50) NOT NULL DEFAULT 'active',
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, email),
    UNIQUE(provider, provider_id)
);

-- No default admin is seeded (a known password would be public).
-- Create admins via POST /api/auth/register with ADMIN_REGISTRATION_CODE.

-- Allow the app role to read/write the users table (used by registerHandler,
-- loginHandler, and googleCallbackHandler). Applied on every fresh volume init.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE users TO auth_service_user;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO auth_service_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO auth_service_user;