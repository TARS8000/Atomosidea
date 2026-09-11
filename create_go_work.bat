@echo off
setlocal

echo ========================================
echo Creating go.work
echo ========================================

del go.work 2>nul
del go.work.sum 2>nul

docker run --rm -v "%CD%:/app" -w /app golang:1.25-alpine sh -c "go work init ./backend/auth/auth-worker/auth-service ./backend/auth/auth-worker/profile-service ./backend/auth/mypage-worker ./backend/game-service/game-upload-api ./backend/game-service/game-worker ./backend/security/sfsp ./backend/shared ./backend/static-site-service/static-site-upload-api ./backend/static-site-service/static-site-worker ./backend/video-service/video-upload-api ./backend/video-service/video-worker"

if errorlevel 1 (
    echo ERROR: Failed to create go.work
    pause
    exit /b 1
)

type go.work

pause