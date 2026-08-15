package queue

import (
	"context"
	"fmt"
	"strings" // ✨ 追加
	"time"

	"github.com/atmosidea/sfsp/internal/config" // 型名はファイル内の定義に合わせてください
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

var (
	// RedisClient holds the Redis client instance
	RedisClient *redis.Client
	// ScanQueueName is the name of the Redis list used as a queue for pending scans
	ScanQueueName = "sfsp:scan_queue"
	// Logger holds the sugared logger instance
	Logger *zap.SugaredLogger
)

// ConnectRedis initializes the Redis client
func ConnectRedis(cfg config.Config, l *zap.SugaredLogger) error { // 型名はファイル内の定義に合わせてください
	Logger = l
	var opt *redis.Options
	var err error

	// redis:// または rediss:// で始まる場合のみ ParseURL を使用
	if strings.HasPrefix(cfg.RedisAddr, "redis://") || strings.HasPrefix(cfg.RedisAddr, "rediss://") {
		opt, err = redis.ParseURL(cfg.RedisAddr)
		if err != nil {
			return fmt.Errorf("failed to parse Redis URL: %w", err)
		}
	} else {
		opt = &redis.Options{
			Addr:     cfg.RedisAddr,
			Password: cfg.RedisPassword,
			DB:       cfg.RedisDB,
		}
	}

	// config.Retry を使用して接続を試行
	err = config.Retry(5, 2*time.Second, func() error {
		RedisClient = redis.NewClient(opt)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := RedisClient.Ping(ctx).Result()
		return err
	})

	if err != nil {
		return fmt.Errorf("unable to connect to Redis after retries: %w", err)
	}

	Logger.Info("Successfully connected to Redis.")
	return nil
}

// EnqueueJob adds a job ID to the scan queue
func EnqueueJob(ctx context.Context, jobID string) error {
	err := RedisClient.LPush(ctx, ScanQueueName, jobID).Err()
	if err != nil {
		Logger.Errorf("Failed to enqueue job %s to %s: %v", jobID, ScanQueueName, err)
		return fmt.Errorf("failed to enqueue job %s to %s: %w", jobID, ScanQueueName, err)
	}
	Logger.Infof("Enqueued job %s to %s", jobID, ScanQueueName)
	return nil
}

// DequeueJob removes and returns a job ID from the scan queue (blocking)
func DequeueJob(ctx context.Context) (string, error) {
	result, err := RedisClient.BRPop(ctx, 0, ScanQueueName).Result()
	if err != nil {
		if err != context.Canceled {
			Logger.Errorf("Failed to dequeue job from %s: %v", ScanQueueName, err)
		}
		return "", err
	}
	if len(result) < 2 {
		Logger.Errorf("Invalid result from BRPop on %s: %v", ScanQueueName, result)
		return "", fmt.Errorf("invalid result from BRPop: %v", result)
	}
	return result[1], nil
}