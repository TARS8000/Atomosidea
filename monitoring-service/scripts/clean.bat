@echo off
echo Cleaning monitoring services...
docker-compose -f ../docker-compose.monitoring.yml down --volumes --remove-orphans
echo Monitoring services cleaned.
pause