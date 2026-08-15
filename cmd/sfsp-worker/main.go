package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/atmosidea/shared/config" // 修正済み
    "github.com/atmosidea/shared/queue"  // 修正済み
    "github.com/atmosidea/sfsp/internal/database"
    "github.com/atmosidea/sfsp/internal/storage"
    "github.com/atmosidea/sfsp/internal/worker"
    "go.uber.org/zap"
)

func main() {
    logger, err := zap.NewProduction()
    if err != nil {
       log.Fatalf("can't initialize zap logger: %v", err)
    }
    defer logger.Sync()
    sugar := logger.Sugar()

    cfg, err := config.LoadConfig()
    if err != nil {
       sugar.Fatalf("Failed to load configuration: %v", err)
    }

    if err := database.Connect(cfg); err != nil {
       sugar.Fatalf("Failed to connect to database: %v", err)
    }
    defer database.Close()

    if err := storage.ConnectMinIO(cfg); err != nil {
       sugar.Fatalf("Failed to connect to MinIO: %v", err)
    }

    if err := storage.EnsureBuckets(context.Background(), cfg); err != nil {
       sugar.Fatalf("Failed to ensure MinIO buckets: %v", err)
    }

    // 修正済み（sugarを渡す）
    if err := queue.ConnectRedis(cfg, sugar); err != nil {
       sugar.Fatalf("Failed to connect to Redis: %v", err)
    }

    sugar.Info("SFSP Worker started. Waiting for jobs...")
    ctx, cancel := context.WithCancel(context.Background())

    go worker.StartWorker(ctx, cfg, sugar)

    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    <-quit
    sugar.Info("Shutting down SFSP Worker...")

    cancel()
    time.Sleep(2 * time.Second)
    sugar.Info("SFSP Worker stopped.")
}