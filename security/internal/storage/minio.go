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

// EnsureBuckets checks if specified buckets exist and creates them if they don't.
func EnsureBuckets(ctx context.Context, cfg config.Config) error {
	buckets := []string{
		cfg.MinIO.Buckets.RawFiles,
		cfg.MinIO.Buckets.CleanFiles,
		cfg.MinIO.Buckets.Quarantine,
	}

	for _, bucket := range buckets {
		exists, err := MinIOClient.BucketExists(ctx, bucket)
		if err != nil {
			return fmt.Errorf("failed to check existence of bucket %s: %w", bucket, err)
		}

		if !exists {
			log.Printf("Bucket %s does not exist, creating...", bucket)
			if err := MinIOClient.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
				return fmt.Errorf("failed to create bucket %s: %w", bucket, err)
			}
			log.Printf("Bucket %s created successfully.", bucket)

			// Set policy for clean-files to be public readable
			if bucket == cfg.MinIO.Buckets.CleanFiles {
				policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + bucket + `/*"]}]}`
				if err = MinIOClient.SetBucketPolicy(ctx, bucket, policy); err != nil {
					return fmt.Errorf("failed to set public policy for bucket %s: %w", bucket, err)
				}
				log.Printf("Public read policy set for bucket %s.", bucket)
			}
		}
	}
	return nil
}

// CopyObject copies an object from a source bucket to a destination bucket.
func CopyObject(ctx context.Context, destBucket, destObject, srcBucket, srcObject string) (minio.UploadInfo, error) {
	srcOpts := minio.CopySrcOptions{
		Bucket: srcBucket,
		Object: srcObject,
	}
	destOpts := minio.CopyDestOptions{
		Bucket: destBucket,
		Object: destObject,
	}
	uploadInfo, err := MinIOClient.CopyObject(ctx, destOpts, srcOpts)
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("failed to copy object from %s/%s to %s/%s: %w", srcBucket, srcObject, destBucket, destObject, err)
	}
	return uploadInfo, nil
}

// DeleteObject deletes an object from the specified bucket.
func DeleteObject(ctx context.Context, bucketName, objectName string) error {
	err := MinIOClient.RemoveObject(ctx, bucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete object %s from bucket %s: %w", objectName, bucketName, err)
	}
	return nil
}