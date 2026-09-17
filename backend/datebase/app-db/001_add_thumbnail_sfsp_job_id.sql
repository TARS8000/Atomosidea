-- 既存データベースへのマイグレーション
-- thumbnail_sfsp_job_id カラムとインデックスを追加する
-- 新規データベースはinit.sqlで既に定義済みなので、既存DBのみ実行する

ALTER TABLE public.games
    ADD COLUMN IF NOT EXISTS thumbnail_sfsp_job_id UUID;

CREATE INDEX IF NOT EXISTS idx_games_thumbnail_sfsp_job_id
    ON public.games(thumbnail_sfsp_job_id);