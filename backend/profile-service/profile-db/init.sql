-- profile-db schema for profile-service
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    username VARCHAR(255),
    bio TEXT,
    icon_url TEXT,
    background_image_url TEXT,
    icon_sfsp_job_id UUID,
    background_sfsp_job_id UUID,
    status VARCHAR(50) DEFAULT 'offline',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
