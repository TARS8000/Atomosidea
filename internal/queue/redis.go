package queue

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap" // zap をインポート
)

var (
	// RedisClient holds the Redis client instance
	RedisClient *redis.Client
	// ScanQueueName is the name of the Redis list used as a queue for pending scans
	ScanQueueName = "sfsp:scan_queue"
	// Logger holds the sugared logger instance
	Logger *zap.SugaredLogger // Logger を追加
)

// ConnectRedis initializes the Redis client
func ConnectRedis(cfg config.Config, l *zap.SugaredLogger) error { // logger を引数に追加
	Logger = l // グローバル Logger に設定
	var err error
	opt, err := redis.ParseURL(cfg.RedisAddr)
	if err != nil {
		// Fallback for simple "host:port" address
		opt = &redis.Options{
			Addr: cfg.RedisAddr,
		}
	}

	err = config.Retry(5, 2*time.Second, func() error {
		RedisClient = redis.NewClient(opt)
		_, err := RedisClient.Ping(context.Background()).Result()
		return err
	})

	if err != nil {
		return fmt.Errorf("unable to connect to Redis after retries: %w", err)
	}

	Logger.Info("Successfully connected to Redis.") // Logger を使用
	return nil
}

// EnqueueJob adds a job ID to the scan queue
func EnqueueJob(ctx context.Context, jobID string) error {
	err := RedisClient.LPush(ctx, ScanQueueName, jobID).Err()
	if err != nil {
		Logger.Errorf("Failed to enqueue job %s to %s: %v", jobID, ScanQueueName, err) // Logger を使用
		return fmt.Errorf("failed to enqueue job %s to %s: %w", jobID, ScanQueueName, err)
	}
	Logger.Infof("Enqueued job %s to %s", jobID, ScanQueueName) // Logger を使用
	return nil
}

// DequeueJob removes and returns a job ID from the scan queue (blocking)
func DequeueJob(ctx context.Context) (string, error) {
	// BRPOP blocks until a job is available or a timeout occurs
	result, err := RedisClient.BRPop(ctx, 0, ScanQueueName).Result()
	if err != nil {
		if err != context.Canceled { // context.Canceled はエラーとしてログしない
			Logger.Errorf("Failed to dequeue job from %s: %v", ScanQueueName, err) // Logger を使用
		}
		return "", err
	}
	if len(result) < 2 {
		Logger.Errorf("Invalid result from BRPop on %s: %v", ScanQueueName, result) // Logger を使用
		return "", fmt.Errorf("invalid result from BRPop: %v", result)
	}
	return result[1], nil
}