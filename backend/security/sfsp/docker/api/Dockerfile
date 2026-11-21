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

# Build the sfsp-api application
RUN CGO_ENABLED=0 GOOS=linux go build -o /usr/local/bin/sfsp-api ./cmd/sfsp-api

FROM alpine:latest

WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /usr/local/bin/sfsp-api /usr/local/bin/sfsp-api

# 非 root ユーザー（uid/gid 10001）で実行する。ポートは 8080（1024 以上）なので特権は不要
RUN addgroup -S -g 10001 sfsp && adduser -S -D -H -u 10001 -G sfsp -s /sbin/nologin sfsp

USER 10001:10001

# Expose the port the API listens on
EXPOSE 8080

# Run the sfsp-api application
ENTRYPOINT ["/usr/local/bin/sfsp-api"]
