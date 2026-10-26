package worker

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/atmosidea/sfsp/internal/database"
	squeue "github.com/atmosidea/sfsp/internal/queue"
	"github.com/atmosidea/sfsp/internal/sandbox"
	"github.com/atmosidea/sfsp/internal/scanner"
	"github.com/atmosidea/sfsp/internal/storage"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/model"
	"github.com/atmosidea/shared/queue"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Root探索用定数
const (
	maxExploreDepth = 10               // Root探索時の最大ディレクトリ階層
	scanBaseDir     = "/tmp/sfsp-scan" // スキャン用一時ベースディレクトリ
)

// 信頼性キュー関連
const (
	dequeueTimeout = 5 * time.Second // BRPOPLPUSH のブロック時間（ctx のキャンセルを確認するため有限にする）
	maxJobAttempts = 5               // 終端状態にできなかったジョブを再試行する回数。超えたら :dead へ
	retryBackoff   = 10 * time.Second
)

// 終端状態（この状態を DB に記録し、完了イベントを publish したらキューから Ack する）
var terminalStatuses = map[string]bool{
	"clean":      true,
	"malicious":  true,
	"suspicious": true,
	"failed":     true,
	"invalid":    true,
}

// StartWorker は SFSP ワーカープロセスを開始します
// appDB (*pgxpool.Pool) を受け取る (sfsp_worker ロールによる Read-Only 接続)
//
// sfsp:scan_queue は信頼性キューとして扱う: BRPOPLPUSH で sfsp:scan_queue:processing に移し、
// ジョブが終端状態になり完了イベントを publish した後に Ack（LREM）する。
// 起動時に processing に残っているもの（前回のクラッシュ分）を RecoverProcessing で戻す。
func StartWorker(ctx context.Context, cfg config.Config, appDB *pgxpool.Pool, logger *zap.SugaredLogger) {
	sb, err := sandbox.NewDockerSandbox(logger)
	if err != nil {
		logger.Fatalf("Failed to create Docker sandbox: %v", err)
	}

	// shared/queue で接続済みのクライアントを scan_queue の信頼性キュー操作にも使う
	squeue.SetClient(queue.RedisClient, logger)

	// 前回のクラッシュで残った一時ディレクトリを削除（コンシューマーは 1 つだけの前提）
	cleanupStaleScanDirs(logger)

	if n, err := squeue.RecoverProcessing(ctx, squeue.ScanQueueName); err != nil {
		logger.Errorf("Failed to recover unacknowledged jobs from %s: %v", squeue.ProcessingQueueName(squeue.ScanQueueName), err)
	} else if n > 0 {
		logger.Warnf("Recovered %d unacknowledged job(s) from %s back to %s", n, squeue.ProcessingQueueName(squeue.ScanQueueName), squeue.ScanQueueName)
	}

	// バックグラウンドで app-db 監視 ＆ クリーンアップタスクを定期実行
	if appDB != nil {
		go startCleanupWatcher(ctx, cfg, appDB, logger)
	} else {
		logger.Warn("appDB is nil. Cleanup watcher will not start.")
	}

	attempts := map[string]int{}
	for {
		if ctx.Err() != nil {
			logger.Info("Worker context cancelled, stopping job processing.")
			return
		}

		payload, err := squeue.DequeueReliable(ctx, squeue.ScanQueueName, dequeueTimeout)
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue // タイムアウト（キューが空）
			}
			if ctx.Err() != nil {
				logger.Info("Dequeue operation cancelled.")
				return
			}
			logger.Errorf("Failed to dequeue job: %v", err)
			sleepCtx(ctx, 5*time.Second)
			continue
		}

		jobID, err := uuid.Parse(strings.TrimSpace(payload))
		if err != nil {
			logger.Errorf("Invalid job ID received from queue: %q, error: %v (discarding)", payload, err)
			ackJob(logger, payload)
			continue
		}

		logger.Infof("Processing job: %s", jobID)
		if processJob(ctx, cfg, logger, sb, jobID) {
			delete(attempts, payload)
			ackJob(logger, payload)
			continue
		}

		// 終端状態にできなかった（DB / Redis / MinIO の一時的な障害、シャットダウン等）
		if ctx.Err() != nil {
			// processing に残しておけば次回起動時に RecoverProcessing で戻る
			logger.Warnf("Job %s interrupted by shutdown; it will be recovered on next start", jobID)
			return
		}
		attempts[payload]++
		if attempts[payload] >= maxJobAttempts {
			// まず failed として確定させて完了イベントを送る（呼び出し側が scanning のまま残らないように）
			if failJob(ctx, cfg, logger, jobID, fmt.Sprintf("Gave up after %d attempts", attempts[payload])) {
				delete(attempts, payload)
				ackJob(logger, payload)
				continue
			}
			logger.Errorf("Job %s could not reach a terminal status after %d attempts; moving it to %s", jobID, attempts[payload], squeue.DeadLetterQueueName(squeue.ScanQueueName))
			if err := squeue.DeadLetter(ctx, squeue.ScanQueueName, payload); err != nil {
				logger.Errorf("Failed to dead-letter job %s: %v", jobID, err)
			}
			delete(attempts, payload)
			continue
		}
		logger.Warnf("Job %s did not reach a terminal status (attempt %d/%d); requeueing in %s", jobID, attempts[payload], maxJobAttempts, retryBackoff)
		sleepCtx(ctx, retryBackoff)
		if ctx.Err() != nil {
			return
		}
		if err := squeue.Requeue(ctx, squeue.ScanQueueName, payload); err != nil {
			logger.Errorf("Failed to requeue job %s (it stays in the processing list until restart): %v", jobID, err)
		}
	}
}

