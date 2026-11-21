package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings" // ✨ 追加
	"time"

	"github.com/atmosidea/shared/config" // プロジェクトのインポートパスに合わせる
	"github.com/atmosidea/shared/event"
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

// Queue names for scan completion events
const (
	StaticSiteCompletionQueue = "sfsp:completed:static-site"
	GameCompletionQueue       = "sfsp:completed:game"
	StreamCompletionQueue     = "sfsp:completed:stream"
	ProfileCompletionQueue    = "sfsp:completed:profile"
	VideoThumbnailCompletionQueue = "sfsp:completed:video-thumbnail"
	GameThumbnailCompletionQueue  = "sfsp:completed:game-thumbnail"
	SiteThumbnailCompletionQueue = "sfsp:completed:site-thumbnail"
)

// ConnectRedis initializes the Redis client
func ConnectRedis(cfg config.Config, l *zap.SugaredLogger) error {
	Logger = l
	var opt *redis.Options
	var err error

	// redis:// や rediss:// で始まる場合のみ ParseURL を使用
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

// EnqueueScanCompletionEvent enqueues a scan completion event to the appropriate service-specific queue
func EnqueueScanCompletionEvent(ctx context.Context, e event.ScanCompletionEvent) error { // event.ScanCompletionEvent を使用
	if RedisClient == nil {
		return fmt.Errorf("Redis client is not initialized")
	}
	if Logger == nil {
		return fmt.Errorf("Logger is not initialized")
	}

	if e.TargetService == "" {
		Logger.Errorw("Event discarded: target_service is not set",
			"job_id", e.JobID,
			"file_id", e.FileID,
			"final_status", e.FinalStatus,
			"filename", e.Filename,
		)
		return fmt.Errorf("target_service is not set for job %s", e.JobID)
	}

	var queueName string
	switch e.TargetService {
	case "static-site":
		queueName = StaticSiteCompletionQueue
	case "game":
		queueName = GameCompletionQueue
	case "stream":
		queueName = StreamCompletionQueue
	case "profile":
		queueName = ProfileCompletionQueue
	case "video-thumbnail":
		queueName = VideoThumbnailCompletionQueue
	case "game-thumbnail":
		queueName = GameThumbnailCompletionQueue
	case "site-thumbnail":
		queueName = SiteThumbnailCompletionQueue
	default:
		Logger.Warnw("Event discarded: unknown target_service",
			"job_id", e.JobID,
			"file_id", e.FileID,
			"target_service", e.TargetService,
			"final_status", e.FinalStatus,
			"filename", e.Filename,
		)
		return fmt.Errorf("unknown target_service %s for job %s", e.TargetService, e.JobID)
	}

	eventBytes, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("failed to marshal scan completion event for job %s: %w", e.JobID, err)
	}

	err = RedisClient.LPush(ctx, queueName, eventBytes).Err()
	if err != nil {
		return fmt.Errorf("failed to enqueue scan completion event for job %s to %s: %w", e.JobID, queueName, err)
	}

	Logger.Infow("Published Event",
		"JobID", e.JobID,
		"TargetService", e.TargetService,
		"Queue", queueName,
	)
	return nil
}

// ProcessingQueueName returns the name of the list that holds messages taken from
// queueName but not yet acknowledged.
func ProcessingQueueName(queueName string) string {
	return queueName + ":processing"
}

// DequeueReliable blocks until a message is available on queueName and atomically
// moves it to the processing list (BRPOPLPUSH). Call Ack after the message has been
// handled (successfully or with a terminal failure recorded in the DB). Messages that
// were never acknowledged are returned to queueName by RecoverProcessing.
// A timeout of 0 blocks indefinitely.
func DequeueReliable(ctx context.Context, queueName string, timeout time.Duration) (string, error) {
	return RedisClient.BRPopLPush(ctx, queueName, ProcessingQueueName(queueName), timeout).Result()
}

// Ack removes a message previously returned by DequeueReliable from the processing list.
func Ack(ctx context.Context, queueName, payload string) error {
	return RedisClient.LRem(ctx, ProcessingQueueName(queueName), 1, payload).Err()
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
