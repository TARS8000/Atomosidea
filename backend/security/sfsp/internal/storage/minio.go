// Package storage は SFSP が使う2つの MinIO（スキャン前のファイルを置く Raw と、
// スキャン後のファイルを置く Clean）へのアクセスを提供する。
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

// パッケージ全体で共有する MinIO クライアント。ConnectMinIO で初期化される。
// rawBucketName は、バケット名からどちらのクライアントを使うかを判定するために保持する。
var (
	RawMinIOClient   *minio.Client
	CleanMinIOClient *minio.Client
	rawBucketName    string
)

// ConnectMinIO は Raw / Clean 両方の MinIO クライアントを初期化する。
// それぞれ ListBuckets が成功するまでリトライし、認証情報と疎通を確認する（バケットの作成は行わない）。
func ConnectMinIO(cfg config.Config) error {
	var err error
	rawBucketName = cfg.MinIO.Buckets.RawFiles

	// 1. Raw MinIO クライアントの初期化
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

	// 2. Clean MinIO クライアントの初期化
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

// UploadFile はローカルの filePath を bucketName に応じた MinIO にアップロードする。
// bucketName が raw バケットなら Raw MinIO、それ以外（clean / quarantine など）は Clean MinIO を使う。
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

// streamPartSize はサイズ不明（-1）のときに PutObject が使うメモリ量を抑えるためのパートサイズ。
// 指定しないと、minio-go はサイズ不明のアップロードに約 576 MiB のパートサイズを選ぶ。
const streamPartSize = 16 * 1024 * 1024

// UploadStream はオブジェクト全体をバッファせずに r を MinIO へストリーミングでアップロードする。
// 長さが不明な場合は size = -1 を渡す（メモリ使用量は streamPartSize で抑えられる）。
// 使用するクライアントの選び方は UploadFile と同じ。
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

// DownloadFile は Raw MinIO からオブジェクトをローカルの filePath にダウンロードする。
// bucketName にかかわらず常に Raw MinIO を使う。
func DownloadFile(ctx context.Context, bucketName, objectName, filePath string) error {
	err := RawMinIOClient.FGetObject(ctx, bucketName, objectName, filePath, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("failed to download file from Raw MinIO: %w", err)
	}
	return nil
}

// DeleteObject は対象バケットに応じて Raw または Clean MinIO からオブジェクトを削除する。
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

// IsNotFound は err が MinIO の「オブジェクトまたはバケットが存在しない」エラーかどうかを返す。
func IsNotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" || resp.StatusCode == 404
	}
	return false
}
