@echo off
setlocal

echo ========================================
echo Creating go.work
echo ========================================

del go.work 2>nul
del go.work.sum 2>nul

docker run --rm -v "%CD%:/app" -w /app golang:1.25-alpine sh -c "go work init ./auth-service ./game-upload-api ./game-worker ./mypage-service ./profile-service ./security ./shared ./static-site-upload-api ./static-site-worker ./stream-service ./upload-service"

if errorlevel 1 (
    echo ERROR: Failed to create go.work
    pause
    exit /b 1
)

type go.work

pause