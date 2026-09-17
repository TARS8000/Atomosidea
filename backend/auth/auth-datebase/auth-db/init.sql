-- Roles for the Atmosidea auth-db cluster.
-- Runs once on a fresh volume via docker-entrypoint-initdb.d/init.sql.
-- auth-service connects as auth_service_user; mypage-service as mypage_service_user.
-- Passwords mirror AUTH_SERVICE_DB_PASSWORD / MYPAGE_SERVICE_DB_PASSWORD in .env.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'auth_service_user') THEN
    CREATE ROLE auth_service_user WITH LOGIN PASSWORD 'auth_service_strong_password';
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'mypage_service_user') THEN
    CREATE ROLE mypage_service_user WITH LOGIN PASSWORD 'mypage_service_strong_password';
  END IF;
END
$$;

-- Allow the app roles to use auth_db and create their own tables on a fresh schema.
GRANT CONNECT ON DATABASE auth_db TO auth_service_user, mypage_service_user;
GRANT USAGE, CREATE ON SCHEMA public TO auth_service_user, mypage_service_user;

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

-- Default admin user (password: AdminPassword123!). Inserted only if absent.
-- Hash generated with pgcrypto crypt('AdminPassword123!', gen_salt('bf')) for PostgreSQL 16.
INSERT INTO users (id, username, email, password_hash, provider, is_admin, status)
SELECT gen_random_uuid(), 'admin', 'admin@internal.local',
       '$2a$06$KJWRVBanouBTqLqoyl093uNTqJWJzOYo0xt2ltyNtHBvyBLvZYrNO',
       'local', TRUE, 'active'
WHERE NOT EXISTS (SELECT 1 FROM users WHERE email = 'admin@internal.local');

-- Allow the app roles to read/write the shared users table (used by registerHandler,
-- loginHandler, and googleCallbackHandler). Applied on every fresh volume init.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE users TO auth_service_user, mypage_service_user;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO auth_service_user, mypage_service_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO auth_service_user, mypage_service_user;