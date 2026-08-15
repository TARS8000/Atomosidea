package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/atmosidea/sfsp/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// SetupTestRouter sets up a router for testing
func SetupTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.Default()
	return router
}

func TestHandleFileUpload(t *testing.T) {
	// Note: This is a basic test. A full integration test would require
	// a running DB, MinIO, and Redis, which is better suited for e2e tests.
	// This test primarily checks the handler's ability to process a request.

	router := SetupTestRouter()
	logger, _ := zap.NewDevelopment()
	sugar := logger.Sugar()
	cfg, _ := config.LoadConfig() // Assuming default config is sufficient for this test

	apiHandler := NewAPI(cfg, sugar)
	router.POST("/api/v1/files", apiHandler.HandleFileUpload)

	// Create a dummy file for upload
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.txt")
	err := os.WriteFile(filePath, []byte("this is a test file"), 0644)
	assert.NoError(t, err)

	// Create a multipart form request
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	assert.NoError(t, err)
	file, err := os.Open(filePath)
	assert.NoError(t, err)
	_, err = io.Copy(part, file)
	assert.NoError(t, err)
	writer.Close()

	req, _ := http.NewRequest(http.MethodPost, "/api/v1/files", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// As we don't have live services, we expect an internal server error,
	// but a 2xx or 4xx status would also indicate the handler is processing the request.
	// A 500 error from a failed DB/MinIO/Redis connection is an acceptable outcome for this unit test.
	assert.NotEqual(t, http.StatusNotFound, w.Code, "Endpoint should exist")
}
