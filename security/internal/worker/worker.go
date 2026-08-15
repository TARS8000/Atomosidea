package worker

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/atmosidea/sfsp/internal/database"
	"github.com/atmosidea/sfsp/internal/sandbox"
	"github.com/atmosidea/sfsp/internal/scanner"
	"github.com/atmosidea/sfsp/internal/storage"
	"github.com/atmosidea/shared/event"
	"github.com/atmosidea/shared/model"
	"github.com/atmosidea/shared/queue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// Constants for root exploration
const (
	maxExploreDepth = 10               // Max directory depth to explore for roots
	maxFilesToStat  = 10000            // Max number of files/dirs to stat during exploration
	scanBaseDir     = "/tmp/sfsp-scan" // ベースとなるスキャン用一時ディレクトリ
)

// StartWorker starts the SFSP worker process
// appDB (*pgxpool.Pool) を受け取る（sfsp_worker ロールによる Read-Only 接続）
func StartWorker(ctx context.Context, cfg config.Config, appDB *pgxpool.Pool, logger *zap.SugaredLogger) {
	sb, err := sandbox.NewDockerSandbox(logger)
	if err != nil {
		logger.Fatalf("Failed to create Docker sandbox: %v", err)
	}

	// バックグラウンドで app-db 監視 ＆ クリーンアップタスクを定期実行
	if appDB != nil {
		go startCleanupWatcher(ctx, cfg, appDB, logger)
	} else {
		logger.Warn("appDB is nil. Cleanup watcher will not start.")
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("Worker context cancelled, stopping job processing.")
			return
		default:
			jobIDStr, err := queue.DequeueJob(ctx)
			if err != nil {
				if err == context.Canceled {
					logger.Info("Dequeue operation cancelled.")
					return
				}
				logger.Errorf("Failed to dequeue job: %v", err)
				time.Sleep(5 * time.Second) // Wait before retrying
				continue
			}

			jobID, err := uuid.Parse(jobIDStr)
			if err != nil {
				logger.Errorf("Invalid job ID received from queue: %s, error: %v", jobIDStr, err)
				continue
			}

			logger.Infof("Processing job: %s", jobID)
			processJob(ctx, cfg, logger, sb, jobID)
		}
	}
}

// =========================================================================
// 定期クリーンアップ（監視 ＆ 中間ファイル消去）ロジック
// =========================================================================

type cleanupJobInfo struct {
	jobID         uuid.UUID
	fileID        uuid.UUID
	sha256        string
	filename      string
	targetService string
	storagePath   string // MinIO上の正確な保存パス
}

func startCleanupWatcher(ctx context.Context, cfg config.Config, appDB *pgxpool.Pool, logger *zap.SugaredLogger) {
	logger.Info("Starting cleanup watcher loop (checking app-db status every 30s)...")
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
	// ターゲットサービスに応じた app-db テーブル名の確定（public. スキーマを明示）
	var tableName string
	switch strings.ToLower(job.targetService) {
	case "video", "videos", "stream", "streams": // "stream", "streams" を videos テーブルにマッピング
		tableName = "public.videos"
	case "game", "games":
		tableName = "public.games"
	case "static_site", "static_sites":
		tableName = "public.static_sites"
	default:
		logger.Warnf("Unknown target service '%s' for job %s, skipping cleanup", job.targetService, job.jobID)
		return nil
	}

	// app-db (Read-Only) に対して sfsp_job_id の status を照会
	// ※tableName には "public.videos" 等が入るためクォートせず直接識別子として展開する
	query := fmt.Sprintf("SELECT status FROM %s WHERE sfsp_job_id = $1", tableName)
	var appStatus string
	err := appDB.QueryRow(ctx, query, job.jobID).Scan(&appStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// まだ app-db 側にレコードが存在しない・準備中の場合は次回へスキップ
			return nil
		}
		return fmt.Errorf("failed to check status in app-db for table %s: %w", tableName, err)
	}

	// 'public' 以外（まだ処理中・非公開等）なら何もしない
	if appStatus != "public" {
		return nil
	}

	// 'public' が確認できたため clean-files バケットからオブジェクトを物理削除
	cleanBucket := cfg.MinIO.Buckets.CleanFiles
	objectName := strings.TrimPrefix(job.storagePath, cleanBucket+"/")
	objectName = strings.TrimPrefix(objectName, cfg.MinIO.Buckets.RawFiles+"/")
	objectName = strings.TrimPrefix(objectName, "/")

	logger.Infof("Target %s status is 'public'. Deleting clean file '%s' from bucket '%s' (JobID: %s)",
		tableName, objectName, cleanBucket, job.jobID)

	if err := storage.DeleteObject(ctx, cleanBucket, objectName); err != nil {
		return fmt.Errorf("failed to delete object %s from MinIO bucket %s: %w", objectName, cleanBucket, err)
	}

	// sfsp-db 側にクリーンアップ完了（is_cleaned_up = TRUE）を追記
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

