FROM golang:1.25-alpine AS builder
WORKDIR /app

RUN apk add --no-cache git
COPY . .

WORKDIR /app/backend/video-service/video-upload-api

# 日本の高速プロキシを使用するように設定
ENV GOPROXY=https://goproxy.io,direct

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# --- Final Stage ---
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata ffmpeg

WORKDIR /app
COPY --from=builder /app/backend/video-service/video-upload-api/main ./main

# ルート直下の .env をコンテナの実行ディレクトリにコピー
COPY --from=builder /app/.env ./.env

EXPOSE 8080
CMD ["./main"]
