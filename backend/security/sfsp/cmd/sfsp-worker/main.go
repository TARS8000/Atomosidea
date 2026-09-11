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
          sugar.Fatalf("Failed to connect to app-db (%s): %v", appDBURL, err)
       }
       defer appDB.Close()
       sugar.Info("Successfully connected to app-db for status watching.")
    }

    // 3. MinIO 接続 (RAW / CLEAN 両エンドポイントの設定を内部で保持)
    if err := storage.ConnectMinIO(cfg); err != nil {
       sugar.Fatalf("Failed to connect to MinIO: %v", err)
    }

    // バケット初期化は minio-init サービスが事前実行するためスキップ
    // if err := storage.EnsureBuckets(ctx, cfg); err != nil {
    //    sugar.Fatalf("Failed to ensure MinIO buckets: %v", err)
    // }

    // 4. Redis 接続の初期化
    if err := queue.ConnectRedis(sharedCfg, sugar); err != nil {
       sugar.Fatalf("Failed to connect to Redis: %v", err)
    }

    sugar.Info("SFSP Worker started. Waiting for jobs...")

    // 5. ワーカープロセスの起動
    go worker.StartWorker(ctx, cfg, appDB, sugar)

    // Graceful Shutdown
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    <-quit
    sugar.Info("Shutting down SFSP Worker...")

    cancel()
    time.Sleep(2 * time.Second)
    sugar.Info("SFSP Worker stopped.")
}