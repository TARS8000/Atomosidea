-- 既存データベースへのマイグレーション
-- static_sites テーブルに team_id カラムを追加する
-- 新規データベースはinit.sqlで既に定義済みなので、既存DBのみ実行する
-- 2026-09-23: videos/games は既に team_id を持つが, static_sites のみが未追加のため

ALTER TABLE public.static_sites
    ADD COLUMN IF NOT EXISTS team_id VARCHAR(24);