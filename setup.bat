@echo off
setlocal enabledelayedexpansion

echo Setting up environment...

:: Prompt for Google OAuth2 keys
set /p GOOGLE_CLIENT_ID="Enter your Google Client ID: "
if "!GOOGLE_CLIENT_ID!"=="" (
    echo Google Client ID cannot be empty. Exiting.
    exit /b 1
)

set /p GOOGLE_CLIENT_SECRET="Enter your Google Client Secret: "
if "!GOOGLE_CLIENT_SECRET!"=="" (
    echo Google Client Secret cannot be empty. Exiting.
    exit /b 1
)

echo Creating/overwriting .env file...
echo Debug: GOOGLE_CLIENT_ID=!GOOGLE_CLIENT_ID!
echo Debug: GOOGLE_CLIENT_SECRET=!GOOGLE_CLIENT_SECRET!

:: Clear existing .env.tmp if it exists
if exist .env.tmp del .env.tmp

:: Write content line by line to a temporary file
echo # PostgreSQL Database Settings > .env.tmp
echo POSTGRES_USER=user >> .env.tmp
echo POSTGRES_PASSWORD=password >> .env.tmp
echo POSTGRES_DB=movie_db >> .env.tmp
echo. >> .env.tmp
echo # Application Settings >> .env.tmp
echo JWT_SECRET=your-super-secret-and-long-random-string >> .env.tmp
echo UPLOAD_DIR=/storage/videos >> .env.tmp
echo THUMBNAIL_DIR=/storage/thumbnails >> .env.tmp
echo APP_URL=http://localhost:3001 >> .env.tmp
echo CACHE_BUSTER=1 >> .env.tmp
echo. >> .env.tmp
echo # Static Site Hosting >> .env.tmp
echo STATIC_SITE_DOMAIN=localhost >> .env.tmp
echo. >> .env.tmp
echo # Admin Registration Code (keep this secret) >> .env.tmp
echo ADMIN_REGISTRATION_CODE="your-secret-admin-code" >> .env.tmp
echo. >> .env.tmp
echo # Google OAuth2 Settings >> .env.tmp
echo GOOGLE_CLIENT_ID="!GOOGLE_CLIENT_ID!" >> .env.tmp
echo GOOGLE_CLIENT_SECRET="!GOOGLE_CLIENT_SECRET!" >> .env.tmp
echo. >> .env.tmp
echo # --- MinIO Settings --- >> .env.tmp
echo MINIO_USE_SSL=false >> .env.tmp
echo. >> .env.tmp
echo # Game Storage (MinIO) >> .env.tmp
echo GAME_MINIO_ACCESS_KEY_ID=minioadmin >> .env.tmp
echo GAME_MINIO_SECRET_ACCESS_KEY=minioadmin >> .env.tmp
echo. >> .env.tmp
echo # Profile Storage (MinIO) >> .env.tmp
echo PROFILE_MINIO_ACCESS_KEY_ID=minioadmin >> .env.tmp
echo PROFILE_MINIO_SECRET_ACCESS_KEY=minioadmin >> .env.tmp
echo. >> .env.tmp
echo # Redis (Job Queue) Settings >> .env.tmp
echo REDIS_ADDR=redis:6379 >> .env.tmp
echo REDIS_PASSWORD="" >> .env.tmp
echo REDIS_DB=0 >> .env.tmp
echo. >> .env.tmp
echo # Static Site Storage (MinIO) >> .env.tmp
echo STATIC_SITE_MINIO_ACCESS_KEY_ID=minioadmin >> .env.tmp
echo STATIC_SITE_MINIO_SECRET_ACCESS_KEY=minioadmin >> .env.tmp
echo. >> .env.tmp
echo # SFSP MinIO Credentials >> .env.tmp
echo SFSP_MINIO_ACCESS_KEY_ID=sfspadmin >> .env.tmp
echo SFSP_MINIO_SECRET_ACCESS_KEY=sfspsecret >> .env.tmp

:: Replace the old .env with the new one
move /Y .env.tmp .env

echo .env file created successfully.

echo Building Docker images...
docker compose build
if %errorlevel% neq 0 (
    echo Docker image build failed. Exiting.
    exit /b %errorlevel%
)

echo Starting Docker services...
docker compose up -d
if %errorlevel% neq 0 (
    echo Docker services failed to start. Exiting.
    exit /b %errorlevel%
)

echo.
echo Environment setup complete and Docker services are running.
echo You can access the following services:
echo -------------------------------------------------------------------
echo Frontend:                  http://localhost:3001
echo Profile Service API:       http://localhost:8084
echo Mypage Service API:        http://localhost:8083
echo Static Site Upload API:    http://localhost:8085
echo SFSP API:                  http://localhost:8090
echo.
echo MinIO Consoles:
echo Profile Storage:           http://localhost:9001
echo Game Storage:              http://localhost:9003
echo Static Site Storage:       http://localhost:9005
echo SFSP MinIO:                http://localhost:9011
echo -------------------------------------------------------------------
echo.
echo You can check the status of your services with "docker compose ps".

endlocal
pause