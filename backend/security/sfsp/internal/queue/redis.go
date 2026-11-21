package queue

import (
	"context"
	"time"

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
