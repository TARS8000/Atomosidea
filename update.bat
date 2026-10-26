@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"
cls
echo =================================================
echo = Force Updating and Restarting Application...
echo = Building containers in parallel using BuildKit...
echo =================================================
echo.

set DOCKER_BUILDKIT=1
set COMPOSE_DOCKER_CLI_BUILD=1

if not exist ".env" (
    echo [ERROR] .env not found. Run 'setup.bat' first.
    pause
    goto :eof
)

echo Step 1: Pulling latest changes from Git...
git pull || echo [WARN] git pull failed (uncommitted local changes present?). Continuing with current working tree - rebuild will use the local code.
echo.

echo Step 2: Stopping old containers and networks...
rem 'down' (not just 'up') is required: networks whose definition changed
rem (e.g. internal: true) are only recreated after 'docker compose down'.
rem Volumes and the MinIO data (.\backend\storage_data) are kept.
docker compose down
if errorlevel 1 (
    echo [WARN] 'docker compose down' reported an error ^(a network may still be in use by
    echo        another project, e.g. the monitoring service^). Continuing anyway.
)
echo.

echo Step 3: Rebuilding all services in parallel without cache...
docker compose build --no-cache
if errorlevel 1 (
    echo.
    echo #################################################
    echo #         ERROR: Build failed.                  #
    echo #   Please check the logs above for details.    #
    echo #################################################
    pause
    goto :eof
)

echo.
echo Step 4: Starting the application...
docker compose up -d

REM Retry once in case of initial failure due to service startup lag
if errorlevel 1 (
    echo.
    echo Warning: Initial start failed. Retrying in 5 seconds...
    timeout /t 5 /nobreak >nul
    docker compose up -d
)

if errorlevel 1 (
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
echo = Application has been forcefully updated and is now running!
echo = Access the frontend at http://localhost:3001
echo = ^(The monitoring dashboard, if used, is a separate project: see start.bat^)
echo =================================================
pause
