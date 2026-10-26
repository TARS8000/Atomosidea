@echo off
setlocal
cd /d "%~dp0"
cls
echo =================================================
echo =           Starting Application...           =
echo =================================================
echo.
echo This will start all application containers in the background.
echo Make sure you have run 'setup.bat' at least once.
echo.

if not exist ".env" (
    echo [ERROR] .env not found. Run 'setup.bat' first.
    pause
    goto :eof
)

docker compose up -d

if errorlevel 1 (
    echo.
    echo #################################################
    echo #      ERROR: Failed to start containers.       #
    echo #   Please check the logs above for details.    #
    echo #################################################
    echo.
    echo If the network definitions in docker-compose.yml changed since the last
    echo start, run 'stop.bat' ^(docker compose down^) and then 'start.bat' again.
    pause
    goto :eof
)

echo.
echo =================================================
echo =           Application is running!           =
echo =================================================
echo Frontend:   http://localhost:3001  ^(the only port published to the host^)
echo.
echo The MinIO console ^(port 9001^) and the other services are NOT published
echo to the host. Use the cleanup_*.bat scripts or 'docker exec atmosidea-minio mc ...'.
echo.
echo Monitoring dashboard ^(optional, separate compose project, not started by this script^):
echo   cd monitoring-service
echo   docker compose --env-file ..\.env -f docker-compose.monitoring.yml up -d --build
echo   Then open http://127.0.0.1:8090 ^(loopback only^) and log in with
echo   MONITOR_USERNAME / MONITOR_PASSWORD from .env ^(HTTP Basic authentication^).
pause
