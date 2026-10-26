package queue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
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

// SetClient lets the caller reuse an already connected client (e.g. the one created by
// github.com/atmosidea/shared/queue) instead of opening a second connection.
func SetClient(c *redis.Client, l *zap.SugaredLogger) {
	RedisClient = c
	Logger = l
}

// ConnectRedis initializes the Redis client
func ConnectRedis(cfg config.Config, l *zap.SugaredLogger) error {
	Logger = l
	var opt *redis.Options
	var err error

	// redis:// または rediss:// で始まる場合のみ ParseURL を使用
	if strings.HasPrefix(cfg.RedisAddr, "redis://") || strings.HasPrefix(cfg.RedisAddr, "rediss://") {
		opt, err = redis.ParseURL(cfg.RedisAddr)
		if err != nil {
			return fmt.Errorf("failed to parse Redis URL %s: %w", config.RedactURL(cfg.RedisAddr), err)
		}
		// URL にパスワードが含まれていなければ REDIS_PASSWORD を使う
		if opt.Password == "" {
			opt.Password = cfg.RedisPassword
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

// DequeueJob removes and returns a job ID from the scan queue (blocking).
// Deprecated: a crash after BRPOP loses the job. Use DequeueReliable + Ack.
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

// ---------------------------------------------------------------------------
// Reliable queue (mirrors backend/shared/queue)
// ---------------------------------------------------------------------------

// ProcessingQueueName returns the name of the list that holds messages taken from
// queueName but not yet acknowledged.
func ProcessingQueueName(queueName string) string {
	return queueName + ":processing"
}

// DeadLetterQueueName returns the name of the list that holds messages that failed
// too many times.
func DeadLetterQueueName(queueName string) string {
	return queueName + ":dead"
}

// DequeueReliable blocks until a message is available on queueName and atomically
// moves it to the processing list (BRPOPLPUSH). Call Ack after the message has been
// handled. Messages that were never acknowledged are returned to queueName by
// RecoverProcessing. A timeout of 0 blocks indefinitely; on timeout redis.Nil is returned.
func DequeueReliable(ctx context.Context, queueName string, timeout time.Duration) (string, error) {
	return RedisClient.BRPopLPush(ctx, queueName, ProcessingQueueName(queueName), timeout).Result()
}

// Ack removes a message previously returned by DequeueReliable from the processing list.
func Ack(ctx context.Context, queueName, payload string) error {
	return RedisClient.LRem(ctx, ProcessingQueueName(queueName), 1, payload).Err()
}

// Requeue atomically moves a message from the processing list back to the tail of
// queueName (it will be picked up after the messages already waiting).
func Requeue(ctx context.Context, queueName, payload string) error {
	_, err := RedisClient.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LRem(ctx, ProcessingQueueName(queueName), 1, payload)
		p.LPush(ctx, queueName, payload)
		return nil
	})
	return err
}

// DeadLetter atomically moves a message from the processing list to the dead-letter list.
func DeadLetter(ctx context.Context, queueName, payload string) error {
	_, err := RedisClient.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LRem(ctx, ProcessingQueueName(queueName), 1, payload)
		p.LPush(ctx, DeadLetterQueueName(queueName), payload)
		return nil
	})
	return err
}

// RecoverProcessing moves every unacknowledged message from the processing list back
// to queueName. Call it once at startup, before the consumer loop starts. It assumes a
// single consumer per queue (the current deployment).
func RecoverProcessing(ctx context.Context, queueName string) (int, error) {
	n := 0
	for {
		_, err := RedisClient.RPopLPush(ctx, ProcessingQueueName(queueName), queueName).Result()
		if err == redis.Nil {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		n++
	}
}
