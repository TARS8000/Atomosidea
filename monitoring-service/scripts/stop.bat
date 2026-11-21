@echo off
rem Works from any directory: paths are relative to this script (%~dp0)
set "MON_ENV_FILE=%~dp0..\..\.env"
set "MON_COMPOSE_FILE=%~dp0..\docker-compose.monitoring.yml"

echo Stopping monitoring services...
docker compose --env-file "%MON_ENV_FILE%" -f "%MON_COMPOSE_FILE%" down
if %errorlevel% neq 0 (
    echo Stop failed.
    pause
    exit /b %errorlevel%
)
echo Monitoring services stopped.
pause
