@echo off
setlocal
cls
cd /d "%~dp0"

echo =================================================
echo = Force Updating and Restarting Application...
echo =================================================
echo.

if not exist ".env.example" goto ERR_NO_EXAMPLE

if exist ".env" (
    echo .env already exists.
    echo Regenerating it creates NEW random passwords. Existing DB volumes keep the old
    echo POSTGRES_PASSWORD, so only regenerate on a fresh install or after cleanup_*.bat.
    choice /C YN /N /M "Regenerate .env? [Y/N]: "
    if errorlevel 2 goto SKIP_ENV
)

echo Step 1: Setting up environment variables...
set GOOGLE_CLIENT_ID=
set /p GOOGLE_CLIENT_ID="Enter your Google Client ID: "
if "%GOOGLE_CLIENT_ID%"=="" goto ERR_NO_ID

set GOOGLE_CLIENT_SECRET=
set /p GOOGLE_CLIENT_SECRET="Enter your Google Client Secret: "
if "%GOOGLE_CLIENT_SECRET%"=="" goto ERR_NO_SECRET

echo.
echo Creating .env from .env.example with random secrets...
rem Every password / secret key / API key gets a random 64-char hex value (URL-safe, e.g. for REDIS_URL).
powershell -NoProfile -ExecutionPolicy Bypass -Command "$ErrorActionPreference='Stop'; $rng=[Security.Cryptography.RandomNumberGenerator]::Create(); function New-RandomHex { $b=New-Object byte[] 32; $rng.GetBytes($b); -join ($b | ForEach-Object { $_.ToString('x2') }) }; $re='^(POSTGRES_PASSWORD|[A-Z_]+_DB_PASSWORD|JWT_SECRET|ADMIN_REGISTRATION_CODE|MINIO_ROOT_PASSWORD|[A-Z_]+_SECRET_KEY|[A-Z_]+_SECRET_ACCESS_KEY|REDIS_PASSWORD|SFSP_API_KEY|INTERNAL_API_KEY|MONITOR_PASSWORD)$'; $src=Join-Path (Get-Location) '.env.example'; $dst=Join-Path (Get-Location) '.env'; $out=foreach ($l in [IO.File]::ReadAllLines($src)) { if ($l -match '^([A-Z0-9_]+)=') { $k=$Matches[1]; if ($k -eq 'GOOGLE_CLIENT_ID') { $k + '=' + $env:GOOGLE_CLIENT_ID } elseif ($k -eq 'GOOGLE_CLIENT_SECRET') { $k + '=' + $env:GOOGLE_CLIENT_SECRET } elseif ($k -match $re) { $k + '=' + (New-RandomHex) } else { $l } } else { $l } }; [IO.File]::WriteAllText($dst, (($out -join [string][char]10) + [char]10), (New-Object Text.UTF8Encoding $false))"
if %errorlevel% neq 0 goto ERR_ENV

echo .env file created successfully.
echo.

:SKIP_ENV
rem ffmpeg scratch dir of video-upload-api / video-worker (bind-mounted to /storage). MinIO data (backend\storage_data) is created by Docker.
if not exist "backend\video-service\upload_workdir\videos" mkdir "backend\video-service\upload_workdir\videos"
if not exist "backend\video-service\upload_workdir\thumbnails" mkdir "backend\video-service\upload_workdir\thumbnails"

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

:ERR_NO_EXAMPLE
echo.
echo [ERROR] .env.example not found.
pause
exit /b 1

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

:ERR_ENV
echo.
echo [ERROR] Failed to create .env.
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
