FROM golang:1.25-alpine AS builder
WORKDIR /app

RUN apk add --no-cache git
COPY . .

WORKDIR /app/backend/game-service/game-upload-api

# 公式プロキシを優先し、失敗したら goproxy.io、最後に直接取得する（"|" 区切りはどのエラーでも次に切り替える）
ENV GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

# --- Final Stage ---
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/backend/game-service/game-upload-api/main ./main

EXPOSE 8082
CMD ["./main"]
