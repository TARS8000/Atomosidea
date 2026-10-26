@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Deletes the Docker volume of auth-db (compose volume 'auth_db_data'; users,
rem credentials, ...). The real volume name is "<compose project>_auth_db_data"
rem and depends on the project name, so it is resolved at runtime:
rem   1. from the mounts of the container 'atmosidea-auth-db' (if it still exists)
rem   2. otherwise from 'docker compose config --format json'
rem Containers using the volume must be removed first (stop.bat).
rem ---------------------------------------------------------------------------
echo =================================================
echo =           Auth DB Cleanup Script            =
echo =================================================
echo.
echo This script will permanently delete the Docker volume associated
echo with the authentication database (auth-db / auth_db). This will result in the loss
echo of all user accounts, authentication tokens, and related personal information.
echo.
echo Warning: This action is irreversible.
echo It does not update other database records (app-db, profile-db, ...) or MinIO storage.
echo.
echo Important: Before running this script, ensure all Docker containers are stopped
echo            and removed (run 'stop.bat').
echo.

where docker >nul 2>&1
if errorlevel 1 (
    echo Error: Docker command not found. Please ensure Docker is installed and in your PATH.
    pause
    goto :eof
)

rem --- Resolve the volume name -------------------------------------------------
set "VOLUME_NAME="
for /f "delims=" %%V in ('docker inspect -f "{{range .Mounts}}{{.Name}}{{end}}" atmosidea-auth-db 2^>nul') do set "VOLUME_NAME=%%V"
if not defined VOLUME_NAME (
    for /f "delims=" %%V in ('powershell -NoProfile -Command "(docker compose config --format json | ConvertFrom-Json).volumes.auth_db_data.name" 2^>nul') do set "VOLUME_NAME=%%V"
)
if not defined VOLUME_NAME (
    echo Error: could not determine the auth-db volume name.
    echo        Check 'docker compose config --volumes' and 'docker volume ls'.
    pause
    goto :eof
)
docker volume inspect "%VOLUME_NAME%" >nul 2>&1
if errorlevel 1 (
    echo Docker volume '%VOLUME_NAME%' does not exist. Nothing to delete.
    pause
    goto :eof
)

echo Target Docker volume: %VOLUME_NAME%
echo.

set /p "are_you_sure=Are you sure you want to delete the Auth DB Docker volume? (y/n): "
if /i not "%are_you_sure%"=="y" (
    echo Cleanup canceled.
    pause
    goto :eof
)

echo.
echo Deleting Docker volume '%VOLUME_NAME%'...
docker volume rm "%VOLUME_NAME%"

if errorlevel 1 (
    echo   - Failed to delete Docker volume '%VOLUME_NAME%'.
    echo      Please ensure Docker is running and no container uses the volume
    echo      ^(run 'stop.bat' to remove the containers first^).
) else (
    echo   - Docker volume '%VOLUME_NAME%' has been deleted.
    echo      It will be recreated and initialized the next time you run 'start.bat' ^(docker compose up^).
)

echo.
echo =================================================
echo =           Auth DB Cleanup Complete!           =
echo =================================================
pause
