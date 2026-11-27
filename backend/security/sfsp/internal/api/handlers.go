package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/atmosidea/sfsp/internal/database"
	"github.com/atmosidea/sfsp/internal/storage"
	"github.com/atmosidea/shared/model"
	"github.com/atmosidea/shared/queue"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// APIKeyHeader は /api/v1/* へのすべてのリクエストが付けなければならないヘッダー名。
const APIKeyHeader = "X-SFSP-API-Key"

// API は API ハンドラが使う依存（設定とロガー）を保持する。
type API struct {
	Config config.Config
	Logger *zap.SugaredLogger
}

// NewAPI は新しい API インスタンスを生成する。
func NewAPI(cfg config.Config, logger *zap.SugaredLogger) *API {
	return &API{
		Config: cfg,
		Logger: logger,
	}
}

// RequireAPIKey は X-SFSP-API-Key ヘッダーが expected と一致しないリクエストを 401 で拒否するミドルウェアを返す。
// キーの内容も長さもタイミングから漏れないよう、両方の値を SHA-256 でハッシュしてから定数時間比較する。
// ハンドラより前に実行されるため、認証されていないアップロードのボディは読まれない。
func RequireAPIKey(expected string) gin.HandlerFunc {
	want := sha256.Sum256([]byte(expected))
	return func(c *gin.Context) {
		provided := c.GetHeader(APIKeyHeader)
		got := sha256.Sum256([]byte(provided))
		if provided == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			// ボディは未読のまま。接続を再利用せず閉じさせ、未認証の大きなボディを読み続けないようにする
			c.Header("Connection", "close")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// allowedServices は target_service として受け付ける値の許可リスト。
var allowedServices = map[string]bool{
	"static-site":     true,
	"game":            true,
	"stream":          true,
	"profile":         true,
	"video-thumbnail": true,
	"game-thumbnail":  true,
	"site-thumbnail":  true,
}

// errUploadTooLarge はファイル部分が MaxUploadBytes を超えたときに countingLimitReader が返すエラー。
var errUploadTooLarge = errors.New("uploaded file exceeds the size limit")

// countingLimitReader は読み取ったバイト数を数え、max を超えて読んだ時点でエラーを返す。
type countingLimitReader struct {
	r io.Reader
	// n はこれまでに読み取ったバイト数、max は許容する最大バイト数。
	// exceeded は上限を超えたかどうか（413 を返すかの判定に使う）。
	n        int64
	max      int64
	exceeded bool
}

// Read は下位の Reader から読み取り、累計が max を超えたら errUploadTooLarge を返す。
func (l *countingLimitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.n > l.max {
		l.exceeded = true
		return n, errUploadTooLarge
	}
	return n, err
}

// isBodyTooLarge は err がサイズ上限超過によるものかを判定する。
// http.MaxBytesError や errUploadTooLarge がラップされずに文字列としてしか残らない場合に備え、
// エラーメッセージの文字列でも判定する。
func isBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe) || errors.Is(err, errUploadTooLarge) ||
		(err != nil && strings.Contains(err.Error(), "request body too large"))
}

