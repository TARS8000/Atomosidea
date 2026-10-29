FROM golang:1.25-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY . .
WORKDIR /app/backend/team-service/team-worker
ENV GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o main .
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/backend/team-service/team-worker/main ./main
EXPOSE 8080
CMD ["./main"]
