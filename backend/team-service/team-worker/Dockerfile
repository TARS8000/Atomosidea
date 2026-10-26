FROM golang:1.25-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git
COPY . .
WORKDIR /app/backend/team-service/team-worker
ENV GOPROXY=https://goproxy.io,direct
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/backend/team-service/team-worker/main ./main
COPY --from=builder /app/.env ./.env
EXPOSE 8080
CMD ["./main"]