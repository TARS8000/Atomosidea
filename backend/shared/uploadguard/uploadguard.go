// Package uploadguard は、アップロードを受けてよいかを「サーバーの状態」と「ユーザーの保存量」から判定する。
//
//   - ディスクの空き：空きが UPLOAD_MIN_FREE_DISK_BYTES（既定 10 GiB）＋今回のファイルの 2 倍を下回るなら断る
//     （一時ファイル・MinIO への保存・展開で、受け取ったサイズの数倍を一時的に使うため）。
//     ディスクが埋まると DB や Redis も書けなくなり、サイト全体が止まる
//   - 処理待ち：sfsp のスキャン待ち・動画の変換待ちが多すぎるなら断る（重い処理を際限なく積まない）
//   - 保存量：ユーザーごとの合計（games / static_sites / videos の upload_bytes の和）が上限を超えるなら断る。
//     作ってから日の浅いアカウントは上限を小さくする
//
// 判定に必要な値を読めなかったとき（Redis・DB・ディスクの情報が取れない）は、断らずに通す。
// この仕組みの障害でアップロード全体を止めないため（nginx でもサーバー全体の同時数を制限している）。
package uploadguard

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/atmosidea/shared/queue"
	"github.com/atmosidea/shared/ratelimit"
	"github.com/go-redis/redis/v8"
)

const (
	gib = int64(1) << 30
	mib = int64(1) << 20
)

// Config はアップロードを受けるかどうかの判定に使う上限。0 以下の項目はその判定をしない。
type Config struct {
	QuotaBytes           int64 // ユーザーごとの保存量の上限（UPLOAD_QUOTA_BYTES、既定 5 GiB）
	NewAccountQuotaBytes int64 // 作ってから日の浅いアカウントの上限（UPLOAD_QUOTA_NEW_ACCOUNT_BYTES、既定 1 GiB）
	MinFreeDiskBytes     int64 // 残しておくディスクの空き（UPLOAD_MIN_FREE_DISK_BYTES、既定 10 GiB）
	MaxScanBacklog       int64 // sfsp のスキャン待ち（未着手＋処理中）の上限（UPLOAD_MAX_SCAN_BACKLOG、既定 20）
	MaxTranscodeBacklog  int64 // 動画の変換待ち（未着手＋処理中）の上限（UPLOAD_MAX_TRANSCODE_BACKLOG、既定 10）
	DiskPath             string
}

// LoadConfigFromEnv は環境変数から設定を読む。未設定や数値でない値は既定値を使う。
func LoadConfigFromEnv() Config {
	return Config{
		QuotaBytes:           envBytes("UPLOAD_QUOTA_BYTES", 5*gib),
		NewAccountQuotaBytes: envBytes("UPLOAD_QUOTA_NEW_ACCOUNT_BYTES", 1*gib),
		MinFreeDiskBytes:     envBytes("UPLOAD_MIN_FREE_DISK_BYTES", 10*gib),
		MaxScanBacklog:       int64(ratelimit.EnvInt("UPLOAD_MAX_SCAN_BACKLOG", 20)),
		MaxTranscodeBacklog:  int64(ratelimit.EnvInt("UPLOAD_MAX_TRANSCODE_BACKLOG", 10)),
		DiskPath:             "/",
	}
}

func envBytes(name string, def int64) int64 {
	// EnvInt は int なので、64 bit 環境なら GiB 単位でも収まる
	return int64(ratelimit.EnvInt(name, int(def)))
}

// String はログ用の要約。
func (c Config) String() string {
	return fmt.Sprintf("quota=%s new-account-quota=%s min-free-disk=%s max-scan-backlog=%d max-transcode-backlog=%d",
		FormatBytes(c.QuotaBytes), FormatBytes(c.NewAccountQuotaBytes), FormatBytes(c.MinFreeDiskBytes), c.MaxScanBacklog, c.MaxTranscodeBacklog)
}

// SchemaSQL は保存量を数えるための列を足す SQL。各アップロード API が起動時に流す（何度流してもよい）。
// 3 つのテーブルすべてに足すのは、どのサービスが先に起動しても合計を数えられるようにするため。
// 既存の行は 0 のまま（この仕組みより前の投稿は保存量に数えない）。
var SchemaSQL = []string{
	`ALTER TABLE public.games ADD COLUMN IF NOT EXISTS upload_bytes BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE public.static_sites ADD COLUMN IF NOT EXISTS upload_bytes BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE public.videos ADD COLUMN IF NOT EXISTS upload_bytes BIGINT NOT NULL DEFAULT 0`,
}

// UsageSQL はユーザー（$1 = UUID）の保存量の合計を返す SQL。
const UsageSQL = `SELECT
  COALESCE((SELECT SUM(upload_bytes) FROM public.games WHERE user_id = $1), 0) +
  COALESCE((SELECT SUM(upload_bytes) FROM public.static_sites WHERE user_id = $1), 0) +
  COALESCE((SELECT SUM(upload_bytes) FROM public.videos WHERE uploader_id = $1), 0)`