// ackJob は ctx のキャンセルに関係なく Ack する（処理済みのジョブを再起動後に再処理しないため）
func ackJob(logger *zap.SugaredLogger, payload string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := squeue.Ack(ctx, squeue.ScanQueueName, payload); err != nil {
		logger.Errorf("Failed to ack job %s (it will be re-processed idempotently after restart): %v", payload, err)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func cleanupStaleScanDirs(logger *zap.SugaredLogger) {
	entries, err := os.ReadDir(scanBaseDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "job-") {
			p := filepath.Join(scanBaseDir, e.Name())
			if err := os.RemoveAll(p); err != nil {
				logger.Warnf("Failed to remove stale scan directory %s: %v", p, err)
			} else {
				logger.Infof("Removed stale scan directory %s", p)
			}
		}
	}
}

// =========================================================================
// 定期クリーンアップ (app-db の public ステータス監視 ＆ 消去) ロジック
// =========================================================================

type cleanupJobInfo struct {
	jobID         uuid.UUID
	fileID        uuid.UUID
	sha256        string
	filename      string
	targetService string
	storagePath   string // MinIO上の保存パス
}

func startCleanupWatcher(ctx context.Context, cfg config.Config, appDB *pgxpool.Pool, logger *zap.SugaredLogger) {
	logger.Info("Starting cleanup watcher loop (checking app-db status for 'public' every 30s)...")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Cleanup watcher context cancelled, stopping loop.")
			return
		case <-ticker.C:
			if err := runCleanupBatch(ctx, cfg, appDB, logger); err != nil {
				logger.Errorf("Error during cleanup batch execution: %v", err)
			}
		}
	}
}

func runCleanupBatch(ctx context.Context, cfg config.Config, appDB *pgxpool.Pool, logger *zap.SugaredLogger) error {
	// 1. sfsp-db から「スキャン成功 (clean) かつ 未クリーンアップ」のジョブを取得
	query := `
       SELECT j.id, f.id, f.sha256, f.filename, f.target_service, f.storage_path
       FROM scan_jobs j
       JOIN files f ON j.file_id = f.id
       WHERE j.status = 'clean'
         AND (j.is_cleaned_up IS NOT TRUE)
       LIMIT 50
    `

	rows, err := database.DB.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to query pending cleanup jobs from sfsp-db: %w", err)
	}
	defer rows.Close()

	var jobs []cleanupJobInfo
	for rows.Next() {
		var j cleanupJobInfo
		if err := rows.Scan(&j.jobID, &j.fileID, &j.sha256, &j.filename, &j.targetService, &j.storagePath); err != nil {
			logger.Errorf("Failed to scan cleanup job row: %v", err)
			continue
		}
		jobs = append(jobs, j)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("row iteration error in cleanup batch: %w", err)
	}

	// 2. 抽出した各ジョブの公開ステータスを app-db に問い合せて消去判定
	for _, job := range jobs {
		if err := executeCleanupTask(ctx, cfg, appDB, logger, job); err != nil {
			logger.Errorf("Failed cleanup task for job %s: %v", job.jobID, err)
		}
	}

	return nil
}

