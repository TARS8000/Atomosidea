package worker

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atmosidea/sfsp/internal/database"
	"github.com/atmosidea/sfsp/internal/sandbox"
	"github.com/atmosidea/sfsp/internal/scanner"
	"github.com/atmosidea/sfsp/internal/storage"
	"github.com/atmosidea/shared/config" // shared/config 繧偵う繝ｳ繝昴・繝・
	"github.com/atmosidea/shared/event"  // shared/event 繧偵う繝ｳ繝昴・繝・
	"github.com/atmosidea/shared/model"  // shared/model 繧偵う繝ｳ繝昴・繝・
	"github.com/atmosidea/shared/queue"  // shared/queue 繧偵う繝ｳ繝昴・繝・
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// StartWorker starts the SFSP worker process
func StartWorker(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger) { // cfg 縺ｮ蝙九ｒ shared/config.Config 縺ｫ螟画峩
	sb, err := sandbox.NewDockerSandbox(logger)
	if err != nil {
		logger.Fatalf("Failed to create Docker sandbox: %v", err)
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

func processJob(ctx context.Context, cfg config.Config, logger *zap.SugaredLogger, sb sandbox.Sandbox, jobID uuid.UUID) { // cfg 縺ｮ蝙九ｒ shared/config.Config 縺ｫ螟画峩
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

	tempDir, err := os.MkdirTemp("", "sfsp-scan-*")
	if err != nil {
		logger.Errorf("Failed to create temp directory for job %s: %v", jobID, err)
		updateJobStatus(ctx, jobID, "failed", logger)
		return
	}
	defer os.RemoveAll(tempDir)

	downloadPath := filepath.Join(tempDir, file.Filename)
	bucketName := cfg.MinIO.Buckets.RawFiles
	objectName := fmt.Sprintf("%s/%s", file.SHA256, file.Filename)

	logger.Infof("Downloading file %s from MinIO for job %s", objectName, jobID)
	if err := storage.DownloadFile(ctx, bucketName, objectName, downloadPath); err != nil {
		logger.Errorf("Failed to download file %s for job %s: %v", objectName, jobID, err)
		updateJobStatus(ctx, jobID, "failed", logger)
		return
	}

	scanPath := downloadPath
	jobStatus := "completed" // Default to completed, will be overridden by specific logic

	if file.FileType == "zip" {
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
		} else if !isValidUnityWebGL(unzipDir) {
			logger.Warnf("ZIP file for job %s is not a valid Unity WebGL build.", jobID)
			jobStatus = "invalid"
		} else {
			logger.Infof("Unity WebGL structure detected for job %s", jobID)
			scanPath = unzipDir
		}
	}

	var clamavResult, yaraResult scanner.ScanResult
	if jobStatus != "invalid" {
		clamAVScanner := scanner.NewClamAVScanner(cfg.Scanner.ClamAVImage, logger)
		yaraScanner := scanner.NewYARAScanner(cfg.Scanner.YARAImage, cfg.Scanner.YARARulesPath, logger)

		clamavResult, err = clamAVScanner.Scan(ctx, sb, scanPath)
		if err != nil {
			logger.Errorf("ClamAV scan failed for job %s: %v", jobID, err)
			saveScanResult(ctx, jobID, "clamav", "error", err.Error(), nil, logger)
		} else {
			saveScanResult(ctx, jobID, "clamav", clamavResult.Result, clamavResult.Details, clamavResult.RawOutput, logger)
		}

		yaraResult, err = yaraScanner.Scan(ctx, sb, scanPath)
		if err != nil {
			logger.Errorf("YARA scan failed for job %s: %v", jobID, err)
			saveScanResult(ctx, jobID, "yara", "error", err.Error(), nil, logger)
		} else {
			saveScanResult(ctx, jobID, "yara", yaraResult.Result, yaraResult.Details, yaraResult.RawOutput, logger)
		}
		jobStatus = determineOverallStatus(clamavResult, yaraResult)
	}

	updateJobStatus(ctx, jobID, jobStatus, logger)

	// --- Publish Completion Event ---
	completionEvent := event.ScanCompletionEvent{ // event.ScanCompletionEvent 繧剃ｽｿ逕ｨ
		JobID:       jobID,
		FileID:      file.ID,
		TargetService: file.TargetService, // Populate TargetService from the file metadata
		FinalStatus: jobStatus,
		ScannedAt:   time.Now(),
		SHA256:      file.SHA256,
		Filename:    file.Filename,
	}
	if err := queue.EnqueueScanCompletionEvent(ctx, completionEvent); err != nil { // Use the new EnqueueScanCompletionEvent
		logger.Errorf("Failed to publish completion event for job %s: %v", jobID, err)
	} else {
		logger.Infof("Published completion event for job %s with status %s to target service %s", jobID, jobStatus, file.TargetService)
	}

	logger.Infof("Finished processing job: %s with overall status: %s", jobID, jobStatus)
}

func updateJobStatus(ctx context.Context, jobID uuid.UUID, status string, logger *zap.SugaredLogger) error {
	_, err := database.DB.Exec(ctx, "UPDATE scan_jobs SET status = $1, updated_at = NOW() WHERE id = $2", status, jobID)
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

func isValidUnityWebGL(dirPath string) bool {
	indexPath := filepath.Join(dirPath, "index.html")
	buildDirPath := filepath.Join(dirPath, "Build")
	templateDataDirPath := filepath.Join(dirPath, "TemplateData")

	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		return false
	}
	if info, err := os.Stat(buildDirPath); os.IsNotExist(err) || !info.IsDir() {
		return false
	}
	if info, err := os.Stat(templateDataDirPath); os.IsNotExist(err) || !info.IsDir() {
		return false
	}

	buildContents, err := os.ReadDir(buildDirPath)
	if err != nil {
		return false
	}

	var hasWasm, hasJs, hasData bool
	for _, item := range buildContents {
		if strings.HasSuffix(item.Name(), ".wasm") {
			hasWasm = true
		}
		if strings.HasSuffix(item.Name(), ".js") {
			hasJs = true
		}
		if strings.HasSuffix(item.Name(), ".data") {
			hasData = true
		}
	}

	return hasWasm && hasJs && hasData
}
