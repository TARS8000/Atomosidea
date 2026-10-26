@echo off
setlocal
cd /d "%~dp0"
echo Stopping the application...
rem 'down' removes the containers and networks but keeps the DB volumes and
rem the MinIO data (.\backend\storage_data). Use clean.bat to delete the volumes.
docker compose down
if errorlevel 1 (
    echo.
    echo [WARN] 'docker compose down' reported an error. If a network could not be removed,
    echo        another compose project ^(e.g. the monitoring service in monitoring-service^)
    echo        is still attached to atmosidea-network / sfsp-isolated-net. Stop it too:
    echo          docker compose -f monitoring-service\docker-compose.monitoring.yml down
)
echo.
echo Application stopped. ^(Databases and MinIO data are kept.^)
pause
