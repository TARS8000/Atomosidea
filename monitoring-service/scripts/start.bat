@echo off
rem Login credentials (MONITOR_USERNAME / MONITOR_PASSWORD) are read from the project root .env
set "MON_ENV_FILE=%~dp0..\..\.env"
set "MON_COMPOSE_FILE=%~dp0..\docker-compose.monitoring.yml"

echo Building monitoring services without cache...
docker-compose --env-file "%MON_ENV_FILE%" -f "%MON_COMPOSE_FILE%" build --no-cache
if %errorlevel% neq 0 (
    echo Build failed.
    pause
    exit /b %errorlevel%
)

echo Starting monitoring services...
docker-compose --env-file "%MON_ENV_FILE%" -f "%MON_COMPOSE_FILE%" up -d
if %errorlevel% neq 0 (
    echo Startup failed.
    pause
    exit /b %errorlevel%
)

echo Monitoring services started. Access at http://localhost:8090
echo Login with MONITOR_USERNAME / MONITOR_PASSWORD from the project root .env
pause
