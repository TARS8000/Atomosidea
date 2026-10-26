package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/atmosidea/sfsp/internal/database"
	"github.com/atmosidea/sfsp/internal/storage"
	"github.com/atmosidea/sfsp/internal/worker"
	sharedconfig "github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/queue"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer logger.Sync()
	sugar := logger.Sugar()

	// 内部用Configの読み込み（.env は不要。環境変数から読む）
	cfg, err := config.LoadConfig()
	if err != nil {
		sugar.Fatalf("Failed to load configuration: %v", err)
	}

	// 外部(Shared)用Configの読み込み（Redis 接続）
	sharedCfg, err := sharedconfig.LoadConfig()
	if err != nil {
		sugar.Fatalf("Failed to load shared configuration: %v", err)
	}
	// REDIS_URL（redis://:<pw>@redis:6379）があればそれを使い、URL にパスワードが無ければ REDIS_PASSWORD を補う
	sharedCfg.RedisAddr = config.ResolveRedisAddr(sharedCfg.RedisAddr, sharedCfg.RedisPassword)

	sugar.Infow("SFSP worker limits",
		"clamav_max_filesize", cfg.ClamAV.MaxFileSize,
		"clamav_max_scansize", cfg.ClamAV.MaxScanSize,
		"max_extracted_bytes", cfg.Limits.MaxExtractedBytes,
		"max_zip_entries", cfg.Limits.MaxZipEntries,
		"max_zip_entry_bytes", cfg.Limits.MaxZipEntryBytes,
		"max_zip_ratio", cfg.Limits.MaxZipRatio,
		"max_zip_depth", cfg.Limits.MaxZipDepth,
	)

	// 1. sfsp-db (SFSP管理用DB) へ接続
	if err := database.Connect(cfg); err != nil {
		sugar.Fatalf("Failed to connect to sfsp database: %s", config.RedactString(err.Error(), cfg.DatabaseURL))
	}
	defer database.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 2. app-db (アプリ本体DB) への Read-Only 接続プールを作成
	appDBURL := os.Getenv("APP_DATABASE_URL")
	var appDB *pgxpool.Pool
	if appDBURL == "" {
		sugar.Warn("APP_DATABASE_URL is not set. Cleanup watcher will be disabled.")
	} else {
		var err error
		appDB, err = pgxpool.New(ctx, appDBURL)
		if err != nil {
			// DSN にパスワードが含まれるため、ログには伏せた形だけを出す
			sugar.Fatalf("Failed to connect to app-db (%s): %s", config.RedactURL(appDBURL), config.RedactString(err.Error(), appDBURL))
		}
		defer appDB.Close()
		sugar.Info("Successfully connected to app-db for status watching.")
	}

	// 3. MinIO 接続 (RAW / CLEAN 両エンドポイントの設定を内部で保持)
	if err := storage.ConnectMinIO(cfg); err != nil {
		sugar.Fatalf("Failed to connect to MinIO: %v", err)
	}

	// 4. Redis 接続の初期化
	if err := queue.ConnectRedis(sharedCfg, sugar); err != nil {
		sugar.Fatalf("Failed to connect to Redis (%s): %s", config.RedactURL(sharedCfg.RedisAddr), config.RedactString(err.Error(), sharedCfg.RedisAddr))
	}

	sugar.Info("SFSP Worker started. Waiting for jobs...")

	// 5. ワーカープロセスの起動
	done := make(chan struct{})
	go func() {
		worker.StartWorker(ctx, cfg, appDB, sugar)
		close(done)
	}()

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	unexpected := false
	select {
	case <-quit:
	case <-done:
		unexpected = true
		sugar.Error("Worker loop exited unexpectedly")
	}
	sugar.Info("Shutting down SFSP Worker...")

	cancel()
	// 処理中のジョブは Ack していないので sfsp:scan_queue:processing に残り、次回起動時に回収される
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	sugar.Info("SFSP Worker stopped.")
	if unexpected {
		logger.Sync()
		os.Exit(1)
	}
}
