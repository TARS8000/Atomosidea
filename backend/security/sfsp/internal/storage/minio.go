package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	RawMinIOClient   *minio.Client
	CleanMinIOClient *minio.Client
	rawBucketName    string
)

// ConnectMinIO initializes both Raw and Clean MinIO clients
func ConnectMinIO(cfg config.Config) error {
	var err error
	rawBucketName = cfg.MinIO.Buckets.RawFiles

	// 1. Raw MinIO Client Initialization
	err = config.Retry(5, 2*time.Second, func() error {
		RawMinIOClient, err = minio.New(cfg.MinIO.RawEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.MinIO.RawAccessKeyID, cfg.MinIO.RawSecretAccessKey, ""),
			Secure: cfg.MinIO.UseSSL,
		})
		if err != nil {
			return fmt.Errorf("failed to create raw minio client: %w", err)
		}
		_, err = RawMinIOClient.ListBuckets(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list buckets on raw minio: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("unable to connect to Raw MinIO after retries: %w", err)
	}

	// 2. Clean MinIO Client Initialization
	err = config.Retry(5, 2*time.Second, func() error {
		CleanMinIOClient, err = minio.New(cfg.MinIO.CleanEndpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(cfg.MinIO.CleanAccessKeyID, cfg.MinIO.CleanSecretAccessKey, ""),
			Secure: cfg.MinIO.UseSSL,
		})
		if err != nil {
			return fmt.Errorf("failed to create clean minio client: %w", err)
		}
		_, err = CleanMinIOClient.ListBuckets(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list buckets on clean minio: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("unable to connect to Clean MinIO after retries: %w", err)
	}

	log.Println("Successfully connected to both Raw and Clean MinIO instances.")
	return nil
}

// UploadFile uploads a file to the appropriate MinIO instance based on bucketName
func UploadFile(ctx context.Context, bucketName, objectName, filePath, contentType string) (minio.UploadInfo, error) {
	client := CleanMinIOClient
	if bucketName == rawBucketName {
		client = RawMinIOClient
	}

	info, err := client.FPutObject(ctx, bucketName, objectName, filePath, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("failed to upload file to MinIO: %w", err)
	}
	return info, nil
}

// streamPartSize bounds the memory used by PutObject when the size is unknown (-1).
// Without it minio-go picks a part size of ~576 MiB for unknown-length uploads.
const streamPartSize = 16 * 1024 * 1024

// UploadStream streams r to MinIO without buffering the whole object.
// Pass size = -1 when the length is unknown (memory use is bounded by streamPartSize).
func UploadStream(ctx context.Context, bucketName, objectName string, r io.Reader, size int64, contentType string) (minio.UploadInfo, error) {
	client := CleanMinIOClient
	if bucketName == rawBucketName {
		client = RawMinIOClient
	}

	opts := minio.PutObjectOptions{ContentType: contentType}
	if size < 0 {
		opts.PartSize = streamPartSize
	}
	info, err := client.PutObject(ctx, bucketName, objectName, r, size, opts)
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("failed to stream object to MinIO: %w", err)
	}
	return info, nil
}

// DownloadFile downloads a file from Raw MinIO
func DownloadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	err := RawMinIOClient.FGetObject(ctx, bucketName, objectName, filePath, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to download file from Raw MinIO: %w", err)
	}
	return nil
}

// DeleteObject deletes an object from either Raw or Clean MinIO depending on the target bucket
func DeleteObject(ctx context.Context, bucketName, objectName string) error {
	client := CleanMinIOClient
	if bucketName == rawBucketName {
		client = RawMinIOClient
	}

	err := client.RemoveObject(ctx, bucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object %s from bucket %s: %w", objectName, bucketName, err)
	}
	return nil
}

// IsNotFound reports whether err is a MinIO "object/bucket does not exist" error.
func IsNotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" || resp.StatusCode == 404
	}
	return false
}