// HandleFileUpload はファイルのアップロードを受け付け、MinIO へストリーミング保存し、スキャンジョブをキューに積む。
// multipart のボディはパートごとに読み（MultipartReader）、ファイルパートは SHA256 を計算しながら
// そのまま MinIO へ流すため、アップロード内容をメモリに保持したり一時ファイルにコピーしたりしない。
// 成功時は 202 Accepted で FileID・JobID・状態を返す。
func (a *API) HandleFileUpload(c *gin.Context) {
	// 1. ボディ全体のサイズ上限を設定し、multipart リーダーを取得する
	maxUpload := a.Config.Limits.MaxUploadBytes
	// フォームのフィールドや境界の分として 1 MiB の余裕を持たせる
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUpload+(1<<20))

	mr, err := c.Request.MultipartReader()
	if err != nil {
		a.Logger.Errorf("Failed to read multipart request: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "multipart/form-data request is required"})
		return
	}

	ctx := c.Request.Context()
	rawBucket := a.Config.MinIO.Buckets.RawFiles

	var (
		targetService string
		filename      string
		contentType   string
		fileSHA256    string
		fileSize      int64
		fileID        uuid.UUID
		objectName    string
		uploaded      bool
		committed     bool
	)
	// 途中で失敗した場合は raw-files に置いたオブジェクトを消す
	defer func() {
		if uploaded && !committed {
			delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := storage.DeleteObject(delCtx, rawBucket, objectName); err != nil {
				a.Logger.Warnf("Failed to remove orphaned raw object %s: %v", objectName, err)
			}
		}
	}()

	// 2. パートを順に読み、target_service を取り出し、file を MinIO へストリーミングする
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isBodyTooLarge(err) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file exceeds the maximum size of %d bytes", maxUpload)})
				return
			}
			a.Logger.Errorf("Failed to read multipart part: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Malformed multipart request"})
			return
		}

		switch part.FormName() {
		case "target_service":
			// 短い識別子なので 256 バイトまでしか読まない
			b, err := io.ReadAll(io.LimitReader(part, 256))
			part.Close()
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read target_service"})
				return
			}
			targetService = strings.TrimSpace(string(b))

		case "file":
			if uploaded {
				part.Close()
				c.JSON(http.StatusBadRequest, gin.H{"error": "Only one file is allowed"})
				return
			}
			filename = part.FileName() // multipart は filepath.Base を適用済み
			if filename == "" || filename == "." || filename == "/" {
				part.Close()
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get file from request"})
				return
			}
			// target_service が先に来ていれば、ストリーミング前に検証する
			if targetService != "" && !allowedServices[targetService] {
				part.Close()
				a.rejectTargetService(c, targetService)
				return
			}
			contentType = part.Header.Get("Content-Type")
			fileID = uuid.New()
			// MinIO のオブジェクトキーを FileID の文字列単体に統一
			objectName = fileID.String()

			// サイズを数えつつ SHA256 を計算しながら、サイズ不明（-1）として MinIO へ流し込む
			hash := sha256.New()
			counter := &countingLimitReader{r: part, max: maxUpload}
			a.Logger.Infof("Streaming upload to MinIO bucket '%s' with key '%s'", rawBucket, objectName)
			info, err := storage.UploadStream(ctx, rawBucket, objectName, io.TeeReader(counter, hash), -1, contentType)
			part.Close()
			if err != nil {
				// 部分的に書かれたオブジェクトが残っていれば defer で消す
				uploaded = true
				if counter.exceeded || isBodyTooLarge(err) {
					c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file exceeds the maximum size of %d bytes", maxUpload)})
					return
				}
				a.Logger.Errorf("Failed to upload file to MinIO (Bucket: %s, Key: %s): %v", rawBucket, objectName, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store file"})
				return
			}
			uploaded = true
			// MinIO がサイズを返さなかった場合は自前で数えたバイト数を使う
			fileSize = info.Size
			if fileSize <= 0 {
				fileSize = counter.n
			}
			fileSHA256 = hex.EncodeToString(hash.Sum(nil))

		default:
			// 未知のフィールドは読み捨てる（MaxBytesReader で上限あり）
			part.Close()
		}
	}

	// 3. 必須項目を検証する（file が target_service より先に来た場合はここで初めて検証される。
	//    不正なら defer でアップロード済みオブジェクトが削除される）
	if !uploaded {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get file from request"})
		return
	}
	if targetService == "" {
		a.Logger.Errorf("target_service is missing in form data")
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_service is required"})
		return
	}
	if !allowedServices[targetService] {
		a.rejectTargetService(c, targetService)
		return
	}

	// 4. ファイルのメタデータを files テーブルに登録する
	fileType := determineFileType(filename, contentType)

	newFile := model.File{
		ID:            fileID,
		Filename:      filename,
		Filesize:      fileSize,
		MimeType:      contentType,
		SHA256:        fileSHA256,
		StoragePath:   fmt.Sprintf("%s/%s", rawBucket, objectName),
		FileType:      fileType,
		TargetService: targetService,
		CreatedAt:     time.Now(),
	}
	_, err = database.DB.Exec(ctx,
		"INSERT INTO files (id, filename, filesize, mime_type, sha256, storage_path, file_type, target_service, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)",
		newFile.ID, newFile.Filename, newFile.Filesize, newFile.MimeType, newFile.SHA256, newFile.StoragePath, newFile.FileType, newFile.TargetService, newFile.CreatedAt,
	)
	if err != nil {
		a.Logger.Errorf("Failed to save file metadata to DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file metadata"})
		return
	}

	// 5. queued 状態のスキャンジョブを scan_jobs テーブルに登録する
	newJob := model.ScanJob{
		ID:        uuid.New(),
		FileID:    newFile.ID,
		Status:    "queued",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_, err = database.DB.Exec(ctx,
		"INSERT INTO scan_jobs (id, file_id, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
		newJob.ID, newJob.FileID, newJob.Status, newJob.CreatedAt, newJob.UpdatedAt,
	)
	if err != nil {
		a.Logger.Errorf("Failed to create scan job in DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create scan job"})
		return
	}

	// 6. ジョブ ID を Redis のスキャンキューに積む
	if err := queue.EnqueueJob(ctx, newJob.ID.String()); err != nil {
		a.Logger.Errorf("Failed to enqueue job to Redis: %v", err)
		// キューに入らなかったジョブが queued のまま残らないようにする
		if _, dbErr := database.DB.Exec(context.Background(), "UPDATE scan_jobs SET status = 'failed', processing_details = $1 WHERE id = $2", "Failed to enqueue scan job", newJob.ID); dbErr != nil {
			a.Logger.Errorf("Failed to mark job %s as failed: %v", newJob.ID, dbErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enqueue scan job"})
		return
	}
	// ここまで成功したので、defer による raw オブジェクトの削除を行わない
	committed = true

	a.Logger.Infof("Successfully uploaded file and enqueued job: FileID=%s, JobID=%s, Bucket=%s, ObjectKey=%s, FileType=%s, Size=%d", newFile.ID, newJob.ID, rawBucket, objectName, fileType, fileSize)
	c.JSON(http.StatusAccepted, model.FileUploadResponse{
		FileID: newFile.ID,
		JobID:  newJob.ID,
		Status: newJob.Status,
	})
}

// rejectTargetService は不正な target_service をログに記録し、許可値の一覧を含む 400 を返す。
func (a *API) rejectTargetService(c *gin.Context, targetService string) {
	a.Logger.Errorf("Invalid target_service: %s", targetService)
	c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid target_service: %s. Allowed values are 'static-site', 'game', 'stream', 'profile', 'video-thumbnail', 'game-thumbnail', 'site-thumbnail'", targetService)})
}

// HandleGetJob はパスパラメータ id のスキャンジョブを scan_jobs から取得して返す。
// ID の形式が不正なら 400、取得に失敗した場合（DB エラーを含む）は 404 を返す。
func (a *API) HandleGetJob(c *gin.Context) {
	jobIDStr := c.Param("id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid job ID format"})
		return
	}

	var job model.ScanJob
	err = database.DB.QueryRow(c, "SELECT id, file_id, status, created_at, updated_at FROM scan_jobs WHERE id = $1", jobID).Scan(
		&job.ID, &job.FileID, &job.Status, &job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		a.Logger.Errorf("Failed to get job from DB: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
		return
	}

	c.JSON(http.StatusOK, job)
}

