FROM golang:1.25-alpine AS builder
WORKDIR /app

RUN apk add --no-cache git
COPY . .

WORKDIR /app/static-site-worker
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# --- Final Stage ---
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/static-site-worker/main ./main

# ルート直下の .env をコンテナの実行ディレクトリにコピー
COPY --from=builder /app/.env ./.env

EXPOSE 8080
CMD ["./main"]
