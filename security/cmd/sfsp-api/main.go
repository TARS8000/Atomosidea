package main

import (
	"fmt"
	"log"

	"github.com/atmosidea/sfsp/internal/api"
	"github.com/atmosidea/sfsp/internal/config"
	"github.com/atmosidea/sfsp/internal/database"
	"github.com/atmosidea/sfsp/internal/storage"
	sharedconfig "github.com/atmosidea/shared/config"
	"github.com/atmosidea/shared/queue"
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

	// SFSP内部用Config（MinIOバケット設定などを含む）
	cfg, err := config.LoadConfig()
	if err != nil {
		sugar.Fatalf("Failed to load configuration: %v", err)
	}

	// Shared用Config（Redis接続設定などを含む）
	sharedCfg, err := sharedconfig.LoadConfig()
	if err != nil {
		sugar.Fatalf("Failed to load shared configuration: %v", err)
	}

	if err := database.Connect(cfg); err != nil {
		sugar.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close()

	if err := storage.ConnectMinIO(cfg); err != nil {
		sugar.Fatalf("Failed to connect to MinIO: %v", err)
	}

	// shared/queue に sharedCfg を渡して Redis 接続を初期化
	if err := queue.ConnectRedis(sharedCfg, sugar); err != nil {
		sugar.Fatalf("Failed to connect to Redis: %v", err)
	}

	router := gin.Default()

	// APIハンドラーの初期化
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