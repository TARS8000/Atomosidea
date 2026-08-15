@echo off
cls
echo =================================================
echo = Force Updating and Restarting Application...
echo = This will stop and remove all existing containers,
echo = then rebuild everything from scratch without cache.
echo =================================================
echo.

echo Step 1: Pulling latest changes from Git...
echo (Note: This requires your local branch to be tracking a remote branch)
git pull
echo.

echo Step 2: Stopping and removing old containers...
docker compose down
echo.

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
echo = Application has been forcefully updated and is now running!
echo = Access the frontend at http://localhost:3001
echo =================================================
pause
