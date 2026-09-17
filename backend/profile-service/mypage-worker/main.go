package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	dbAuth    *pgxpool.Pool
	dbApp     *pgxpool.Pool
	jwtSecret []byte
)

type Video struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ThumbnailURL string    `json:"thumbnail_url"`
	UploaderID   string    `json:"uploader_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type Game struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	ThumbnailURL string    `json:"thumbnail_url"`
	UploaderID   string    `json:"uploader_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type StaticSite struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	UploaderID  string    `json:"uploader_id"`
	CreatedAt   time.Time `json:"created_at"`
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is missing"})
			return
		}
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return jwtSecret, nil
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			var userIDStr string
			if idStr, ok := claims["sub"].(string); ok {
				userIDStr = idStr
			} else if idStr, ok := claims["userID"].(string); ok {
				userIDStr = idStr
			} else if idStr, ok := claims["user_id"].(string); ok {
				userIDStr = idStr
			} else {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid user ID in token"})
				return
			}

			if userIDStr == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Empty user ID in token"})
				return
			}

			c.Set("userID", userIDStr)
			c.Next()
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		}
	}
}

func main() {
	var err error
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	authDbUrl := os.Getenv("AUTH_DATABASE_URL")
	appDbUrl := os.Getenv("APP_DATABASE_URL")

	dbAuth, err = pgxpool.New(context.Background(), authDbUrl)
	if err != nil {
		log.Fatalf("Unable to connect to auth database: %v\n", err)
	}
	defer dbAuth.Close()

	dbApp, err = pgxpool.New(context.Background(), appDbUrl)
	if err != nil {
		log.Fatalf("Unable to connect to app database: %v\n", err)
	}
	defer dbApp.Close()

	r := gin.Default()
	api := r.Group("/api/my")
	api.Use(authMiddleware())
	{
		api.GET("/videos", myVideosHandler)
		api.GET("/games", myGamesHandler)
		api.GET("/static-sites", myStaticSitesHandler)
	}

	log.Println("INFO: MyPage service starting on port 8083")
	if err := r.Run(":8083"); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

func myVideosHandler(c *gin.Context) {
	uploaderID := c.GetString("userID")
	rows, err := dbApp.Query(context.Background(), "SELECT id, title, COALESCE(thumbnail_path, ''), uploader_id, created_at FROM videos WHERE uploader_id = $1 ORDER BY created_at DESC", uploaderID)
	if err != nil {
		log.Printf("ERROR: Database query failed in myVideosHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve videos"})
		return
	}
	defer rows.Close()

	videos := []Video{}
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.Title, &v.ThumbnailURL, &v.UploaderID, &v.CreatedAt); err != nil {
			log.Printf("Error scanning video row: %v", err)
			continue
		}
		videos = append(videos, v)
	}
	c.JSON(http.StatusOK, videos)
}

func myGamesHandler(c *gin.Context) {
	userID := c.GetString("userID")

	rows, err := dbApp.Query(context.Background(),
		`SELECT id, title, COALESCE(thumbnail_url, ''), user_id, created_at
        FROM games
        WHERE user_id = $1
        ORDER BY created_at DESC`, userID)
	if err != nil {
		log.Printf("ERROR: Database query failed in myGamesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error on listing user's games"})
		return
	}
	defer rows.Close()

	games := []Game{}
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.Title, &g.ThumbnailURL, &g.UploaderID, &g.CreatedAt); err != nil {
			log.Printf("Error scanning game row: %v", err)
			continue
		}
		games = append(games, g)
	}
	c.JSON(http.StatusOK, games)
}

func myStaticSitesHandler(c *gin.Context) {
	userID := c.GetString("userID")

	rows, err := dbApp.Query(context.Background(),
		`SELECT id, title, COALESCE(description, ''), user_id, created_at
        FROM static_sites
        WHERE user_id = $1
        ORDER BY created_at DESC`, userID)
	if err != nil {
		log.Printf("ERROR: Database query failed in myStaticSitesHandler: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error on listing user's static sites"})
		return
	}
	defer rows.Close()

	sites := []StaticSite{}
	for rows.Next() {
		var s StaticSite
		if err := rows.Scan(&s.ID, &s.Title, &s.Description, &s.UploaderID, &s.CreatedAt); err != nil {
			log.Printf("Error scanning static site row: %v", err)
			continue
		}
		sites = append(sites, s)
	}
	c.JSON(http.StatusOK, sites)
}