func executeCleanupTask(ctx context.Context, cfg config.Config, appDB *pgxpool.Pool, logger *zap.SugaredLogger, job cleanupJobInfo) error {
	var query string

	// ターゲットサービスに応じた app-db テーブル名の決定
	switch strings.ToLower(job.targetService) {
	case "video", "videos", "stream", "streams":
		query = fmt.Sprintf("SELECT status FROM public.videos WHERE sfsp_job_id = $1")
	case "game", "games":
		query = fmt.Sprintf("SELECT status FROM public.games WHERE sfsp_job_id = $1")
	case "static_site", "static_sites", "static-site", "staticsite":
		query = fmt.Sprintf("SELECT status FROM public.static_sites WHERE sfsp_job_id = $1")
	case "thumbnail", "video-thumbnail":
		// サムネイルはvideosテーブルのthumbnail_sfsp_job_idで参照される
		query = "SELECT status FROM public.videos WHERE thumbnail_sfsp_job_id = $1"
	case "game-thumbnail":
		// サムネイルはgamesテーブルのthumbnail_sfsp_job_idで参照される
		query = "SELECT status FROM public.games WHERE thumbnail_sfsp_job_id = $1"
	case "site-thumbnail":
		// サムネイルはstatic_sitesテーブルのthumbnail_sfsp_job_idで参照される
		query = "SELECT status FROM public.static_sites WHERE thumbnail_sfsp_job_id = $1"
	default:
		// ターゲットサービスが不明な場合は 3 テーブルを横断検索
		query = `
          SELECT status FROM (
             SELECT status, sfsp_job_id FROM public.videos
             UNION ALL
             SELECT status, sfsp_job_id FROM public.games
             UNION ALL
             SELECT status, sfsp_job_id FROM public.static_sites
          ) AS combined WHERE sfsp_job_id = $1 LIMIT 1
       `
	}

	var appStatus string
	err := appDB.QueryRow(ctx, query, job.jobID).Scan(&appStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// まだ app-db 側にレコードが存在しない・準備中の場合は次回へスキップ
			return nil
		}
		return fmt.Errorf("failed to check status in app-db for job %s: %w", job.jobID, err)
	}

	// 'public' 以外（まだ処理中・非公開等）なら消去を行わない
	if appStatus != "public" {
		return nil
	}

	// 'public' が確認できたため clean-files バケットからオブジェクトを物理削除
	cleanBucket := cfg.MinIO.Buckets.CleanFiles
	objectName := job.storagePath
	objectName = strings.TrimPrefix(objectName, cleanBucket+"/")
	objectName = strings.TrimPrefix(objectName, cfg.MinIO.Buckets.RawFiles+"/")
	objectName = strings.TrimPrefix(objectName, "/")
	if objectName == "" {
		objectName = job.fileID.String()
	}

	logger.Infof("Target job status is 'public'. Deleting clean file '%s' from bucket '%s' (JobID: %s)",
		objectName, cleanBucket, job.jobID)

	if err := storage.DeleteObject(ctx, cleanBucket, objectName); err != nil {
		return fmt.Errorf("failed to delete object %s from MinIO bucket %s: %w", objectName, cleanBucket, err)
	}

	// sfsp-db 側にクリーンアップ完了（is_cleaned_up = TRUE）を記録
	updateSQL := `
       UPDATE scan_jobs
       SET is_cleaned_up = TRUE,
           cleaned_up_at = NOW()
       WHERE id = $1
    `
	if _, err := database.DB.Exec(ctx, updateSQL, job.jobID); err != nil {
		return fmt.Errorf("failed to update is_cleaned_up in sfsp-db for job %s: %w", job.jobID, err)
	}

	logger.Infof("Successfully cleaned up intermediate file and updated sfsp-db for JobID: %s", job.jobID)
	return nil
}

// ジョブ処理本体
// =========================================================================

