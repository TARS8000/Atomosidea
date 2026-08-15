# syntax=docker/dockerfile:1.4
# Use Go 1.25 as required by dependencies
FROM golang:1.25-alpine AS builder

WORKDIR /app

# git をインストール
RUN apk add --no-cache git

# プロジェクト全体のソースコードをコピー
COPY . .

# security ディレクトリに移動して go.mod を処理
WORKDIR /app/security

# go.sum が無い時だけ go mod tidy を実行
RUN test -f go.sum || go mod tidy

# パッケージをダウンロード (go.sum に基づいてダウンロードされる)
RUN go mod download

# Build the sfsp-worker application
RUN CGO_ENABLED=0 GOOS=linux go build -o /usr/local/bin/sfsp-worker ./cmd/sfsp-worker

FROM alpine:latest

# Install Docker client and other necessary tools
RUN apk add --no-cache docker-cli yara

WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /usr/local/bin/sfsp-worker /usr/local/bin/sfsp-worker

# The worker needs access to yara rules, which will be mounted via a volume
# in docker-compose.yml. This just ensures the directory exists.
RUN mkdir -p /etc/yara-rules

# Run the sfsp-worker application
ENTRYPOINT ["/usr/local/bin/sfsp-worker"]