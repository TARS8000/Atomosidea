module profile-service

go 1.25

require (
	github.com/gin-gonic/gin v1.9.1
	github.com/golang-jwt/jwt/v5 v5.0.0
	github.com/google/uuid v1.3.0
	github.com/jackc/pgx/v5 v5.4.3
	github.com/minio/minio-go/v7 v7.0.63
	github.com/atmosidea/shared v0.0.0
	go.uber.org/zap v1.28.0
)

replace github.com/atmosidea/shared => ../../../shared
