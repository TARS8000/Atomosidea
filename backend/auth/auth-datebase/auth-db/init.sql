CREATE TABLE IF NOT EXISTS users (
                                     id UUID PRIMARY KEY,
                                     username VARCHAR(255),
    google_name VARCHAR(255),
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255),
    provider VARCHAR(50) NOT NULL,
    provider_id VARCHAR(255),
    is_admin BOOLEAN DEFAULT FALSE,
    icon_url TEXT,
    bio TEXT,
    background_image_url TEXT,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
    );

-- タイムスタンプ自動更新トリガー
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_users_updated_at ON public.users;
CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON public.users
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- ロール作成用の補助関数（環境変数を動的に取り出す）
DO $$
DECLARE
auth_pwd text := current_setting('custom.auth_service_db_password', true);
    mypage_pwd text := current_setting('custom.mypage_service_db_password', true);
BEGIN
    IF auth_pwd IS NOT NULL AND auth_pwd <> '' THEN
        IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'auth_service_user') THEN
            EXECUTE format('CREATE ROLE auth_service_user WITH LOGIN PASSWORD %L', auth_pwd);
ELSE
            EXECUTE format('ALTER ROLE auth_service_user WITH PASSWORD %L', auth_pwd);
END IF;
END IF;

    IF mypage_pwd IS NOT NULL AND mypage_pwd <> '' THEN
        IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'mypage_service_user') THEN
            EXECUTE format('CREATE ROLE mypage_service_user WITH LOGIN PASSWORD %L', mypage_pwd);
ELSE
            EXECUTE format('ALTER ROLE mypage_service_user WITH PASSWORD %L', mypage_pwd);
END IF;
END IF;
END
$$;

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.users TO auth_service_user;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO auth_service_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO auth_service_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO auth_service_user;

GRANT SELECT ON TABLE public.users TO mypage_service_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO mypage_service_user;