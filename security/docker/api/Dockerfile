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

# Build the sfsp-api application
RUN CGO_ENABLED=0 GOOS=linux go build -o /usr/local/bin/sfsp-api ./cmd/sfsp-api

FROM alpine:latest

WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /usr/local/bin/sfsp-api /usr/local/bin/sfsp-api

# Expose the port the API listens on
EXPOSE 8080

# Run the sfsp-api application
ENTRYPOINT ["/usr/local/bin/sfsp-api"]