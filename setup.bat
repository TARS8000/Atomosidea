@echo off
setlocal enabledelayedexpansion
cls

echo =================================================
echo = Force Updating and Restarting Application...
echo =================================================
echo.

:: 1. Google OAuth2 Key の入力受け取り
echo Step 1: Setting up environment variables...
set "GOOGLE_CLIENT_ID="
set /p GOOGLE_CLIENT_ID="Enter your Google Client ID: "
if "!GOOGLE_CLIENT_ID!"=="" (
    echo [ERROR] Google Client ID cannot be empty. Exiting.
    pause
    goto :eof
)

set "GOOGLE_CLIENT_SECRET="
set /p GOOGLE_CLIENT_SECRET="Enter your Google Client Secret: "
if "!GOOGLE_CLIENT_SECRET!"=="" (
    echo [ERROR] Google Client Secret cannot be empty. Exiting.
    pause
    goto :eof
)

:: 2. .env ファイルの生成（引用符を外して余計なトラブルを防止）
echo Creating .env file...
(
echo # PostgreSQL Database Settings
echo POSTGRES_USER=user
echo POSTGRES_PASSWORD=password
echo POSTGRES_DB=movie_db
echo.
echo # Application Settings
echo JWT_SECRET=your-super-secret-and-long-random-string
echo UPLOAD_DIR=/storage/videos
echo THUMBNAIL_DIR=/storage/thumbnails
echo APP_URL=http://localhost:3001
echo CACHE_BUSTER=1
echo.
echo # Static Site Hosting
echo STATIC_SITE_DOMAIN=localhost
echo.
echo # Admin Registration Code
echo ADMIN_REGISTRATION_CODE=your-secret-admin-code
echo.
echo # Google OAuth2 Settings
echo GOOGLE_CLIENT_ID=!GOOGLE_CLIENT_ID!
echo GOOGLE_CLIENT_SECRET=!GOOGLE_CLIENT_SECRET!
echo.
echo # --- MinIO Settings ---
echo MINIO_USE_SSL=false
echo.
echo # Game Storage
echo GAME_MINIO_ACCESS_KEY_ID=minioadmin
echo GAME_MINIO_SECRET_ACCESS_KEY=minioadmin
echo.
echo # Profile Storage
echo PROFILE_MINIO_ACCESS_KEY_ID=minioadmin
echo PROFILE_MINIO_SECRET_ACCESS_KEY=minioadmin
echo.
echo # Redis Settings
echo REDIS_ADDR=redis:6379
echo REDIS_PASSWORD=
echo REDIS_DB=0
echo.
echo # Static Site Storage
echo STATIC_SITE_MINIO_ACCESS_KEY_ID=minioadmin
echo STATIC_SITE_MINIO_SECRET_ACCESS_KEY=minioadmin
echo.
echo # SFSP MinIO Credentials
echo SFSP_MINIO_ACCESS_KEY_ID=sfspadmin
echo SFSP_MINIO_SECRET_ACCESS_KEY=sfspsecret
) > .env

echo .env file created successfully.
echo.

:: 3. 必要なローカルディレクトリの事前作成
if not exist "storage\videos" mkdir "storage\videos"
if not exist "storage\thumbnails" mkdir "storage\thumbnails"

:: 4. 古いコンテナとネットワークの完全停止・削除
echo Step 2: Stopping and removing old containers...
docker compose down
echo.

:: 5. キャッシュなしでクリーンビルド
echo Step 3: Rebuilding all services without cache...
docker compose build --no-cache

if %errorlevel% neq 0 (
    echo.
    echo #################################################
    echo #         ERROR: Build failed.                  #
    echo #   Please check the logs above for details.    #
    echo #################################################
    pause
    goto :eof
)
echo.

:: 6. サービス起動
echo Step 4: Starting the application...
docker compose up -d

if %errorlevel% neq 0 (
    echo.
    echo #################################################
    echo #      ERROR: Failed to start containers.       #
    echo #   Please check the logs above for details.    #
    echo #################################################
    pause
    goto :eof
)
echo.

echo =================================================
echo = Environment setup complete and Docker services are running!
echo = Access the frontend at http://localhost:3001
echo =================================================
pause
endlocal