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

	// 認証キーが無ければ起動しない（fail closed）
	if cfg.API.Key == "" {
		log.Fatal("SFSP_API_KEY is not set: sfsp-api refuses to start without an API key. " +
			"Set SFSP_API_KEY (a long random value) for sfsp-api and send the same value in the X-SFSP-API-Key header from every client.")
	}
	if len(cfg.API.Key) < 32 {
		sugar.Warn("SFSP_API_KEY is shorter than 32 characters; use a long random value")
	}

	// Shared用Config（Redis接続設定などを含む）
	sharedCfg, err := sharedconfig.LoadConfig()
	if err != nil {
		sugar.Fatalf("Failed to load shared configuration: %v", err)
	}
	// REDIS_URL（redis://:<pw>@redis:6379）があればそれを使い、URL にパスワードが無ければ REDIS_PASSWORD を補う
	sharedCfg.RedisAddr = config.ResolveRedisAddr(sharedCfg.RedisAddr, sharedCfg.RedisPassword)

	if err := database.Connect(cfg); err != nil {
		sugar.Fatalf("Failed to connect to database: %s", config.RedactString(err.Error(), cfg.DatabaseURL))
	}
	defer database.Close()

	// MinIOへの接続 (バケット作成は docker-compose の minio-init が担当するため接続のみ行う)
	if err := storage.ConnectMinIO(cfg); err != nil {
		sugar.Fatalf("Failed to connect to MinIO: %v", err)
	}

	// shared/queue に sharedCfg を渡して Redis 接続を初期化
	if err := queue.ConnectRedis(sharedCfg, sugar); err != nil {
		sugar.Fatalf("Failed to connect to Redis (%s): %s", config.RedactURL(sharedCfg.RedisAddr), config.RedactString(err.Error(), sharedCfg.RedisAddr))
	}

	router := gin.Default()
	// アップロードは MultipartReader でストリーミングするため、フォーム全体をメモリに展開しない
	router.MaxMultipartMemory = 1 << 20

	// APIハンドラーの初期化
	apiHandler := api.NewAPI(cfg, sugar)

	// /health は認証なし（compose の healthcheck 等のため）
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	v1 := router.Group("/api/v1")
	v1.Use(api.RequireAPIKey(cfg.API.Key))
	{
		v1.POST("/files", apiHandler.HandleFileUpload)
		v1.GET("/jobs/:id", apiHandler.HandleGetJob)
		v1.GET("/results/:id", apiHandler.HandleGetResult)
	}

	port := cfg.API.Port
	if port == "" {
		port = "8080"
	}

	sugar.Infof("Starting SFSP API server on port %s (max upload %d bytes)", port, cfg.Limits.MaxUploadBytes)
	if err := router.Run(fmt.Sprintf(":%s", port)); err != nil {
		sugar.Fatalf("Failed to run server: %v", err)
	}
}
