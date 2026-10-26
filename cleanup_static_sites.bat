@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Deletes ALL static sites: rows of public.static_sites in app-db (database
rem app_db) and every object in the 'static-sites' bucket of the single MinIO
rem container (atmosidea-minio). Runs against the RUNNING stack (docker exec);
rem MinIO root credentials come from the container's environment (.env).
rem ---------------------------------------------------------------------------
echo =================================================
echo =      Cleaning up Static Site Data...          =
echo =================================================
echo.
echo This script will permanently delete:
echo   1. All records from the 'static_sites' table in app-db (app_db).
echo   2. All objects in the MinIO 'static-sites' bucket (container: atmosidea-minio).
echo.
echo WARNING: This action is irreversible.
echo NOTE: The containers 'atmosidea-app-db' and 'atmosidea-minio' must be running.
echo.

set /p "are_you_sure=Are you sure you want to delete all static sites? (y/n): "
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
echo Step 1: Truncating 'static_sites' table in app-db...
docker exec atmosidea-app-db sh -c "psql -v ON_ERROR_STOP=1 -U \"$POSTGRES_USER\" -d app_db -c 'TRUNCATE TABLE public.static_sites RESTART IDENTITY CASCADE;'"
if errorlevel 1 (
    echo   - FAILED to truncate the 'static_sites' table.
    pause
    goto :eof
)
echo   - Successfully deleted all records from the 'static_sites' table.

echo.
echo Step 2: Deleting all objects from the MinIO 'static-sites' bucket...
docker exec atmosidea-minio sh -c "export MC_HOST_local=http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000; mc rm --recursive --force local/static-sites"
if errorlevel 1 (
    echo   - FAILED to delete static site files from MinIO.
    pause
    goto :eof
)
echo   - Successfully deleted all objects from the 'static-sites' bucket.

echo.
echo =================================================
echo =      Static Site Cleanup Complete!             =
echo =================================================
pause
