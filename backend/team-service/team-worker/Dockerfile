FROM golang:1.27.4-alpine3.24 AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY . .
WORKDIR /app/backend/team-service/team-worker
ENV GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -o main .
FROM alpine:3.24.4
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/backend/team-service/team-worker/main ./main
EXPOSE 8080
CMD ["./main"]
