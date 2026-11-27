package main

// guard.go：アップロードを受けてよいかの判定（ユーザーごとの回数・同時数・保存量、ディスクの空き、処理待ち）と、
// 分割アップロード（Cloudflare の 1 リクエスト 100 MB 制限への対応）をまとめる。
// 中身は backend/shared の ratelimit / uploadguard / chunkupload。3 つのアップロード API で同じ形にしてある。

import (
	"context"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/atmosidea/shared/chunkupload"
	"github.com/atmosidea/shared/queue"
	"github.com/atmosidea/shared/ratelimit"
	"github.com/atmosidea/shared/uploadguard"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const uploadKind = "video"

var (
	uploadGuard uploadguard.Config
	chunkStore  *chunkupload.Store
)

// initUploadGuard は起動時に呼ぶ。保存量を数える列を足し、分割アップロードの置き場所を用意する。
func initUploadGuard(ctx context.Context, maxFileBytes int64) {
	uploadGuard = uploadguard.LoadConfigFromEnv()
	logger.Infof("Upload guard: %s", uploadGuard)
	for _, stmt := range uploadguard.SchemaSQL {
		if _, err := db.Exec(ctx, stmt); err != nil {
			// ほかのサービスのテーブルがまだ無いなど。保存量の判定は、数えられないあいだは通す
			logger.Warnf("Upload guard schema (%s): %v", stmt, err)
		}
	}
	dir := os.Getenv("CHUNK_UPLOAD_DIR")
	if dir == "" {
		dir = "/tmp/chunked-uploads/" + uploadKind
	}
	store, err := chunkupload.New(dir, uploadKind, maxFileBytes)
	if err != nil {
		logger.Fatalf("Failed to prepare chunked upload directory %s: %v", dir, err)
	}
	chunkStore = store
}

// userUsage はユーザーの保存量の合計を返す。
func userUsage(ctx context.Context, userID string) (int64, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return 0, err
	}
	var used int64
	err = db.QueryRow(ctx, uploadguard.UsageSQL, uid).Scan(&used)
	return used, err
}

// checkUploadAllowed は size バイトのアップロードを受けてよいかを判定する（断るなら HTTP ステータスと文言）。
// サーバーの状態（ディスク・処理待ち）は管理者にも適用し、保存量は管理者には適用しない。
func checkUploadAllowed(ctx context.Context, userID string, isAdmin bool, authorization string, size int64) (int, string, time.Duration) {
	rej, err := uploadGuard.CheckServer(ctx, queue.RedisClient, size, true)
	if err != nil {
		logger.Warnf("Upload guard server check failed, allowing: %v", err)
	}
	if !rej.OK() {
		return rej.Status, rej.Message, rej.RetryAfter
	}
	if isAdmin {
		return 0, "", 0
	}
	used, err := userUsage(ctx, userID)
	if err != nil {
		logger.Warnf("Upload guard usage query failed for %s, allowing: %v", userID, err)
		return 0, "", 0
	}
	newAccount := ratelimit.IsNewAccount(authorization, ratelimit.LoadUploadLimitsFromEnv().NewAccountDays)
	if rej := uploadGuard.CheckQuota(used, size, newAccount); !rej.OK() {
		return rej.Status, rej.Message, rej.RetryAfter
	}
	return 0, "", 0
}

// rejectUpload は判定で断るときの応答を返す。
func rejectUpload(c *gin.Context, status int, msg string, retry time.Duration) {
	if retry > 0 {
		c.Header("Retry-After", strconv.Itoa(int(retry/time.Second)+1))
	}
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

// guardUploadSize はハンドラーの中で、ファイルの大きさが分かったところで呼ぶ（断ったら false）。
func guardUploadSize(c *gin.Context, size int64) bool {
	status, msg, retry := checkUploadAllowed(c.Request.Context(), c.GetString("userID"), c.GetBool("isAdmin"), c.GetHeader("Authorization"), size)
	if status != 0 {
		logger.Warnf("Upload rejected: user=%s size=%d status=%d reason=%s", c.GetString("userID"), size, status, msg)
		rejectUpload(c, status, msg, retry)
		return false
	}
	return true
}

// openMainUpload はアップロードの本体を開く。フォームに upload_id があれば分割アップロードで組み立てたファイルを、
// 無ければ multipart の field を使う。ok が false のときは応答済み。
// 返すファイルは Close で後始末される（分割アップロードの一時ファイルも消える）。
func openMainUpload(c *gin.Context, field string) (f multipart.File, filename string, size int64, ok bool) {
	if id := c.PostForm("upload_id"); id != "" {
		cf, err := chunkStore.Open(c.GetString("userID"), id)
		if err != nil {
			status, msg := chunkupload.HTTPStatus(err)
			c.JSON(status, gin.H{"error": msg})
			return nil, "", 0, false
		}
		return cf, cf.Session.Filename, cf.Session.Size, true
	}
	file, header, err := c.Request.FormFile(field)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Video file is required"})
		return nil, "", 0, false
	}
	return file, header.Filename, header.Size, true
}

