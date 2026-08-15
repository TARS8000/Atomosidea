-- ユーザーテーブル
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(255) NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255), -- Can be null for OAuth users
    provider VARCHAR(50) NOT NULL DEFAULT 'local',
    provider_id VARCHAR(255),
    is_admin BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, provider_id)
);

-- 動画テーブル
CREATE TABLE IF NOT EXISTS videos (
    id SERIAL PRIMARY KEY,
    uploader_id INTEGER NOT NULL REFERENCES users(id),
    title VARCHAR(255) NOT NULL,
    description TEXT,
    filename VARCHAR(255) NOT NULL,
    thumbnail_path VARCHAR(255),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- ゲームテーブル
CREATE TABLE IF NOT EXISTS games (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id),
    title VARCHAR(255) NOT NULL,
    description TEXT,
    status VARCHAR(20) NOT NULL DEFAULT 'processing',
    game_url VARCHAR(255),
    thumbnail_url VARCHAR(255),
    scale REAL DEFAULT 1.0,
    offset_x INTEGER DEFAULT 0,
    offset_y INTEGER DEFAULT 0,
    native_width INTEGER DEFAULT 1280, -- New: Store extracted native width
    native_height INTEGER DEFAULT 720, -- New: Store extracted native height
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- 初回起動時に管理者ユーザーを作成（存在しない場合のみ）
INSERT INTO users (username, email, password_hash, is_admin, provider)
SELECT 'admin', 'admin@internal.local', '$2a$10$f.wR1k.1u.V7eS7iAnYJgeG0s2g6G5A.d.s8m/2.4.4a3b2c1d0e1', TRUE, 'local'
WHERE NOT EXISTS (SELECT 1 FROM users WHERE email = 'admin@internal.local');
