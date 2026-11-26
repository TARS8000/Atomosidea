// Package queue は SFSP ワーカー用の信頼性キュー操作（処理中リスト・ack・再投入・デッドレター）を提供する。
package queue

import (
	"context"
	"time"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

// パッケージ全体で共有するグローバル状態。SetClient で設定される。
var (
	// RedisClient は Redis クライアントのインスタンスを保持する。
	RedisClient *redis.Client
	// ScanQueueName はスキャン待ちジョブを積む Redis リストの名前。
	ScanQueueName = "sfsp:scan_queue"
	// Logger はパッケージ内で使う zap の SugaredLogger を保持する。
	Logger *zap.SugaredLogger
)

// SetClient は接続済みのクライアント（例: github.com/atmosidea/shared/queue が作成したもの）を
// 再利用できるように設定する。2本目の接続を開かずに済ませるため。
func SetClient(c *redis.Client, l *zap.SugaredLogger) {
	RedisClient = c
	Logger = l
}

// ---------------------------------------------------------------------------
// 信頼性キュー（backend/shared/queue と同じ実装）
// ---------------------------------------------------------------------------

// ProcessingQueueName は queueName から取り出されたがまだ ack されていないメッセージを
// 保持する処理中リストの名前を返す。
func ProcessingQueueName(queueName string) string {
	return queueName + ":processing"
}

// DeadLetterQueueName は失敗回数が多すぎたメッセージを保持するデッドレターリストの名前を返す。
func DeadLetterQueueName(queueName string) string {
	return queueName + ":dead"
}

// DequeueReliable は queueName にメッセージが届くまでブロックし、届いたメッセージを
// 処理中リストへアトミックに移動して返す（BRPOPLPUSH）。メッセージの処理後に Ack を呼ぶこと。
// ack されなかったメッセージは RecoverProcessing によって queueName に戻される。
// timeout に 0 を指定すると無期限にブロックし、タイムアウト時は redis.Nil を返す。
func DequeueReliable(ctx context.Context, queueName string, timeout time.Duration) (string, error) {
	return RedisClient.BRPopLPush(ctx, queueName, ProcessingQueueName(queueName), timeout).Result()
}

// Ack は DequeueReliable が返したメッセージを処理中リストから1件削除する。
func Ack(ctx context.Context, queueName, payload string) error {
	return RedisClient.LRem(ctx, ProcessingQueueName(queueName), 1, payload).Err()
}

// Requeue はメッセージを処理中リストから queueName の待ち行列の最後尾へ、MULTI/EXEC で
// アトミックに移動する。取り出しは右端から行うため、左端へ LPUSH すると既に待っている
// メッセージの後に処理される。
func Requeue(ctx context.Context, queueName, payload string) error {
	_, err := RedisClient.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LRem(ctx, ProcessingQueueName(queueName), 1, payload)
		p.LPush(ctx, queueName, payload)
		return nil
	})
	return err
}

// DeadLetter はメッセージを処理中リストからデッドレターリストへ MULTI/EXEC でアトミックに移動する。
func DeadLetter(ctx context.Context, queueName, payload string) error {
	_, err := RedisClient.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LRem(ctx, ProcessingQueueName(queueName), 1, payload)
		p.LPush(ctx, DeadLetterQueueName(queueName), payload)
		return nil
	})
	return err
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