// =========================================================================
// ジョブ処理本体 (既存のスキャン処理)
// =========================================================================

func processJob(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger, sb sandbox.Sandbox, jobID uuid.UUID) {
	processingDetails := "" // Initialize processingDetails here

	if err := updateJobStatus(ctx, jobID, "running", logger); err != nil {
		return
	}

	var fileID uuid.UUID
	err := database.DB.QueryRow(ctx, "SELECT file_id FROM scan_jobs WHERE id = $1", jobID).Scan(&fileID)
	if err != nil {
		logger.Errorf("Failed to get file_id for job %s: %v", jobID, err)
		updateJobStatus(ctx, jobID, "failed", logger)
		return
	}

	var file model.File
	err = database.DB.QueryRow(ctx, "SELECT id, filename, filesize, mime_type, sha256, storage_path, file_type, target_service FROM files WHERE id = $1", fileID).Scan(
		&file.ID, &file.Filename, &file.Filesize, &file.MimeType, &file.SHA256, &file.StoragePath, &file.FileType, &file.TargetService,
	)
	if err != nil {
		logger.Errorf("Failed to get file metadata for file %s (job %s): %v", fileID, jobID, err)
		updateJobStatus(ctx, jobID, "failed", logger)
		return
	}
	logger.Infof("DEBUG: FileType from DB for file %s (job %s): %s", file.ID, jobID, file.FileType)

	// /tmp/sfsp-scan ディレクトリが存在しない場合は作成
	if err := os.MkdirAll(scanBaseDir, 0755); err != nil {
		logger.Errorf("Failed to create base scan directory %s for job %s: %v", scanBaseDir, jobID, err)
		updateJobStatus(ctx, jobID, "failed", logger)
		return
	}

	// /tmp/sfsp-scan 配下にジョブ毎の一時ディレクトリを作成
	tempDir, err := os.MkdirTemp(scanBaseDir, "job-")
	if err != nil {
		logger.Errorf("Failed to create temp directory for job %s: %v", jobID, err)
		updateJobStatus(ctx, jobID, "failed", logger)
		return
	}
	defer os.RemoveAll(tempDir)

	downloadPath := filepath.Join(tempDir, file.Filename)
	bucketName := cfg.MinIO.Buckets.RawFiles

	// DBの storage_path から先頭のバケット名やスラッシュを除去
	objectName := strings.TrimPrefix(file.StoragePath, bucketName+"/")
	objectName = strings.TrimPrefix(objectName, "/")

	logger.Infof("Downloading file %s from MinIO bucket %s for job %s", objectName, bucketName, jobID)
	if err := storage.DownloadFile(ctx, bucketName, objectName, downloadPath); err != nil {
		logger.Errorf("Failed to download file %s from bucket %s for job %s: %v", objectName, bucketName, jobID, err)
		updateJobStatus(ctx, jobID, "failed", logger)
		return
	}

	scanPath := downloadPath
	jobStatus := "completed"

	switch file.FileType {
	case "zip":
		logger.Infof("Processing ZIP file for job %s", jobID)
		unzipDir := filepath.Join(tempDir, "unzipped")
		if err := os.Mkdir(unzipDir, 0755); err != nil {
			logger.Errorf("Failed to create unzip directory for job %s: %v", jobID, err)
			updateJobStatus(ctx, jobID, "failed", logger)
			return
		}

		if err := unzip(downloadPath, unzipDir); err != nil {
			logger.Errorf("Failed to unzip file for job %s: %v", jobID, err)
			jobStatus = "invalid"
			processingDetails = fmt.Sprintf("Failed to unzip file: %v", err)
		} else {
			unityRoots, unityErr := findUnityWebGLRoot(unzipDir, logger)
			if unityErr != nil {
				logger.Errorf("Error during Unity WebGL root search for job %s: %v", jobID, unityErr)
			}

			if len(unityRoots) == 1 {
				scanPath = unityRoots[0]
				logger.Infof("Detected Unity WebGL root: %s for job %s", scanPath, jobID)
				processingDetails = fmt.Sprintf("Detected Unity WebGL root: %s", filepath.Base(scanPath))
			} else if len(unityRoots) > 1 {
				logger.Warnf("ZIP file for job %s: Multiple Unity WebGL builds found: %v", jobID, unityRoots)
				jobStatus = "invalid"
				processingDetails = fmt.Sprintf("Multiple Unity WebGL builds found: %v", unityRoots)
			} else {
				staticSiteRoots, staticSiteErr := findStaticSiteRoot(unzipDir, logger)
				if staticSiteErr != nil {
					logger.Errorf("Error during Static Site root search for job %s: %v", jobID, staticSiteErr)
				}

				if len(staticSiteRoots) == 1 {
					scanPath = staticSiteRoots[0]
					logger.Infof("Detected Static Site root: %s for job %s", scanPath, jobID)
					processingDetails = fmt.Sprintf("Detected Static Site root: %s", filepath.Base(scanPath))
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

	var clamavResult, yaraResult scanner.ScanResult
	if jobStatus != "invalid" {
		clamAVScanner := scanner.NewClamAVScanner(cfg.Scanner.ClamAVImage, logger)
		yaraScanner := scanner.NewYARAScanner(cfg.Scanner.YARAImage, cfg.Scanner.YARARulesPath, logger)

		clamavResult, err = clamAVScanner.Scan(ctx, sb, jobID.String(), scanPath)
		if err != nil {
			logger.Errorf("ClamAV scan failed for job %s: %v", jobID, err)
			saveScanResult(ctx, jobID, "clamav", "error", err.Error(), nil, logger)
		} else {
			saveScanResult(ctx, jobID, "clamav", clamavResult.Result, clamavResult.Details, clamavResult.RawOutput, logger)
		}

		yaraResult, err = yaraScanner.Scan(ctx, sb, jobID.String(), scanPath)
		if err != nil {
			logger.Errorf("YARA scan failed for job %s: %v", jobID, err)
			saveScanResult(ctx, jobID, "yara", "error", err.Error(), nil, logger)
		} else {
			saveScanResult(ctx, jobID, "yara", yaraResult.Result, yaraResult.Details, yaraResult.RawOutput, logger)
		}
		jobStatus = determineOverallStatus(clamavResult, yaraResult)
	}

	// Move file to appropriate bucket based on scan result
	if jobStatus == "clean" {
		_, err := storage.CopyObject(ctx, cfg.MinIO.Buckets.CleanFiles, objectName, bucketName, objectName)
		if err != nil {
			logger.Errorf("Failed to copy clean file to clean-files bucket for job %s: %v", jobID, err)
			jobStatus = "failed"
			processingDetails = "Failed to move clean file to storage."
		} else {
			// コピー成功後に Raw バケットから削除
			if err := storage.DeleteObject(ctx, bucketName, objectName); err != nil {
				logger.Warnf("Failed to delete original raw file %s after moving to clean bucket for job %s: %v", objectName, jobID, err)
			}
		}
	} else if jobStatus == "malicious" || jobStatus == "suspicious" {
		_, err := storage.CopyObject(ctx, cfg.MinIO.Buckets.Quarantine, objectName, bucketName, objectName)
		if err != nil {
			logger.Errorf("Failed to copy malicious file to quarantine bucket for job %s: %v", jobID, err)
			jobStatus = "failed"
			processingDetails = "Failed to move malicious file to quarantine."
		} else {
			// コピー成功後に Raw バケットから削除
			if err := storage.DeleteObject(ctx, bucketName, objectName); err != nil {
				logger.Warnf("Failed to delete original raw file %s after moving to quarantine bucket for job %s: %v", objectName, jobID, err)
			}
		}
	}

	// Update job status with final processing details
	if err := updateJobStatus(ctx, jobID, jobStatus, logger, processingDetails); err != nil {
		logger.Errorf("Failed to update final job status for job %s: %v", jobID, err)
		return
	}

	completionEvent := event.ScanCompletionEvent{
		JobID:         jobID,
		FileID:        file.ID,
		FinalStatus:   jobStatus,
		ScannedAt:     time.Now(),
		SHA256:        file.SHA256,
		Filename:      file.Filename,
		TargetService: file.TargetService,
	}
	logger.Infof("DEBUG completion event: %+v", completionEvent)

	if err := queue.EnqueueScanCompletionEvent(ctx, completionEvent); err != nil {
		logger.Errorf("Failed to publish completion event for job %s: %v", jobID, err)
	} else {
		logger.Infof("Published completion event for job %s with status %s for service %s", jobID, jobStatus, completionEvent.TargetService)
	}

	logger.Infof("Finished processing job: %s with overall status: %s", jobID, jobStatus)
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

func determineOverallStatus(clamavRes, yaraRes scanner.ScanResult) string {
	if clamavRes.Result == "malicious" {
		return "malicious"
	}
	if yaraRes.Result == "suspicious" {
		return "suspicious"
	}
	if clamavRes.Result == "error" || yaraRes.Result == "error" {
		return "failed"
	}
	return "clean"
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			outFile.Close()
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}

func findUnityWebGLRoot(baseDir string, logger *zap.SugaredLogger) ([]string, error) {
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

func findStaticSiteRoot(baseDir string, logger *zap.SugaredLogger) ([]string, error) {
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