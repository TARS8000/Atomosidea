@echo off
cls

echo =================================================
echo = Force Updating and Restarting Application...
echo =================================================
echo.

echo Step 1: Setting up environment variables...
set GOOGLE_CLIENT_ID=
set /p GOOGLE_CLIENT_ID="Enter your Google Client ID: "
if "%GOOGLE_CLIENT_ID%"=="" goto ERR_NO_ID

set GOOGLE_CLIENT_SECRET=
set /p GOOGLE_CLIENT_SECRET="Enter your Google Client Secret: "
if "%GOOGLE_CLIENT_SECRET%"=="" goto ERR_NO_SECRET

echo.
echo Creating .env file...

> .env echo # PostgreSQL Database Settings
>> .env echo POSTGRES_USER=user
>> .env echo POSTGRES_PASSWORD=password
>> .env echo POSTGRES_DB=movie_db
>> .env echo.
>> .env echo # Application Settings
>> .env echo JWT_SECRET=your-super-secret-and-long-random-string
>> .env echo UPLOAD_DIR=/storage/videos
>> .env echo THUMBNAIL_DIR=/storage/thumbnails
>> .env echo APP_URL=http://localhost:3001
>> .env echo CACHE_BUSTER=1
>> .env echo.
>> .env echo # Static Site Hosting
>> .env echo STATIC_SITE_DOMAIN=localhost
>> .env echo.
>> .env echo # Admin Registration Code
>> .env echo ADMIN_REGISTRATION_CODE=your-secret-admin-code
>> .env echo.
>> .env echo # Google OAuth2 Settings
>> .env echo GOOGLE_CLIENT_ID=%GOOGLE_CLIENT_ID%
>> .env echo GOOGLE_CLIENT_SECRET=%GOOGLE_CLIENT_SECRET%
>> .env echo.
>> .env echo # MinIO Settings
>> .env echo MINIO_USE_SSL=false
>> .env echo.
>> .env echo # Game Storage
>> .env echo GAME_MINIO_ACCESS_KEY_ID=minioadmin
>> .env echo GAME_MINIO_SECRET_ACCESS_KEY=minioadmin
>> .env echo.
>> .env echo # Profile Storage
>> .env echo PROFILE_MINIO_ACCESS_KEY_ID=minioadmin
>> .env echo PROFILE_MINIO_SECRET_ACCESS_KEY=minioadmin
>> .env echo.
>> .env echo # Redis Settings
>> .env echo REDIS_ADDR=redis:6379
>> .env echo REDIS_PASSWORD=
>> .env echo REDIS_DB=0
>> .env echo.
>> .env echo # Static Site Storage
>> .env echo STATIC_SITE_MINIO_ACCESS_KEY_ID=minioadmin
>> .env echo STATIC_SITE_MINIO_SECRET_ACCESS_KEY=minioadmin
>> .env echo.
>> .env echo # SFSP MinIO Credentials
>> .env echo SFSP_MINIO_ACCESS_KEY_ID=sfspadmin
>> .env echo SFSP_MINIO_SECRET_ACCESS_KEY=sfspsecret

echo .env file created successfully.
echo.

if not exist "storage\videos" mkdir "storage\videos"
if not exist "storage\thumbnails" mkdir "storage\thumbnails"

echo Step 2: Stopping and removing old containers...
docker compose down
echo.

echo Step 3: Rebuilding all services without cache...
docker compose build --no-cache
if %errorlevel% neq 0 goto ERR_BUILD

echo.
echo Step 4: Starting the application...
docker compose up -d
if %errorlevel% neq 0 goto ERR_START

echo.
echo =================================================
echo = Application has been forcefully updated and is now running!
echo = Access the frontend at http://localhost:3001
echo =================================================
pause
exit /b 0

:ERR_NO_ID
echo.
echo [ERROR] Google Client ID cannot be empty.
pause
exit /b 1

:ERR_NO_SECRET
echo.
echo [ERROR] Google Client Secret cannot be empty.
pause
exit /b 1

:ERR_BUILD
echo.
echo #################################################
echo #         ERROR: Build failed.                  #
echo #################################################
pause
exit /b 1

:ERR_START
echo.
echo #################################################
echo #      ERROR: Failed to start containers.       #
echo #################################################
pause
exit /b 1