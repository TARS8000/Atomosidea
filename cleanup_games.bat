@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Deletes ALL games: rows of public.games in app-db (database app_db) and every
rem object in the 'games' bucket of the single MinIO container (atmosidea-minio).
rem Runs against the RUNNING stack (docker exec); MinIO root credentials are read
rem from the container's own environment (MINIO_ROOT_USER / MINIO_ROOT_PASSWORD
rem from .env), nothing is hardcoded here.
rem ---------------------------------------------------------------------------
echo =================================================
echo =         Unity Game Cleanup Script           =
echo =================================================
echo.
echo This script will permanently delete:
echo   1. All game records from the 'games' table in app-db (app_db).
echo   2. All objects in the MinIO 'games' bucket (container: atmosidea-minio).
echo.
echo This action does NOT affect video / static site data.
echo.
echo WARNING: This action is irreversible.
echo NOTE: The containers 'atmosidea-app-db' and 'atmosidea-minio' must be running.
echo.

set /p "are_you_sure=Are you sure you want to delete all uploaded games? (y/n): "
if /i not "%are_you_sure%"=="y" (
    echo Cleanup canceled.
    pause
    goto :eof
)

set "_RUNNING="
for /f "delims=" %%S in ('docker inspect -f "{{.State.Running}}" atmosidea-app-db 2^>nul') do set "_RUNNING=%%S"
if /i not "%_RUNNING%"=="true" (
    echo.
    echo ERROR: container 'atmosidea-app-db' is not running. Start the stack with start.bat first.
    pause
    goto :eof
)
set "_RUNNING="
for /f "delims=" %%S in ('docker inspect -f "{{.State.Running}}" atmosidea-minio 2^>nul') do set "_RUNNING=%%S"
if /i not "%_RUNNING%"=="true" (
    echo.
    echo ERROR: container 'atmosidea-minio' is not running. Start the stack with start.bat first.
    pause
    goto :eof
)

echo.
echo Step 1: Deleting all game records from app-db...
docker exec atmosidea-app-db sh -c "psql -v ON_ERROR_STOP=1 -U \"$POSTGRES_USER\" -d app_db -c 'TRUNCATE TABLE public.games RESTART IDENTITY CASCADE;'"
if errorlevel 1 (
    echo   - FAILED to delete game records.
    pause
    goto :eof
)
echo   - Successfully deleted all records from the 'games' table.
echo.

echo Step 2: Deleting all game files from the MinIO 'games' bucket...
echo   (This may take a moment...)
docker exec atmosidea-minio sh -c "export MC_HOST_local=http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000; mc rm --recursive --force local/games"
if errorlevel 1 (
    echo   - FAILED to delete game files from MinIO.
    pause
    goto :eof
)
echo   - Successfully deleted all objects from the 'games' bucket.
echo.

echo =================================================
echo =           Game cleanup complete!            =
echo =================================================
pause
