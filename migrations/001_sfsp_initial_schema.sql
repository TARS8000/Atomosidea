-- =========================================================================
-- SFSP Database (sfsp-db) Schema Definition
-- =========================================================================

-- UUID生成関数の利用準備 (PostgreSQL 13以降は gen_random_uuid() も使用可能)
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- -------------------------------------------------------------------------
-- 1. アップロードファイルメタデータテーブル
-- -------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS files (
                                     id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    filename VARCHAR(255) NOT NULL,
    filesize BIGINT NOT NULL,
    mime_type VARCHAR(100) NOT NULL,
    sha256 VARCHAR(64) NOT NULL, -- ※ 同一ファイルの重複アップロードを許可するため UNIQUE を削除（必要に応じて戻してください）
    storage_path VARCHAR(512) NOT NULL,
    file_type VARCHAR(50) NOT NULL, -- 例: "video", "html", "zip", "other"
    target_service VARCHAR(50) NOT NULL DEFAULT 'other', -- アプリ側連携サービス識別 (例: "video", "game", "static_site")
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
    );

-- -------------------------------------------------------------------------
-- 2. スキャンジョブテーブル
-- -------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS scan_jobs (
                                         id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    file_id UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL DEFAULT 'queued', -- 例: queued, running, clean, suspicious, malicious, failed, invalid
    processing_details TEXT, -- 処理ステップやエラー詳細情報
    is_cleaned_up BOOLEAN NOT NULL DEFAULT FALSE, -- ★ workerのクリーンアップタスク用フラグ
    cleaned_up_at TIMESTAMPTZ,                  -- ★ クリーンアップ実行日時
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
    );

-- -------------------------------------------------------------------------
-- 3. スキャン結果詳細テーブル
-- -------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS scan_results (
                                            id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    job_id UUID NOT NULL REFERENCES scan_jobs(id) ON DELETE CASCADE,
    scanner VARCHAR(100) NOT NULL, -- 例: 'clamav', 'yara'
    result VARCHAR(50) NOT NULL,  -- 例: 'clean', 'suspicious', 'malicious', 'error'
    details TEXT,
    raw_output JSONB,
    scanned_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
    );

-- -------------------------------------------------------------------------
-- 4. インデックス作成
-- -------------------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_files_sha256 ON files(sha256);
CREATE INDEX IF NOT EXISTS idx_scan_jobs_file_id ON scan_jobs(file_id);
CREATE INDEX IF NOT EXISTS idx_scan_results_job_id ON scan_results(job_id);

-- ★ クリーンアップ対象（status = 'clean' AND is_cleaned_up = FALSE）の検索を高速化する複合インデックス
CREATE INDEX IF NOT EXISTS idx_scan_jobs_cleanup ON scan_jobs(status, is_cleaned_up);

-- -------------------------------------------------------------------------
-- 5. updated_at 自動更新トリガーの設定
-- -------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION trigger_set_timestamp()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = CURRENT_TIMESTAMP;
RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS set_timestamp ON scan_jobs;
CREATE TRIGGER set_timestamp
    BEFORE UPDATE ON scan_jobs
    FOR EACH ROW
    EXECUTE FUNCTION trigger_set_timestamp();