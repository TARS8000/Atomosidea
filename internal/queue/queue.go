package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/atmosidea/sfsp/internal/model"
	"github.com/go-redis/redis/v8" // go-redis/v8 を使用
)

// EnqueueScanCompletionEvent enqueues a scan completion event to the appropriate service-specific queue
func EnqueueScanCompletionEvent(ctx context.Context, event model.ScanCompletionEvent) error {
	if RedisClient == nil {
		return fmt.Errorf("Redis client is not initialized")
	}
	if Logger == nil {
		return fmt.Errorf("Logger is not initialized")
	}

	if event.TargetService == "" {
		Logger.Errorw("Event discarded: target_service is not set",
			"job_id", event.JobID,
			"file_id", event.FileID,
			"final_status", event.FinalStatus,
			"filename", event.Filename,
		)
		return fmt.Errorf("target_service is not set for job %s", event.JobID)
	}

	var queueName string
	switch event.TargetService {
	case "static-site":
		queueName = "sfsp:completed:static-site"
	case "game":
		queueName = "sfsp:completed:game"
	case "stream":
		queueName = "sfsp:completed:stream"
	default:
		Logger.Warnw("Event discarded: unknown target_service",
			"job_id", event.JobID,
			"file_id", event.FileID,
			"target_service", event.TargetService,
			"final_status", event.FinalStatus,
			"filename", event.Filename,
		)
		return fmt.Errorf("unknown target_service %s for job %s", event.TargetService, event.JobID)
	}

	eventBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal scan completion event for job %s: %w", event.JobID, err)
	}

	err = RedisClient.LPush(ctx, queueName, eventBytes).Err()
	if err != nil {
		return fmt.Errorf("failed to enqueue scan completion event for job %s to %s: %w", event.JobID, queueName, err)
	}

	Logger.Infow("Published Event",
		"JobID", event.JobID,
		"TargetService", event.TargetService,
		"Queue", queueName,
	)
	return nil
}