// processJob はジョブを処理し、ジョブが終端状態になって完了イベントを publish できた
// （またはキューから捨ててよい）ときに true を返す。false のときは再試行する。
//
// 再処理しても安全（冪等）になるようにしている:
//   - 終端状態で event_published_at があるジョブは何もしない（raw の削除だけ再試行）
//   - 終端状態で未 publish のジョブはイベントの publish だけを行う
//   - queued / running のジョブは scan_results を消してから最初から処理し直す
//     （clean-files / quarantine へのアップロードは同じキーへの上書き）
//   - raw-files の削除は終端状態の記録と publish の後に行うので、途中で落ちても再処理に必要なファイルは残る
func processJob(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger, sb sandbox.Sandbox, jobID uuid.UUID) bool {
	var fileID uuid.UUID
	var status string
	var details *string
	var publishedAt *time.Time
	err := database.DB.QueryRow(ctx,
		"SELECT file_id, status, processing_details, event_published_at FROM scan_jobs WHERE id = $1", jobID,
	).Scan(&fileID, &status, &details, &publishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		logger.Errorf("Job %s not found in sfsp-db; discarding", jobID)
		return true
	}
	if err != nil {
		logger.Errorf("Failed to load job %s: %v", jobID, err)
		return false
	}

	file, err := loadFile(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		// target_service が分からないので完了イベントは送れない
		logger.Errorf("File metadata %s not found for job %s; marking failed", fileID, jobID)
		return updateJobStatus(ctx, jobID, "failed", logger, "File metadata not found") == nil
	}
	if err != nil {
		logger.Errorf("Failed to get file metadata for file %s (job %s): %v", fileID, jobID, err)
		return false
	}

	if terminalStatuses[status] {
		if publishedAt != nil {
			logger.Infof("Job %s already finished with status %s and its completion event was published; nothing to do", jobID, status)
			deleteRaw(ctx, cfg, logger, jobID, file)
			return true
		}
		logger.Warnf("Job %s already has terminal status %s but its completion event was not published; publishing it now", jobID, status)
		d := ""
		if details != nil {
			d = *details
		}
		return finalize(ctx, cfg, logger, jobID, file, status, d, false)
	}

	// queued / running（前回の処理が途中で止まった場合を含む）→ 最初から処理し直す
	if status == "running" {
		logger.Warnf("Job %s was left in 'running' (previous attempt did not finish); re-processing", jobID)
	}
	if err := updateJobStatus(ctx, jobID, "running", logger); err != nil {
		return false
	}
	if _, err := database.DB.Exec(ctx, "DELETE FROM scan_results WHERE job_id = $1", jobID); err != nil {
		logger.Errorf("Failed to clear previous scan results for job %s: %v", jobID, err)
		return false
	}

	jobStatus, processingDetails, ok := scanFile(ctx, cfg, logger, sb, jobID, file)
	if !ok {
		return false
	}
	return finalize(ctx, cfg, logger, jobID, file, jobStatus, processingDetails, true)
}

// failJob は再試行回数を超えたジョブを failed として確定させる（まだ終端状態でない場合のみ）
func failJob(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger, jobID uuid.UUID, reason string) bool {
	var fileID uuid.UUID
	var status string
	if err := database.DB.QueryRow(ctx, "SELECT file_id, status FROM scan_jobs WHERE id = $1", jobID).Scan(&fileID, &status); err != nil {
		return false
	}
	if terminalStatuses[status] {
		return false
	}
	file, err := loadFile(ctx, fileID)
	if err != nil {
		return false
	}
	return finalize(ctx, cfg, logger, jobID, file, "failed", reason, true)
}

func loadFile(ctx context.Context, fileID uuid.UUID) (model.File, error) {
	var file model.File
	err := database.DB.QueryRow(ctx,
		"SELECT id, filename, filesize, mime_type, sha256, storage_path, file_type, target_service FROM files WHERE id = $1", fileID,
	).Scan(&file.ID, &file.Filename, &file.Filesize, &file.MimeType, &file.SHA256, &file.StoragePath, &file.FileType, &file.TargetService)
	return file, err
}

// finalize は終端状態を記録し、完了イベントを publish し、raw-files を削除する。
// 終端状態の記録と publish に成功したら true（= Ack してよい）。
func finalize(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger, jobID uuid.UUID, file model.File, status, details string, setStatus bool) bool {
	if ctx.Err() != nil {
		// シャットダウン中に起きた失敗を終端状態として記録しない
		return false
	}
	if setStatus {
		if err := updateJobStatus(ctx, jobID, status, logger, details); err != nil {
			logger.Errorf("Failed to update final job status for job %s: %v", jobID, err)
			return false
		}
	}

	completionEvent := event.ScanCompletionEvent{
		JobID:         jobID,
		FileID:        file.ID,
		FinalStatus:   status,
		ScannedAt:     time.Now(),
		SHA256:        file.SHA256,
		Filename:      file.Filename,
		TargetService: file.TargetService,
	}
	if err := queue.EnqueueScanCompletionEvent(ctx, completionEvent); err != nil {
		logger.Errorf("Failed to publish completion event for job %s: %v", jobID, err)
		return false
	}
	logger.Infof("Published completion event for job %s with status %s for service %s", jobID, status, completionEvent.TargetService)

	if _, err := database.DB.Exec(ctx, "UPDATE scan_jobs SET event_published_at = NOW() WHERE id = $1", jobID); err != nil {
		// publish 自体は済んでいる。記録できなかった場合はクラッシュ後の再処理でイベントが重複しうる
		logger.Warnf("Failed to record event_published_at for job %s: %v", jobID, err)
	}

	deleteRaw(ctx, cfg, logger, jobID, file)
	logger.Infof("Finished processing job: %s with overall status: %s", jobID, status)
	return true
}

// deleteRaw は raw-files のオブジェクトを削除する（終端状態の後なので失敗しても再試行はしない）
func deleteRaw(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger, jobID uuid.UUID, file model.File) {
	rawBucket := cfg.MinIO.Buckets.RawFiles
	objectName := file.ID.String()
	if err := storage.DeleteObject(ctx, rawBucket, objectName); err != nil {
		logger.Warnf("Failed to delete raw file %s from bucket %s (job %s): %v", objectName, rawBucket, jobID, err)
	}
}

