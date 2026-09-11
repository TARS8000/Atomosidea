FROM golang:1.25-alpine AS builder
WORKDIR /app

RUN apk add --no-cache git
COPY . .

WORKDIR /app/backend/auth/auth-worker/profile-service

# 日本の高速プロキシを使用するように設定
ENV GOPROXY=https://goproxy.io,direct

RUN go mod download
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# --- Final Stage ---
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/backend/auth/auth-worker/profile-service/main ./main

# ルート直下の .env をコンテナの実行ディレクトリにコピー
COPY --from=builder /app/.env ./.env

EXPOSE 8084
CMD ["./main"]
