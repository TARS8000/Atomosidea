@echo off
chcp 65001 >nul
cls
echo =================================================
echo = Force Updating and Restarting Application...
echo = Building containers in parallel using BuildKit...
echo =================================================
echo.

set DOCKER_BUILDKIT=1
set COMPOSE_DOCKER_CLI_BUILD=1

echo Step 1: Pulling latest changes from Git...
git pull
echo.

echo Step 2: Stopping old containers...
docker compose down
echo.

echo Step 3: Rebuilding all services in parallel without cache...
docker compose build --no-cache
if errorlevel 1 goto :error

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
echo =================================================
pause
goto :eof

:error
echo.
echo #################################################
echo #         ERROR: Build failed.                  #
echo #   Please check the logs above for details.    #
echo #################################################
pause