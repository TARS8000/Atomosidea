package storage

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIOClient holds the MinIO client instance
var MinIOClient *minio.Client

// ConnectMinIO initializes the MinIO client
func ConnectMinIO(cfg config.Config) error {
	var err error
	endpoint := cfg.MinIO.Endpoint
	accessKeyID := cfg.MinIO.AccessKeyID
	secretAccessKey := cfg.MinIO.SecretAccessKey
	useSSL := cfg.MinIO.UseSSL

	err = config.Retry(5, 2*time.Second, func() error {
		MinIOClient, err = minio.New(endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
			Secure: useSSL,
		})
		if err != nil {
			return fmt.Errorf("failed to create minio client: %w", err)
		}
		// Check if the client is connected
		_, err = MinIOClient.ListBuckets(context.Background())
		if err != nil {
			return fmt.Errorf("failed to list buckets, minio not ready: %w", err)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("unable to connect to MinIO after retries: %w", err)
	}

	log.Println("Successfully connected to MinIO.")
	return nil
}

// UploadFile uploads a file to MinIO
func UploadFile(ctx context.Context, bucketName, objectName, filePath, contentType string) (minio.UploadInfo, error) {
	info, err := MinIOClient.FPutObject(ctx, bucketName, objectName, filePath, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("failed to upload file to MinIO: %w", err)
	}
	return info, nil
}

// DownloadFile downloads a file from MinIO
func DownloadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	err := MinIOClient.FGetObject(ctx, bucketName, objectName, filePath, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to download file from MinIO: %w", err)
	}
	return nil
}