var safeExtRe = regexp.MustCompile(`^\.[A-Za-z0-9]{1,10}$`)

// localInputName はダウンロード先のファイル名を返す。ユーザーのファイル名はパスに使わない
// （"..", 同名ファイルの衝突などを避ける）。拡張子だけ安全な場合に残す。
func localInputName(filename string) string {
	ext := filepath.Ext(filepath.Base(filename))
	if !safeExtRe.MatchString(ext) {
		ext = ""
	}
	return "input" + strings.ToLower(ext)
}

// scanFile はファイルをダウンロード・展開・スキャンし、結果のバケットへアップロードする。
// ok=false は一時的な失敗（再試行する）。
func scanFile(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger, sb sandbox.Sandbox, jobID uuid.UUID, file model.File) (jobStatus string, processingDetails string, ok bool) {
	logger.Infof("FileType from DB for file %s (job %s): %s", file.ID, jobID, file.FileType)

	rawBucket := cfg.MinIO.Buckets.RawFiles
	objectName := file.ID.String()

	// /tmp/sfsp-scan ディレクトリが存在しない場合は作成
	if err := os.MkdirAll(scanBaseDir, 0755); err != nil {
		logger.Errorf("Failed to create base scan directory %s for job %s: %v", scanBaseDir, jobID, err)
		return "", "", false
	}

	// /tmp/sfsp-scan 配下にジョブ毎の一時ディレクトリを作成
	tempDir, err := os.MkdirTemp(scanBaseDir, "job-")
	if err != nil {
		logger.Errorf("Failed to create temp directory for job %s: %v", jobID, err)
		return "", "", false
	}
	defer os.RemoveAll(tempDir)

	// サンドボックスのスキャナーは CapDrop: ALL（CAP_DAC_OVERRIDE なし）で動くため、
	// ワーカーが非 root で作ったディレクトリ・ファイルが「その他」から読めないとスキャンできない。
	// MkdirTemp は 0700 で作るので、読み取り・走査できるよう 0755 にする
	if err := os.Chmod(tempDir, 0755); err != nil {
		logger.Errorf("Failed to set permissions on temp directory for job %s: %v", jobID, err)
		return "", "", false
	}

	downloadPath := filepath.Join(tempDir, localInputName(file.Filename))

	logger.Infof("Downloading file %s from MinIO bucket %s for job %s", objectName, rawBucket, jobID)
	if err := storage.DownloadFile(ctx, rawBucket, objectName, downloadPath); err != nil {
		if storage.IsNotFound(err) {
			logger.Errorf("Raw file %s not found in bucket %s for job %s", objectName, rawBucket, jobID)
			return "failed", "Uploaded file not found in raw storage", true
		}
		logger.Errorf("Failed to download file %s from bucket %s for job %s: %v", objectName, rawBucket, jobID, err)
		return "", "", false
	}

	// FGetObject は 0600 でファイルを作る。zip 以外はこのファイル自体をスキャナーが読むので 0644 にする
	if err := os.Chmod(downloadPath, 0644); err != nil {
		logger.Errorf("Failed to set permissions on downloaded file for job %s: %v", jobID, err)
		return "", "", false
	}

	scanPath := downloadPath
	jobStatus = "" // 空 = まだ未確定（スキャンする）

	switch file.FileType {
	case "zip":
		logger.Infof("Processing ZIP file for job %s", jobID)
		unzipDir := filepath.Join(tempDir, "unzipped")
		if err := os.Mkdir(unzipDir, 0755); err != nil {
			logger.Errorf("Failed to create unzip directory for job %s: %v", jobID, err)
			return "", "", false
		}

		// 下流のワーカーは ZIP 全体を展開して公開するため、検出したルートだけでなく
		// 展開したディレクトリ全体をスキャン対象にする
		scanPath = unzipDir

		if err := unzip(downloadPath, unzipDir, cfg); err != nil {
			var policyErr *unzipPolicyError
			var ioErr *unzipIOError
			switch {
			case errors.As(err, &policyErr):
				// zip bomb / zip slip 等の疑い。clean にはせず隔離する
				logger.Warnf("ZIP for job %s rejected by extraction policy: %v", jobID, err)
				jobStatus = "suspicious"
				processingDetails = fmt.Sprintf("Archive rejected: %v", err)
			case errors.As(err, &ioErr):
				logger.Errorf("Failed to extract ZIP for job %s (I/O): %v", jobID, err)
				jobStatus = "failed"
				processingDetails = fmt.Sprintf("Failed to extract archive: %v", err)
			default:
				logger.Errorf("Failed to unzip file for job %s: %v", jobID, err)
				jobStatus = "invalid"
				processingDetails = fmt.Sprintf("Failed to unzip file: %v", err)
			}
		} else {
			statLimit := cfg.Limits.MaxZipEntries*2 + 16
			unityRoots, unityErr := findUnityWebGLRoot(unzipDir, statLimit, logger)
			if unityErr != nil {
				logger.Errorf("Error during Unity WebGL root search for job %s: %v", jobID, unityErr)
			}

			if len(unityRoots) == 1 {
				logger.Infof("Detected Unity WebGL root: %s for job %s", unityRoots[0], jobID)
				processingDetails = fmt.Sprintf("Detected Unity WebGL root: %s", filepath.Base(unityRoots[0]))
			} else if len(unityRoots) > 1 {
				logger.Warnf("ZIP file for job %s: Multiple Unity WebGL builds found: %v", jobID, unityRoots)
				jobStatus = "invalid"
				processingDetails = fmt.Sprintf("Multiple Unity WebGL builds found: %v", unityRoots)
			} else {
				staticSiteRoots, staticSiteErr := findStaticSiteRoot(unzipDir, statLimit, logger)
				if staticSiteErr != nil {
					logger.Errorf("Error during Static Site root search for job %s: %v", jobID, staticSiteErr)
				}

				if len(staticSiteRoots) == 1 {
					logger.Infof("Detected Static Site root: %s for job %s", staticSiteRoots[0], jobID)
					processingDetails = fmt.Sprintf("Detected Static Site root: %s", filepath.Base(staticSiteRoots[0]))
				} else if len(staticSiteRoots) > 1 {
					logger.Warnf("ZIP file for job %s: Multiple Static Site builds found: %v", jobID, staticSiteRoots)
					jobStatus = "invalid"
					processingDetails = fmt.Sprintf("Multiple Static Site builds found: %v", staticSiteRoots)
				} else {
					logger.Warnf("ZIP file for job %s: No valid Unity WebGL or Static Site build found.", jobID)
					jobStatus = "invalid"
					processingDetails = "No valid Unity WebGL or Static Site build found."
				}
			}
		}
	case "video":
		logger.Infof("Processing video file for job %s", jobID)
		processingDetails = "Video file scan."
	case "html":
		logger.Infof("Processing HTML file for job %s", jobID)
		processingDetails = "HTML file scan."
	default:
		logger.Infof("Processing other file type for job %s", jobID)
		processingDetails = "Other file type scan."
	}

	if jobStatus == "" {
		clamAVScanner := scanner.NewClamAVScanner(cfg.Scanner.ClamAVImage, scanner.ClamAVLimits{
			MaxFileSize:     cfg.ClamAV.MaxFileSize,
			MaxScanSize:     cfg.ClamAV.MaxScanSize,
			MaxFiles:        cfg.ClamAV.MaxFiles,
			MaxRecursion:    cfg.ClamAV.MaxRecursion,
			MaxDirRecursion: cfg.ClamAV.MaxDirRecursion,
			MaxScanTimeMs:   cfg.ClamAV.MaxScanTimeMs,
			PCREMaxFileSize: cfg.ClamAV.PCREMaxFileSize,
		}, logger)
		yaraScanner := scanner.NewYARAScanner(cfg.Scanner.YARAImage, cfg.Scanner.YARARulesPath, logger)

		clamavResult, err := clamAVScanner.Scan(ctx, sb, jobID.String(), scanPath)
		if err != nil {
			logger.Errorf("ClamAV scan failed for job %s: %v", jobID, err)
			clamavResult.Result = "error"
			if clamavResult.Details == "" {
				clamavResult.Details = err.Error()
			}
		}
		saveScanResult(ctx, jobID, "clamav", clamavResult.Result, clamavResult.Details, clamavResult.RawOutput, logger)

		yaraResult, err := yaraScanner.Scan(ctx, sb, jobID.String(), scanPath)
		if err != nil {
			logger.Errorf("YARA scan failed for job %s: %v", jobID, err)
			yaraResult.Result = "error"
			if yaraResult.Details == "" {
				yaraResult.Details = err.Error()
			}
		}
		saveScanResult(ctx, jobID, "yara", yaraResult.Result, yaraResult.Details, yaraResult.RawOutput, logger)

		if ctx.Err() != nil {
			// シャットダウンでスキャンが中断された。failed として確定させず再処理する
			return "", "", false
		}
		jobStatus = determineOverallStatus(clamavResult, yaraResult)
		if jobStatus != "clean" {
			processingDetails = strings.TrimSpace(fmt.Sprintf("%s ClamAV: %s / YARA: %s", processingDetails, clamavResult.Details, yaraResult.Details))
		}
	}

	// 異インスタンス間での CopyObject 回避のため、ローカルファイルから対象バケットへアップロードする。
	// 失敗した場合は一時的な障害とみなして再試行する（raw-files はまだ削除していない）
	switch jobStatus {
	case "clean":
		if _, err := storage.UploadFile(ctx, cfg.MinIO.Buckets.CleanFiles, objectName, downloadPath, file.MimeType); err != nil {
			logger.Errorf("Failed to upload clean file to clean-files bucket for job %s: %v", jobID, err)
			return "", "", false
		}
	case "malicious", "suspicious":
		if _, err := storage.UploadFile(ctx, cfg.MinIO.Buckets.Quarantine, objectName, downloadPath, file.MimeType); err != nil {
			logger.Errorf("Failed to upload file to quarantine bucket for job %s: %v", jobID, err)
			return "", "", false
		}
	}

	return jobStatus, processingDetails, true
}

