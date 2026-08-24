@echo off
echo Stopping monitoring services...
docker-compose -f ../docker-compose.monitoring.yml down
echo Monitoring services stopped.
pause