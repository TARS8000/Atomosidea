CREATE TABLE IF NOT EXISTS users (
                                     id UUID PRIMARY KEY, -- SERIAL から UUID に変更 (Go側で UUID v7 を発行して INSERT)
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