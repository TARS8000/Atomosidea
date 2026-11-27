package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/minio/minio-go/v7"
)

// Unity WebGL の Brotli 圧縮ビルド（*.framework.js.br / *.wasm.br / *.data.br）は、
// Content-Encoding: br で配信してブラウザに展開させる前提になっている。
// しかしブラウザは HTTPS でしか Brotli を展開しない（Chrome は HTTP だと ERR_CONTENT_DECODING_FAILED で捨てる）ため、
// HTTP で配信している環境ではゲームが読み込めない。
// そこで .br ファイルは Brotli を展開して gzip に詰め直し、Content-Encoding: gzip で保存する。
// gzip は HTTP でも展開されるので、Unity のローダーは同じファイル名（.br）のまま中身を受け取れる。

// transcodeBrotliToGzip は r の Brotli データを展開し、gzip で圧縮し直して w に書く。
// 展開後のサイズが limit を超えたらエラーにする（Brotli bomb 対策）。
func transcodeBrotliToGzip(r io.Reader, w io.Writer, limit int64) error {
	gz := gzip.NewWriter(w)
	n, err := io.CopyN(gz, brotli.NewReader(r), limit+1)
	if err != nil && err != io.EOF {
		return fmt.Errorf("brotli decode failed: %w", err)
	}
	if n > limit {
		return fmt.Errorf("brotli content expands to more than %d bytes", limit)
	}
	return gz.Close()
}

// putBrotliAsGzip は Brotli データ r を gzip に詰め直し、key に Content-Encoding: gzip で保存する。
// 変換結果は一時ファイルに書いてからアップロードする（サイズが事前に分からないため）。
func putBrotliAsGzip(ctx context.Context, r io.Reader, key, contentType string) error {
	tmp, err := os.CreateTemp(tempDir, "br2gz-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if err := transcodeBrotliToGzip(r, tmp, maxEntryBytes); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_, err = gameMinioClient.FPutObject(ctx, gameBucketName, key, tmp.Name(), minio.PutObjectOptions{ContentType: contentType, ContentEncoding: "gzip"})
	return err
}

// uploadBrotliFileAsGzip はローカルの .br ファイルを gzip に詰め直してアップロードする（uploadDirectory から使う）。
func uploadBrotliFileAsGzip(ctx context.Context, localPath, key, contentType string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return putBrotliAsGzip(ctx, f, key, contentType)
}

// migrateBrotliObjects は、この変換を入れる前に Content-Encoding: br のまま保存されたゲームファイルを
// gzip に詰め直す。起動時に 1 回だけ実行する。変換済み（Content-Encoding が br でない）オブジェクトは
// 飛ばすので、何度実行しても結果は変わらない。失敗はログに残すだけで、ワーカーの起動は止めない。
func migrateBrotliObjects(ctx context.Context) {
	converted, failed := 0, 0
	for obj := range gameMinioClient.ListObjects(ctx, gameBucketName, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			logger.Warnf("Brotli migration: failed to list objects: %v", obj.Err)
			return
		}
		if !strings.HasSuffix(obj.Key, ".br") {
			continue
		}
		info, err := gameMinioClient.StatObject(ctx, gameBucketName, obj.Key, minio.StatObjectOptions{})
		if err != nil {
			logger.Warnf("Brotli migration: failed to stat %s: %v", obj.Key, err)
			failed++
			continue
		}
		if info.Metadata.Get("Content-Encoding") != "br" {
			continue
		}
		src, err := gameMinioClient.GetObject(ctx, gameBucketName, obj.Key, minio.GetObjectOptions{})
		if err != nil {
			logger.Warnf("Brotli migration: failed to get %s: %v", obj.Key, err)
			failed++
			continue
		}
		err = putBrotliAsGzip(ctx, src, obj.Key, info.ContentType)
		src.Close()
		if err != nil {
			logger.Warnf("Brotli migration: failed to convert %s: %v", obj.Key, err)
			failed++
			continue
		}
		converted++
	}
	if converted > 0 || failed > 0 {
		logger.Infof("Brotli migration: converted %d object(s) to gzip, %d failed", converted, failed)
	}
}