func updateJobStatus(ctx context.Context, jobID uuid.UUID, status string, logger *zap.SugaredLogger, details ...string) error {
	detailMsg := ""
	if len(details) > 0 {
		detailMsg = details[0]
	}
	_, err := database.DB.Exec(ctx, "UPDATE scan_jobs SET status = $1, processing_details = $2, updated_at = NOW() WHERE id = $3", status, detailMsg, jobID)
	if err != nil {
		logger.Errorf("Failed to update job status to %s for job %s: %v", status, jobID, err)
	}
	return err
}

func saveScanResult(ctx context.Context, jobID uuid.UUID, scannerName, result, details string, rawOutput *map[string]any, logger *zap.SugaredLogger) {
	if rawOutput == nil {
		rawOutput = &map[string]any{}
	}
	_, err := database.DB.Exec(ctx,
		"INSERT INTO scan_results (job_id, scanner, result, details, raw_output, scanned_at) VALUES ($1, $2, $3, $4, $5, NOW())",
		jobID, scannerName, result, details, rawOutput,
	)
	if err != nil {
		logger.Errorf("Failed to save scan result for job %s, scanner %s: %v", jobID, scannerName, err)
	}
}

// determineOverallStatus は両方のスキャナーが明示的に clean のときだけ clean を返す
func determineOverallStatus(clamavRes, yaraRes scanner.ScanResult) string {
	if clamavRes.Result == "malicious" {
		return "malicious"
	}
	if clamavRes.Result == "suspicious" || yaraRes.Result == "suspicious" {
		return "suspicious"
	}
	if clamavRes.Result == "clean" && yaraRes.Result == "clean" {
		return "clean"
	}
	return "failed"
}

