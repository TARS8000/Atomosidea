@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Deletes ALL profile assets (icons / background images): every object in the
rem 'user-profiles' bucket of the single MinIO container (atmosidea-minio).
rem Files only - rows in profile-db (profile_db) are NOT touched (icon_url etc.
rem will point to missing objects). Runs against the RUNNING stack (docker exec);
rem MinIO root credentials come from the container's environment (.env).
rem ---------------------------------------------------------------------------
echo =================================================
echo =          Profile Storage Cleanup Script       =
echo =================================================
echo.
echo This script will permanently delete all profile-related assets
echo (every object in the MinIO 'user-profiles' bucket, container: atmosidea-minio).
echo.
echo Warning: This action is irreversible. It only deletes the files,
echo          and does not update the database records (profile-db).
echo.
echo NOTE: The container 'atmosidea-minio' must be running.
echo.

set /p "are_you_sure=Are you sure you want to delete all profile assets? (y/n): "
if /i not "%are_you_sure%"=="y" (
    echo Cleanup canceled.
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
echo Step 1: Deleting all objects from the MinIO 'user-profiles' bucket...
docker exec atmosidea-minio sh -c "export MC_HOST_local=http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000; mc rm --recursive --force local/user-profiles"
if errorlevel 1 (
    echo   - FAILED to delete profile assets from MinIO.
    pause
    goto :eof
)
echo   - All profile assets have been deleted.

echo.
echo =================================================
echo =         Profile Storage Cleanup Complete!     =
echo =================================================
pause