// Rejection は断るときの HTTP ステータスと、利用者に見せる文言。Status が 0 なら通してよい。
type Rejection struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

// OK は通してよいかを返す。
func (r Rejection) OK() bool { return r.Status == 0 }

// Backlog は sfsp のスキャン待ちと、動画の変換待ち（どちらも未着手＋処理中）の件数を返す。
func Backlog(ctx context.Context, rdb *redis.Client) (scan, transcode int64, err error) {
	if rdb == nil {
		return 0, 0, fmt.Errorf("redis client is nil")
	}
	pipe := rdb.Pipeline()
	s1 := pipe.LLen(ctx, queue.ScanQueueName)
	s2 := pipe.LLen(ctx, queue.ProcessingQueueName(queue.ScanQueueName))
	t1 := pipe.LLen(ctx, queue.StreamCompletionQueue)
	t2 := pipe.LLen(ctx, queue.ProcessingQueueName(queue.StreamCompletionQueue))
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return 0, 0, err
	}
	return s1.Val() + s2.Val(), t1.Val() + t2.Val(), nil
}

// CheckServer はサーバーの状態（ディスクの空き・処理待ち）から、incomingBytes のアップロードを受けてよいかを判定する。
// transcode が true なら動画の変換待ちも見る。err は判定に必要な値を読めなかったときだけ返す（そのときは通す）。
func (c Config) CheckServer(ctx context.Context, rdb *redis.Client, incomingBytes int64, transcode bool) (Rejection, error) {
	var firstErr error

	if c.MinFreeDiskBytes > 0 {
		free, err := FreeDiskBytes(c.DiskPath)
		if err != nil {
			firstErr = fmt.Errorf("read free disk space: %w", err)
		} else if need := c.MinFreeDiskBytes + 2*max64(incomingBytes, 0); free < need {
			return Rejection{
				Status:     http.StatusServiceUnavailable,
				Message:    "サーバーの空き容量が足りないため、いまはアップロードを受け付けていません。時間をおいてお試しください。",
				RetryAfter: 30 * time.Minute,
			}, nil
		}
	}

	if c.MaxScanBacklog > 0 || (transcode && c.MaxTranscodeBacklog > 0) {
		scan, tr, err := Backlog(ctx, rdb)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read backlog: %w", err)
			}
		} else {
			if c.MaxScanBacklog > 0 && scan >= c.MaxScanBacklog {
				return Rejection{
					Status:     http.StatusServiceUnavailable,
					Message:    fmt.Sprintf("いまアップロードが混み合っています（検査待ち %d 件）。数分おいてからお試しください。", scan),
					RetryAfter: 5 * time.Minute,
				}, nil
			}
			if transcode && c.MaxTranscodeBacklog > 0 && tr >= c.MaxTranscodeBacklog {
				return Rejection{
					Status:     http.StatusServiceUnavailable,
					Message:    fmt.Sprintf("いま動画の変換が混み合っています（変換待ち %d 件）。しばらくおいてからお試しください。", tr),
					RetryAfter: 10 * time.Minute,
				}, nil
			}
		}
	}
	return Rejection{}, firstErr
}

// CheckQuota は、すでに used バイト保存しているユーザーが incomingBytes を足してよいかを判定する。
func (c Config) CheckQuota(used, incomingBytes int64, newAccount bool) Rejection {
	quota := c.QuotaBytes
	if newAccount && c.NewAccountQuotaBytes > 0 {
		quota = c.NewAccountQuotaBytes
	}
	if quota <= 0 {
		return Rejection{}
	}
	if used+incomingBytes > quota {
		remaining := quota - used
		if remaining < 0 {
			remaining = 0
		}
		msg := fmt.Sprintf("保存できる容量（%s）を超えます。いまの使用量は %s、残りは %s です。不要な作品を削除してからお試しください。",
			FormatBytes(quota), FormatBytes(used), FormatBytes(remaining))
		if newAccount && c.NewAccountQuotaBytes > 0 && c.NewAccountQuotaBytes < c.QuotaBytes {
			msg += fmt.Sprintf("（作ったばかりのアカウントは %s までです）", FormatBytes(quota))
		}
		return Rejection{Status: http.StatusRequestEntityTooLarge, Message: msg}
	}
	return Rejection{}
}

// FormatBytes はバイト数を「1.5 GB」「300 MB」の形にする（1 GB = 1024^3 バイト）。
func FormatBytes(n int64) string {
	switch {
	case n >= gib:
		v := float64(n) / float64(gib)
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d GB", int64(v))
		}
		return fmt.Sprintf("%.1f GB", v)
	case n >= mib:
		m := (n + mib/2) / mib
		if m >= 1024 {
			return "1 GB"
		}
		return fmt.Sprintf("%d MB", m)
	default:
		return fmt.Sprintf("%d KB", (n+1023)/1024)
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