// =========================================================================
// zip 展開（zip bomb / zip slip 対策）
// =========================================================================

// unzipPolicyError は展開ポリシー違反（上限超過、Zip Slip、シンボリックリンク等）。suspicious として隔離する
type unzipPolicyError struct{ msg string }

func (e *unzipPolicyError) Error() string { return e.msg }

func policyErrorf(format string, a ...any) error {
	return &unzipPolicyError{msg: fmt.Sprintf(format, a...)}
}

// unzipIOError は展開先への書き込み失敗（ディスク不足等）。failed とする
type unzipIOError struct{ err error }

func (e *unzipIOError) Error() string { return e.err.Error() }
func (e *unzipIOError) Unwrap() error { return e.err }

// errWriter は書き込み側のエラーを記録する（io.CopyN のエラーが読み出し側か書き込み側かを区別するため）
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil {
		e.err = err
	}
	return n, err
}

const ratioCheckThreshold = 1024 * 1024 // これより小さいエントリは圧縮率を確認しない

// unzip は src を dest に展開する。メモリに読み込まず io.CopyN でディスクに書き出し、
// 合計サイズ・エントリ数・1 エントリのサイズ・圧縮率・階層の上限と Zip Slip を確認する。
// archive/zip は宣言された展開後サイズを超えて読むと ErrFormat を返すため、宣言値は上限として信頼できる。
func unzip(src, dest string, cfg config.Config) error {
	lim := cfg.Limits
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	if len(r.File) > lim.MaxZipEntries {
		return policyErrorf("too many entries in archive: %d (limit %d)", len(r.File), lim.MaxZipEntries)
	}

	// 展開前に宣言値で判定（早期に拒否）
	var declared uint64
	for _, f := range r.File {
		if f.UncompressedSize64 > uint64(lim.MaxZipEntryBytes) {
			return policyErrorf("entry %q is too large: %d bytes (limit %d)", f.Name, f.UncompressedSize64, lim.MaxZipEntryBytes)
		}
		declared += f.UncompressedSize64
		if declared > uint64(lim.MaxExtractedBytes) {
			return policyErrorf("archive expands to more than %d bytes", lim.MaxExtractedBytes)
		}
	}

	cleanDest := filepath.Clean(dest)
	seen := make(map[string]bool, len(r.File))
	var total int64

	for _, f := range r.File {
		name := f.Name
		if name == "" || strings.ContainsRune(name, 0) {
			return policyErrorf("invalid entry name %q", name)
		}
		if path.IsAbs(name) || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
			return policyErrorf("illegal absolute path in archive: %q", name)
		}
		cleanName := path.Clean(name)
		if cleanName == ".." || strings.HasPrefix(cleanName, "../") {
			return policyErrorf("illegal file path in archive: %q", name)
		}
		fpath := filepath.Join(cleanDest, filepath.FromSlash(cleanName))
		rel, err := filepath.Rel(cleanDest, fpath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return policyErrorf("illegal file path in archive: %q", name)
		}
		if rel == "." {
			continue
		}
		if depth := strings.Count(filepath.ToSlash(rel), "/") + 1; depth > lim.MaxZipDepth {
			return policyErrorf("entry %q is nested too deeply: %d levels (limit %d)", name, depth, lim.MaxZipDepth)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return &unzipIOError{err: err}
			}
			continue
		}
		if !f.Mode().IsRegular() {
			// シンボリックリンク・デバイス等は展開しない（スキャン対象と公開内容がずれるのを防ぐ）
			return policyErrorf("non-regular entry in archive (symlink/device): %q", name)
		}
		if seen[rel] {
			return policyErrorf("duplicate entry in archive: %q", name)
		}
		seen[rel] = true

		if f.UncompressedSize64 > ratioCheckThreshold {
			if f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > uint64(lim.MaxZipRatio) {
				return policyErrorf("entry %q has a suspicious compression ratio (%d -> %d bytes, limit %dx)", name, f.CompressedSize64, f.UncompressedSize64, lim.MaxZipRatio)
			}
		}

		if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
			return &unzipIOError{err: err}
		}
		out, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			if os.IsExist(err) {
				return policyErrorf("duplicate entry in archive: %q", name)
			}
			return &unzipIOError{err: err}
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}

		remaining := lim.MaxExtractedBytes - total
		if remaining > lim.MaxZipEntryBytes {
			remaining = lim.MaxZipEntryBytes
		}
		ew := &errWriter{w: out}
		n, copyErr := io.CopyN(ew, rc, remaining+1)
		rc.Close()
		closeErr := out.Close()
		total += n

		if n > remaining {
			return policyErrorf("archive exceeds extraction limits while extracting %q", name)
		}
		if copyErr != nil && copyErr != io.EOF {
			if ew.err != nil {
				return &unzipIOError{err: ew.err}
			}
			return copyErr
		}
		if closeErr != nil {
			return &unzipIOError{err: closeErr}
		}
	}
	return nil
}