// HandleGetResult はパスパラメータ id のジョブに紐づくスキャナーごとの結果（scan_results）を返す。
// 結果がまだ無い場合は、ジョブが存在すればその状態をメッセージ付きで返し、存在しなければ 404 を返す。
func (a *API) HandleGetResult(c *gin.Context) {
	jobIDStr := c.Param("id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid job ID format"})
		return
	}

	var results []model.ScanResult
	rows, err := database.DB.Query(c, "SELECT id, job_id, scanner, result, details, raw_output, scanned_at FROM scan_results WHERE job_id = $1", jobID)
	if err != nil {
		a.Logger.Errorf("Failed to get scan results from DB: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve scan results"})
		return
	}
	defer rows.Close()

	for rows.Next() {
		var result model.ScanResult
		err := rows.Scan(&result.ID, &result.JobID, &result.Scanner, &result.Result, &result.Details, &result.RawOutput, &result.ScannedAt)
		if err != nil {
			a.Logger.Errorf("Failed to scan row for scan result: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process scan results"})
			return
		}
		results = append(results, result)
	}

	// 結果が無い場合は、ジョブ自体の有無と状態を確認する
	if len(results) == 0 {
		var jobStatus string
		err = database.DB.QueryRow(c, "SELECT status FROM scan_jobs WHERE id = $1", jobID).Scan(&jobStatus)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Job is still processing or has no results yet", "job_status": jobStatus})
		return
	}

	c.JSON(http.StatusOK, results)
}

// determineFileType は拡張子と Content-Type から、ワーカーの処理分岐に使うファイル種別
// （"zip" / "video" / "html" / "other"）を判定する。上の case ほど優先される。
func determineFileType(filename, mimeType string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case ext == ".zip" || mimeType == "application/zip" || mimeType == "application/x-zip-compressed":
		return "zip"
	case strings.HasPrefix(mimeType, "video/") || ext == ".mp4" || ext == ".mkv" || ext == ".webm" || ext == ".mov" || ext == ".avi":
		return "video"
	case ext == ".html" || ext == ".htm" || mimeType == "text/html":
		return "html"
	default:
		return "other"
	}
}
