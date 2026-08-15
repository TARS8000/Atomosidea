FROM golang:1.25-alpine AS builder
WORKDIR /app

RUN apk add --no-cache git
COPY . .

WORKDIR /app/upload-service
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# --- Final Stage ---
FROM alpine:latest
# ffmpeg をインストールして動画変換（HLSエンコード）を可能にします
RUN apk --no-cache add ca-certificates tzdata ffmpeg

WORKDIR /app
COPY --from=builder /app/upload-service/main ./main

# Goアプリ起動時の「open .env: no such file or directory」クラッシュを防ぐため空ファイルを作成
RUN touch .env

EXPOSE 8080
CMD ["./main"]