func findUnityWebGLRoot(baseDir string, maxFilesToStat int, logger *zap.SugaredLogger) ([]string, error) {
	var unityRoots []string
	filesStatCount := 0

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		filesStatCount++
		if filesStatCount > maxFilesToStat {
			return fmt.Errorf("exceeded max files to stat (%d) in Unity WebGL root exploration", maxFilesToStat)
		}

		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		if strings.Count(relPath, string(os.PathSeparator)) >= maxExploreDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
		}

		if d.IsDir() {
			indexPath := filepath.Join(path, "index.html")
			buildDirPath := filepath.Join(path, "Build")

			_, errIndex := os.Stat(indexPath)
			_, errBuild := os.Stat(buildDirPath)

			if errIndex == nil && errBuild == nil {
				unityRoots = append(unityRoots, path)
				logger.Debugf("Found potential Unity WebGL root: %s", path)
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return unityRoots, nil
}

func findStaticSiteRoot(baseDir string, maxFilesToStat int, logger *zap.SugaredLogger) ([]string, error) {
	var staticSiteRoots []string
	filesStatCount := 0

	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		filesStatCount++
		if filesStatCount > maxFilesToStat {
			return fmt.Errorf("exceeded max files to stat (%d) in Static Site root exploration", maxFilesToStat)
		}

		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		if strings.Count(relPath, string(os.PathSeparator)) >= maxExploreDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
		}

		if d.IsDir() {
			indexPath := filepath.Join(path, "index.html")
			if _, err := os.Stat(indexPath); err == nil {
				staticSiteRoots = append(staticSiteRoots, path)
				logger.Debugf("Found potential Static Site root: %s", path)
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return staticSiteRoots, nil
}
