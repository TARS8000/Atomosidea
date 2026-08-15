# syntax=docker/dockerfile:1.4
# Use Go 1.25 as required by dependencies
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum to download dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the application source code
COPY . .

# Build the sfsp-api application
# 'go mod tidy' is removed to avoid inconsistencies.
# The local go.sum is expected to be correct.
RUN CGO_ENABLED=0 GOOS=linux go build -o /usr/local/bin/sfsp-api ./cmd/sfsp-api

FROM alpine:latest

WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /usr/local/bin/sfsp-api /usr/local/bin/sfsp-api

# Expose the port the API listens on
EXPOSE 8080

# Run the sfsp-api application
ENTRYPOINT ["/usr/local/bin/sfsp-api"]
