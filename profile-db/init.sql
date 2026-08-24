CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    username VARCHAR(255),
    bio TEXT,
    icon_url TEXT,
    background_image_url TEXT,
    status VARCHAR(50) DEFAULT 'offline',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- テスト用ユーザーデータ (UUID v7 形式のサンプル)
INSERT INTO users (id, username, bio, status) 
VALUES ('01913197-0000-7000-8000-000000000001', 'user_atomos', 'Hello Atomosidea with UUIDv7!', 'online') 
ON CONFLICT (id) DO NOTHING;
