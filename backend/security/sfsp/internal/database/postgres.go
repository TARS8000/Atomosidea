// Package database は sfsp-db（PostgreSQL）への接続プールの管理と、起動時のスキーマ補正を行う。
package database

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB はパッケージ全体で共有する DB 接続プールを保持する。
var DB *pgxpool.Pool

// Connect は DB 接続プールを初期化し、Ping が通るまでリトライする。
// 接続後、古いボリュームで作成された DB に不足しているカラムを追加する。
func Connect(cfg config.Config) error {
	var err error
	connStr := cfg.DatabaseURL

	// 1. 最大5回、2秒から倍々に待ちながら接続を試行する
	err = config.Retry(5, 2*time.Second, func() error {
		DB, err = pgxpool.New(context.Background(), connStr)
		if err != nil {
			return fmt.Errorf("unable to create connection pool: %w", err)
		}
		return DB.Ping(context.Background())
	})

	if err != nil {
		return fmt.Errorf("unable to connect to database after retries: %w", err)
	}

	// 2. 後から追加されたカラムが無ければ追加する
	if err := ensureTargetServiceColumn(); err != nil {
		return err
	}
	if err := ensureEventPublishedColumn(); err != nil {
		return err
	}

	log.Println("Successfully connected to the database.")
	return nil
}

// ensureTargetServiceColumn は files.target_service カラムの有無を information_schema で確認し、
// 無ければ NOT NULL DEFAULT 'other' で追加する（カラム導入前に作成された DB 向け）。
func ensureTargetServiceColumn() error {
	var exists bool
	if err := DB.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'files'
			  AND column_name = 'target_service'
		)
	`).Scan(&exists); err != nil {
		return fmt.Errorf("unable to check target_service column: %w", err)
	}

	if exists {
		return nil
	}

	if _, err := DB.Exec(context.Background(), `ALTER TABLE files ADD COLUMN target_service VARCHAR(50) NOT NULL DEFAULT 'other'`); err != nil {
		return fmt.Errorf("unable to add target_service column: %w", err)
	}

	log.Println("Added missing files.target_service column.")
	return nil
}

// Close は DB 接続プールが初期化済みであれば閉じる。
func Close() {
	if DB != nil {
		DB.Close()
	}
}

// ensureEventPublishedColumn は、カラム導入前に作成された DB に scan_jobs.event_published_at を追加する
// （docker-entrypoint-initdb.d は空のボリュームでしか実行されないため）。
// ワーカーはこのカラムを使ってジョブの再処理を冪等にする。最終ステータスに達して
// 完了イベントを発行済みのジョブは、再度イベントを発行しない。
func ensureEventPublishedColumn() error {
	if _, err := DB.Exec(context.Background(), `ALTER TABLE scan_jobs ADD COLUMN IF NOT EXISTS event_published_at TIMESTAMPTZ`); err != nil {
		return fmt.Errorf("unable to add event_published_at column: %w", err)
	}
	return nil
}
