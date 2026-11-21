# ビルドステージ
FROM golang:1.25-alpine AS builder

# Git や証明書など依存ライブラリの取得に必要なツールをインストール
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# go.mod と go.sum をコピー（依存のダウンロードをレイヤーキャッシュし、go.sum で整合性を検証する）
COPY go.mod go.sum ./

ENV CGO_ENABLED=0

# 依存関係をダウンロード
RUN go mod download

# ソースコードをコピー
COPY . .

# アプリケーションをビルド
RUN GOOS=linux go build -o /monitoring-backend .

# 実行ステージ
FROM alpine:latest

# TLS通信等に必要なルート証明書をコピー
RUN apk add --no-cache ca-certificates

WORKDIR /root/

# ビルドステージからコンパイル済みバイナリをコピー
COPY --from=builder /monitoring-backend .

# 実行可能ファイルを指定
CMD ["./monitoring-backend"]