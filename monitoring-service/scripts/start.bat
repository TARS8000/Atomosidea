@echo off
echo Building monitoring services without cache...
docker-compose -f ../docker-compose.monitoring.yml build --no-cache
if %errorlevel% neq 0 (
    echo Build failed.
    pause
    exit /b %errorlevel%
)

echo Starting monitoring services...
docker-compose -f ../docker-compose.monitoring.yml up -d
if %errorlevel% neq 0 (
    echo Startup failed.
    pause
    exit /b %errorlevel%
)

echo Monitoring services started. Access at http://localhost:8090
pause
