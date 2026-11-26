// Package queue は Redis のリストを使ったジョブキューと、スキャン完了イベントの配信、
// 処理中リストを使った確実な取り出し（ack 方式）を提供する。
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

// パッケージ全体で共有するグローバル状態。ConnectRedis で初期化される。
var (
	// RedisClient は Redis クライアントのインスタンスを保持する。
	RedisClient *redis.Client
	// ScanQueueName はスキャン待ちジョブを積む Redis リストの名前。
	ScanQueueName = "sfsp:scan_queue"
	// Logger はパッケージ内で使う zap の SugaredLogger を保持する。
	Logger *zap.SugaredLogger
)

// スキャン完了イベントの配信先キュー名。TargetService ごとに別のリストへ振り分ける。
const (
	StaticSiteCompletionQueue = "sfsp:completed:static-site"
	GameCompletionQueue       = "sfsp:completed:game"
	StreamCompletionQueue     = "sfsp:completed:stream"
	ProfileCompletionQueue    = "sfsp:completed:profile"
	VideoThumbnailCompletionQueue = "sfsp:completed:video-thumbnail"
	GameThumbnailCompletionQueue  = "sfsp:completed:game-thumbnail"
	SiteThumbnailCompletionQueue = "sfsp:completed:site-thumbnail"
)

// ConnectRedis は Redis クライアントを初期化し、Ping が通るまでリトライしてから
// パッケージ変数 RedisClient と Logger に設定する。
// RedisAddr が redis:// または rediss:// で始まる場合は URL として解析し、
// その場合 RedisPassword と RedisDB は使わず URL 内の値が使われる。
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
	// （最大5回、待ち時間は2秒から倍々に増える。各 Ping は5秒でタイムアウト）
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

// EnqueueJob はジョブ ID をスキャンキューの先頭に LPUSH で追加する。
func EnqueueJob(ctx context.Context, jobID string) error {
	err := RedisClient.LPush(ctx, ScanQueueName, jobID).Err()
	if err != nil {
		Logger.Errorf("Failed to enqueue job %s to %s: %v", jobID, ScanQueueName, err)
		return fmt.Errorf("failed to enqueue job %s to %s: %w", jobID, ScanQueueName, err)
	}
	Logger.Infof("Enqueued job %s to %s", jobID, ScanQueueName)
	return nil
}

// EnqueueScanCompletionEvent はスキャン完了イベントを TargetService に対応するサービス別キューへ
// JSON として LPUSH する。TargetService が空または未知の値の場合はイベントを破棄してエラーを返す。
func EnqueueScanCompletionEvent(ctx context.Context, e event.ScanCompletionEvent) error { // event.ScanCompletionEvent を使用
	// 1. 初期化済みかを確認する
	if RedisClient == nil {
		return fmt.Errorf("Redis client is not initialized")
	}
	if Logger == nil {
		return fmt.Errorf("Logger is not initialized")
	}

	// 2. 配信先が指定されていないイベントは破棄する
	if e.TargetService == "" {
		Logger.Errorw("Event discarded: target_service is not set",
			"job_id", e.JobID,
			"file_id", e.FileID,
			"final_status", e.FinalStatus,
			"filename", e.Filename,
		)
		return fmt.Errorf("target_service is not set for job %s", e.JobID)
	}

	// 3. TargetService から配信先キュー名を決める
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

	// 4. JSON に変換してキューへ積む
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

// ProcessingQueueName は queueName から取り出されたがまだ ack されていないメッセージを
// 保持する処理中リストの名前を返す。
func ProcessingQueueName(queueName string) string {
	return queueName + ":processing"
}

// DequeueReliable は queueName にメッセージが届くまでブロックし、届いたメッセージを
// 処理中リストへアトミックに移動して返す（BRPOPLPUSH）。
// メッセージの処理後（成功時、または DB に最終的な失敗を記録した後）に Ack を呼ぶこと。
// ack されなかったメッセージは RecoverProcessing によって queueName に戻される。
// timeout に 0 を指定すると無期限にブロックする。
// LPUSH で積み RPOP 側から取り出すため、先に積まれたものから処理される（FIFO）。
func DequeueReliable(ctx context.Context, queueName string, timeout time.Duration) (string, error) {
	return RedisClient.BRPopLPush(ctx, queueName, ProcessingQueueName(queueName), timeout).Result()
}

// Ack は DequeueReliable が返したメッセージを処理中リストから1件削除する。
func Ack(ctx context.Context, queueName, payload string) error {
	return RedisClient.LRem(ctx, ProcessingQueueName(queueName), 1, payload).Err()
}

// RecoverProcessing は処理中リストに残っている未 ack のメッセージをすべて queueName に戻し、
// 戻した件数を返す。コンシューマのループを開始する前、起動時に一度だけ呼ぶこと。
// キューごとにコンシューマが1つだけであること（現在の構成）を前提としている。
// 複数コンシューマがいると、他のコンシューマが処理中のメッセージまで戻してしまうため。
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