// registerChunkRoutes は分割アップロードのルート（POST prefix と PUT prefix/:id/:index）を登録する。
// mw には認証ミドルウェアを渡す（グループで認証済みなら空でよい）。
func registerChunkRoutes(r gin.IRoutes, prefix string, mw ...gin.HandlerFunc) {
	withUser := func(c *gin.Context) {
		c.Request = c.Request.WithContext(chunkupload.WithUser(c.Request.Context(), c.GetString("userID")))
		c.Next()
	}
	check := func(r *http.Request, userID string, size int64) (int, string) {
		// 管理者かどうかは gin のコンテキストにしか無いので、ここでは（検証済みの）トークンから読む
		auth := r.Header.Get("Authorization")
		status, msg, _ := checkUploadAllowed(r.Context(), userID, tokenIsAdmin(auth), auth, size)
		return status, msg
	}
	chain := func(last gin.HandlerFunc) []gin.HandlerFunc {
		hs := make([]gin.HandlerFunc, 0, len(mw)+2)
		hs = append(hs, mw...)
		return append(hs, withUser, last)
	}
	r.POST(prefix, chain(gin.WrapF(chunkStore.HandleStart(check)))...)
	r.PUT(prefix+"/:id/:index", chain(gin.WrapF(chunkStore.HandleChunk))...)
}

// tokenIsAdmin は（検証済みの）JWT の isAdmin を読む。
func tokenIsAdmin(authorization string) bool {
	return ratelimit.TokenBool(authorization, "isAdmin")
}

// uploadLimitMiddleware はユーザーごとのアップロード制限（shared/ratelimit）をかける。authMiddleware の後に置く。
// ゲーム・静的サイト・動画で同じ Redis のキーを使うので、本数は 3 種類の合計で数える。
// 作ってから日の浅いアカウントは本数を少なくする。管理者は本数の制限をしない。
// サーバーの状態（ディスクの空き・処理待ち）も、本文を受け取る前にここで一度確かめる。
// Redis に届かないときはログを出して通す（サーバー全体の同時数は nginx でも制限している）。
func uploadLimitMiddleware(limits ratelimit.UploadLimits) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 本文を受け取る前に、サーバーの状態で断れるものは断る（大きさは Content-Length で見積もる）
		rej, err := uploadGuard.CheckServer(c.Request.Context(), queue.RedisClient, c.Request.ContentLength, true)
		if err != nil {
			logger.Warnf("Upload guard server check failed, allowing: %v", err)
		}
		if !rej.OK() {
			logger.Warnf("Upload rejected by server state: user=%s reason=%s", c.GetString("userID"), rej.Message)
			rejectUpload(c, rej.Status, rej.Message, rej.RetryAfter)
			return
		}
		if c.GetBool("isAdmin") {
			c.Next()
			return
		}
		userID := c.GetString("userID")
		l := limits.ForAccount(ratelimit.IsNewAccount(c.GetHeader("Authorization"), limits.NewAccountDays))
		d, err := ratelimit.CheckUpload(c.Request.Context(), queue.RedisClient, l, userID)
		if err != nil {
			logger.Warnf("Upload rate limit check failed, allowing the request: %v", err)
		}
		if !d.Allowed {
			logger.Warnf("Upload rejected by per-user rate limit: user=%s reason=%s", userID, d.Reason)
			rejectUpload(c, http.StatusTooManyRequests, d.Reason, d.RetryAfter)
			return
		}
		defer d.Release()
		c.Next()
	}
}

// imageLimitMiddleware は画像の差し替え（サムネイルなど）をユーザーごとに 1 時間あたり
// IMAGE_UPLOAD_LIMIT_PER_HOUR 回（既定 30）までにする。管理者は対象外。
func imageLimitMiddleware() gin.HandlerFunc {
	limit := ratelimit.EnvInt("IMAGE_UPLOAD_LIMIT_PER_HOUR", 30)
	return func(c *gin.Context) {
		if c.GetBool("isAdmin") {
			c.Next()
			return
		}
		// 画像を含まない小さな更新（タイトルの変更など）は数えない
		if c.Request.ContentLength >= 0 && c.Request.ContentLength < 64<<10 {
			c.Next()
			return
		}
		d, err := ratelimit.CheckAction(c.Request.Context(), queue.RedisClient, c.GetString("userID"), "image", limit, time.Hour, "画像の変更")
		if err != nil {
			logger.Warnf("Image rate limit check failed, allowing: %v", err)
		}
		if !d.Allowed {
			rejectUpload(c, http.StatusTooManyRequests, d.Reason, d.RetryAfter)
			return
		}
		c.Next()
	}
}
