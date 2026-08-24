@echo off
cls
echo =================================================
echo =           Starting Application...           =
echo =================================================
echo.
echo This will start all application containers in the background.
echo Make sure you have run 'setup.bat' at least once.
echo.

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
echo =           Application is running!           =
echo =================================================
echo Access the frontend at http://localhost:3001
echo Access the MinIO console at http://localhost:9001
pause
