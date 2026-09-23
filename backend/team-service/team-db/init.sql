-- team-db schema for team-service
-- Teams, members, and content links for closed/token-based content sharing.

DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'team_service_user') THEN
    CREATE ROLE team_service_user WITH LOGIN PASSWORD 'team_service_strong_password';
  END IF;
END
$$;

GRANT CONNECT ON DATABASE team_db TO team_service_user;
GRANT USAGE, CREATE ON SCHEMA public TO team_service_user;

-- Teams (closed content collections, Discord-invite-link style)
CREATE TABLE IF NOT EXISTS teams (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token VARCHAR(24) NOT NULL,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    is_public BOOLEAN NOT NULL DEFAULT FALSE,
    created_by UUID,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
    UNIQUE (token)
);

-- Team membership (role: owner | admin | member)
CREATE TABLE IF NOT EXISTS team_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id UUID NOT NULL,
    role VARCHAR(20) NOT NULL DEFAULT 'member',
    joined_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
    UNIQUE (team_id, user_id)
);

-- Content links (Phase 2)
CREATE TABLE IF NOT EXISTS team_content (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    content_type VARCHAR(20) NOT NULL,
    content_id VARCHAR(20) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
    UNIQUE (team_id, content_type, content_id)
);

-- Team-native content posts (Phase 1)
CREATE TABLE IF NOT EXISTS team_posts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    author_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
);

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE teams, team_members, team_content, team_posts TO team_service_user;