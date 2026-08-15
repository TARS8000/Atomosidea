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
	"github.com/jackc/pgx/v5/pgxpool" // ★ 追加
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer logger.Sync()
	sugar := logger.Sugar()

	// 内部用Configの読み込み
	cfg, err := config.LoadConfig()
	if err != nil {
		sugar.Fatalf("Failed to load configuration: %v", err)
	}

	// 外部(Shared)用Configの読み込み
	sharedCfg, err := sharedconfig.LoadConfig()
	if err != nil {
		sugar.Fatalf("Failed to load shared configuration: %v", err)
	}

	// 1. sfsp-db (SFSP管理用DB) へ接続
	if err := database.Connect(cfg); err != nil {
		sugar.Fatalf("Failed to connect to sfsp database: %v", err)
	}
	defer database.Close()

	// 2. app-db (アプリ本体DB) への Read-Only 接続プールを作成 ★追加
	appDBURL := os.Getenv("APP_DATABASE_URL")
	if appDBURL == "" {
		sugar.Warn("APP_DATABASE_URL is not set. Cleanup watcher will be disabled.")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var appDB *pgxpool.Pool
	if appDBURL != "" {
		var err error
		appDB, err = pgxpool.New(ctx, appDBURL)
		if err != nil {
			sugar.Fatalf("Failed to connect to app-db (%s): %v", appDBURL, err)
		}
		defer appDB.Close()
		sugar.Info("Successfully connected to app-db for status watching.")
	}

	if err := storage.ConnectMinIO(cfg); err != nil {
		sugar.Fatalf("Failed to connect to MinIO: %v", err)
	}

	if err := storage.EnsureBuckets(context.Background(), cfg); err != nil {
		sugar.Fatalf("Failed to ensure MinIO buckets: %v", err)
	}

	// shared/queue の Redis 接続を初期化
	if err := queue.ConnectRedis(sharedCfg, sugar); err != nil {
		sugar.Fatalf("Failed to connect to Redis: %v", err)
	}

	sugar.Info("SFSP Worker started. Waiting for jobs...")

	// 3. appDB (*pgxpool.Pool) を第3引数に渡す ★修正
	go worker.StartWorker(ctx, cfg, appDB, sugar)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	sugar.Info("Shutting down SFSP Worker...")

	cancel()
	time.Sleep(2 * time.Second)
	sugar.Info("SFSP Worker stopped.")
}