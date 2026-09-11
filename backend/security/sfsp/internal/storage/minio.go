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

// DownloadFile downloads a file from Raw MinIO
func DownloadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	err := RawMinIOClient.FGetObject(ctx, bucketName, objectName, filePath, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to download file from Raw MinIO: %w", err)
	}
	return nil
}

// ListObjects lists objects from either Raw or Clean MinIO depending on the target bucket
func ListObjects(ctx context.Context, bucketName string) <-chan minio.ObjectInfo {
	client := CleanMinIOClient
	if bucketName == rawBucketName {
		client = RawMinIOClient
	}
	return client.ListObjects(ctx, bucketName, minio.ListObjectsOptions{Recursive: true})
}

// EnsureBuckets checks and creates buckets on their respective MinIO instances
func EnsureBuckets(ctx context.Context, cfg config.Config) error {
	// 1. Raw Bucket Verification
	rawBucket := cfg.MinIO.Buckets.RawFiles
	rawExists, err := RawMinIOClient.BucketExists(ctx, rawBucket)
	if err != nil {
		return fmt.Errorf("failed to check existence of raw bucket %s: %w", rawBucket, err)
	}
	if !rawExists {
		log.Printf("Bucket %s does not exist on Raw MinIO, creating...", rawBucket)
		if err := RawMinIOClient.MakeBucket(ctx, rawBucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("failed to create bucket %s on Raw MinIO: %w", rawBucket, err)
		}
		log.Printf("Bucket %s created successfully on Raw MinIO.", rawBucket)
	}

	// 2. Clean Buckets Verification
	cleanBuckets := []string{
		cfg.MinIO.Buckets.CleanFiles,
		cfg.MinIO.Buckets.Quarantine,
	}

	for _, bucket := range cleanBuckets {
		exists, err := CleanMinIOClient.BucketExists(ctx, bucket)
		if err != nil {
			return fmt.Errorf("failed to check existence of clean bucket %s: %w", bucket, err)
		}

		if !exists {
			log.Printf("Bucket %s does not exist on Clean MinIO, creating...", bucket)
			if err := CleanMinIOClient.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
				return fmt.Errorf("failed to create bucket %s on Clean MinIO: %w", bucket, err)
			}
			log.Printf("Bucket %s created successfully on Clean MinIO.", bucket)

			if bucket == cfg.MinIO.Buckets.CleanFiles {
				policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + bucket + `/*"]}]}`
				if err = CleanMinIOClient.SetBucketPolicy(ctx, bucket, policy); err != nil {
					return fmt.Errorf("failed to set public policy for bucket %s: %w", bucket, err)
				}
				log.Printf("Public read policy set for bucket %s on Clean MinIO.", bucket)
			}
		}
	}
	return nil
}

// CopyObject copies an object within Clean MinIO
func CopyObject(ctx context.Context, destBucket, destObject, srcBucket, srcObject string) (minio.UploadInfo, error) {
	srcOpts := minio.CopySrcOptions{
		Bucket: srcBucket,
		Object: srcObject,
	}
	destOpts := minio.CopyDestOptions{
		Bucket: destBucket,
		Object: destObject,
	}
	uploadInfo, err := CleanMinIOClient.CopyObject(ctx, destOpts, srcOpts)
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("failed to copy object from %s/%s to %s/%s on Clean MinIO: %w", srcBucket, srcObject, destBucket, destObject, err)
	}
	return uploadInfo, nil
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