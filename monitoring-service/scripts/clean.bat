@echo off
rem Works from any directory: paths are relative to this script (%~dp0)
set "MON_ENV_FILE=%~dp0..\..\.env"
set "MON_COMPOSE_FILE=%~dp0..\docker-compose.monitoring.yml"

echo Cleaning monitoring services...
docker compose --env-file "%MON_ENV_FILE%" -f "%MON_COMPOSE_FILE%" down --volumes --remove-orphans
if %errorlevel% neq 0 (
    echo Cleanup failed.
    pause
    exit /b %errorlevel%
)
echo Monitoring services cleaned.
pause
