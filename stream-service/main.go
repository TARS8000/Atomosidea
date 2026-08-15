package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	db *pgxpool.Pool
)

type Video struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	Filename          string    `json:"filename"`
	ThumbnailPath     string    `json:"thumbnail_path"`
	UploaderID        string    `json:"uploader_id"`
	UploaderName      string    `json:"uploader_name"`
	Status            string    `json:"status"`
	ProcessingDetails string    `json:"processing_details"`
	CreatedAt         time.Time `json:"created_at"`
}

// 💡 動画更新時のリクエストボディ構造体
type UpdateVideoInput struct {
	Title       string `json:"title" binding:"required"`
	Description string `json:"description"`
}

func main() {
	var err error
	databaseUrl := os.Getenv("DATABASE_URL")

	db, err = pgxpool.New(context.Background(), databaseUrl)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer db.Close()

	r := gin.Default()
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{"http://localhost:3001"} // フロントエンドのURL
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Authorization"}
	config.AllowCredentials = true
	r.Use(cors.New(config))

	api := r.Group("/api/videos")
	{
		api.GET("", listVideosHandler)
		api.GET("/:id", videoDetailsHandler)
		api.PUT("/:id", updateVideoHandler) // 👈 PUTハンドラーを追加
	}

	log.Println("Stream service (metadata only) starting on port 8081")
	if err := r.Run(":8081"); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

// 動画一覧・検索
func listVideosHandler(c *gin.Context) {
	searchTerm := c.Query("q")
	var rows pgx.Rows
	var err error

	if searchTerm != "" {
		// 💡 COALESCE(description, '') で NULL 対策を追加
		rows, err = db.Query(context.Background(),
			`SELECT id, title, COALESCE(description, '') as description, filename, COALESCE(thumbnail_path, ''), uploader_id, 'Unknown User' as username, status, COALESCE(processing_details, '') as processing_details, created_at
           FROM videos
           WHERE title ILIKE $1
           ORDER BY created_at DESC LIMIT 50`, "%"+searchTerm+"%")
	} else {
		rows, err = db.Query(context.Background(),
			`SELECT id, title, COALESCE(description, '') as description, filename, COALESCE(thumbnail_path, ''), uploader_id, 'Unknown User' as username, status, COALESCE(processing_details, '') as processing_details, created_at
           FROM videos
           ORDER BY created_at DESC LIMIT 50`)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database query failed"})
		return
	}
	defer rows.Close()

	videos := []Video{}
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.Title, &v.Description, &v.Filename, &v.ThumbnailPath, &v.UploaderID, &v.UploaderName, &v.Status, &v.ProcessingDetails, &v.CreatedAt); err != nil {
			log.Printf("Error scanning video row: %v", err)
			continue
		}
		videos = append(videos, v)
	}
	c.JSON(http.StatusOK, videos)
}

// 動画詳細取得
func videoDetailsHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	var v Video
	// 💡 COALESCE(description, '') で NULL 対策を追加
	err := db.QueryRow(context.Background(),
		`SELECT id, title, COALESCE(description, '') as description, filename, COALESCE(thumbnail_path, ''), uploader_id, 'Unknown User' as username, status, COALESCE(processing_details, '') as processing_details, created_at
        FROM videos
        WHERE id = $1`, videoID).Scan(&v.ID, &v.Title, &v.Description, &v.Filename, &v.ThumbnailPath, &v.UploaderID, &v.UploaderName, &v.Status, &v.ProcessingDetails, &v.CreatedAt)
	if err != nil {
		log.Printf("ERROR: Database query failed in videoDetailsHandler for ID %s: %v", videoID, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}
	c.JSON(http.StatusOK, v)
}

// 💡 動画更新（PUT /api/videos/:id）
func updateVideoHandler(c *gin.Context) {
	videoID := c.Param("id")
	if videoID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid video ID"})
		return
	}

	var input UpdateVideoInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	// データベースのタイトルと説明文、更新日時（updated_at）を更新
	commandTag, err := db.Exec(context.Background(),
		`UPDATE videos
         SET title = $1, description = $2, updated_at = NOW()
         WHERE id = $3`,
		input.Title, input.Description, videoID)

	if err != nil {
		log.Printf("ERROR: Failed to update video ID %s: %v", videoID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update video"})
		return
	}

	// 対象のレコードが存在しなかった場合
	if commandTag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Video not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Video updated successfully"})
}