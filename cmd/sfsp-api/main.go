package main

import (
    "fmt"
    "log"

    "github.com/atmosidea/sfsp/internal/api"
    "github.com/atmosidea/sfsp/internal/database"
    "github.com/atmosidea/sfsp/internal/storage"
    "github.com/atmosidea/shared/config" // 修正済み
    "github.com/atmosidea/shared/queue"  // 修正済み
    "github.com/gin-gonic/gin"
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

    // 修正済み（sugarを渡す）
    if err := queue.ConnectRedis(cfg, sugar); err != nil {
       sugar.Fatalf("Failed to connect to Redis: %v", err)
    }

    router := gin.Default()
    apiHandler := api.NewAPI(cfg, sugar)

    router.GET("/health", func(c *gin.Context) {
       c.JSON(200, gin.H{"status": "ok"})
    })

    v1 := router.Group("/api/v1")
    {
       v1.POST("/files", apiHandler.HandleFileUpload)
       v1.GET("/jobs/:id", apiHandler.HandleGetJob)
       v1.GET("/results/:id", apiHandler.HandleGetResult)
    }

    port := cfg.API.Port
    if port == "" {
       port = "8080"
    }

    sugar.Infof("Starting SFSP API server on port %s", port)
    if err := router.Run(fmt.Sprintf(":%s", port)); err != nil {
       sugar.Fatalf("Failed to run server: %v", err)
    }
}