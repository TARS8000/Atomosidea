-- =========================================================================
-- 1. テーブル定義
-- =========================================================================

-- 動画テーブル
CREATE TABLE IF NOT EXISTS public.videos (
                                             id VARCHAR(10) PRIMARY KEY,
    uploader_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    filename VARCHAR(255) NOT NULL,
    thumbnail_path VARCHAR(255),
    status VARCHAR(50) NOT NULL DEFAULT 'processing',
    sfsp_job_id UUID,
    processing_details TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
                             );

-- ゲームテーブル
CREATE TABLE IF NOT EXISTS public.games (
                                            id VARCHAR(10) PRIMARY KEY,
    user_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    status VARCHAR(50) NOT NULL DEFAULT 'processing',
    sfsp_job_id UUID,
    processing_details TEXT,
    game_url VARCHAR(255),
    thumbnail_url VARCHAR(255),
    scale REAL DEFAULT 1.0 NOT NULL,
    offset_x INTEGER DEFAULT 0 NOT NULL,
    offset_y INTEGER DEFAULT 0 NOT NULL,
    native_width INTEGER DEFAULT 1280 NOT NULL,
    native_height INTEGER DEFAULT 720 NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
                             );

-- 静的サイトテーブル
CREATE TABLE IF NOT EXISTS public.static_sites (
                                                   id VARCHAR(10) PRIMARY KEY,
    user_id UUID NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    status VARCHAR(50) NOT NULL DEFAULT 'processing',
    sfsp_job_id UUID,
    processing_details TEXT,
    minio_path VARCHAR(255) NOT NULL,
    entry_point_path VARCHAR(255) DEFAULT 'index.html' NOT NULL,
    thumbnail_url VARCHAR(255),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP NOT NULL
                             );

-- =========================================================================
-- 2. インデックス作成
-- =========================================================================

-- ジョブ検索・参照用インデックス
CREATE INDEX IF NOT EXISTS idx_videos_sfsp_job_id ON public.videos(sfsp_job_id);
CREATE INDEX IF NOT EXISTS idx_games_sfsp_job_id ON public.games(sfsp_job_id);
CREATE INDEX IF NOT EXISTS idx_static_sites_sfsp_job_id ON public.static_sites(sfsp_job_id);

-- ユーザー（投稿者）別一覧表示用インデックス
CREATE INDEX IF NOT EXISTS idx_videos_uploader_id ON public.videos(uploader_id);
CREATE INDEX IF NOT EXISTS idx_games_user_id ON public.games(user_id);
CREATE INDEX IF NOT EXISTS idx_static_sites_user_id ON public.static_sites(user_id);

-- =========================================================================
-- 3. 自動更新トリガーの設定 (updated_at)
-- =========================================================================

-- タイムスタンプ更新用関数の定義
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 各テーブルに updated_at 自動更新トリガーを適用
DROP TRIGGER IF EXISTS trg_videos_updated_at ON public.videos;
CREATE TRIGGER trg_videos_updated_at
    BEFORE UPDATE ON public.videos
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS trg_games_updated_at ON public.games;
CREATE TRIGGER trg_games_updated_at
    BEFORE UPDATE ON public.games
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

DROP TRIGGER IF EXISTS trg_static_sites_updated_at ON public.static_sites;
CREATE TRIGGER trg_static_sites_updated_at
    BEFORE UPDATE ON public.static_sites
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- =========================================================================
-- 4. sfsp_worker ロール作成 ＆ 読み取り専用（SELECT）権限の付与
-- =========================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'sfsp_worker') THEN
        -- ※パスワードは適宜本番環境の設定に変更してください
CREATE ROLE sfsp_worker WITH LOGIN PASSWORD 'sfsp_worker_password';
END IF;
END
$$;

-- アプリケーション側テーブルへの SELECT 権限を許可
GRANT SELECT ON TABLE public.videos, public.games, public.static_sites TO sfsp_worker;

-- 将来作成されるテーブルに対する標準権限の設定
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO sfsp_worker;