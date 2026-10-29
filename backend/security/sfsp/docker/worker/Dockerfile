# syntax=docker/dockerfile:1.4
# Use Go 1.25 as required by dependencies
FROM golang:1.25-alpine AS builder

WORKDIR /app

# git をインストール
RUN apk add --no-cache git

# プロジェクト全体のソースコードをコピー
COPY . .

# security ディレクトリに移動して go.mod を処理
WORKDIR /app/backend/security/sfsp

# 公式プロキシを優先し、失敗したら goproxy.io、最後に直接取得する（"|" 区切りはどのエラーでも次に切り替える）
ENV GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"

# パッケージをダウンロード (go.sum に基づいてダウンロードされる)
RUN go mod download

# Build the sfsp-worker application
RUN CGO_ENABLED=0 GOOS=linux go build -o /usr/local/bin/sfsp-worker ./cmd/sfsp-worker

FROM alpine:latest

WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /usr/local/bin/sfsp-worker /usr/local/bin/sfsp-worker

# ワーカーは Docker SDK（sfsp-docker-proxy 経由）でサンドボックスを起動するだけなので docker-cli は不要。
# YARA とルールはサンドボックス側のイメージ（sfsp-yara-client）にあり、ワーカーは読まない

# 非 root ユーザー（uid/gid 10001）で実行する。Docker ソケットは持たず、sfsp-docker-proxy 経由でサンドボックスを起動するので root は不要。
# スキャン用ディレクトリ（名前付きボリューム sfsp_temp_scan のマウント先）を、ボリュームが最初に作られる前にこのユーザー所有で用意しておく
# （空の名前付きボリュームはイメージ側のディレクトリの所有者を引き継ぐ。既存ボリュームは引き継がないので compose の sfsp-scan-volume-init で chown する）。
# 0755: サンドボックスのスキャナーは CapDrop: ALL で動くため、「その他」に読み取り・走査権限が必要
RUN addgroup -S -g 10001 sfsp && adduser -S -D -H -u 10001 -G sfsp -s /sbin/nologin sfsp && mkdir -p /tmp/sfsp-scan && chown 10001:10001 /tmp/sfsp-scan && chmod 0755 /tmp/sfsp-scan

USER 10001:10001

# Run the sfsp-worker application
ENTRYPOINT ["/usr/local/bin/sfsp-worker"]
