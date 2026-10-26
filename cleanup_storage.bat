@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Deletes ALL stored media: every object in the buckets 'videos', 'games',
rem 'user-profiles' and 'static-sites' of the single MinIO container
rem (atmosidea-minio), and empties the ffmpeg scratch directory
rem backend\video-service\upload_workdir. The SFSP buckets (raw-files,
rem clean-files, quarantine) are handled by cleanup_sfsp_minio_data.bat.
rem Files only - database rows are NOT touched.
rem The data lives in the bind mount .\backend\storage_data, but it is emptied
rem through MinIO (mc), never by deleting that directory from the host.
rem Runs against the RUNNING stack (docker exec); MinIO root credentials come
rem from the container's environment (.env).
rem ---------------------------------------------------------------------------
echo =================================================
echo =           Storage Cleanup Script            =
echo =================================================
echo.
echo This script will permanently delete all uploaded videos,
echo game assets, profile images, and static sites from the unified MinIO
echo (container: atmosidea-minio), buckets:
echo   videos, games, user-profiles, static-sites
echo and empty the scratch directory 'backend\video-service\upload_workdir'.
echo.
echo WARNING: This action is irreversible and only deletes files.
echo It does NOT update the database records.
echo.
echo NOTE: The container 'atmosidea-minio' must be running.
echo       Do not run this while uploads / conversions are in progress.
echo.

set /p "are_you_sure=Are you sure you want to delete all stored media files? (y/n): "
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

for %%B in (videos games user-profiles static-sites) do (
    echo.
    echo Deleting all objects from the MinIO '%%B' bucket...
    docker exec atmosidea-minio sh -c "export MC_HOST_local=http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000; mc rm --recursive --force local/%%B"
    if errorlevel 1 (
        echo   - FAILED to delete objects from '%%B'.
        pause
        goto :eof
    )
    echo   - '%%B' has been emptied.
)

echo.
echo Cleaning up the 'backend\video-service\upload_workdir' scratch directory...
for %%D in (videos thumbnails) do (
    if exist "backend\video-service\upload_workdir\%%D" rmdir /S /Q "backend\video-service\upload_workdir\%%D"
    mkdir "backend\video-service\upload_workdir\%%D"
)
echo   - Scratch directory has been emptied.

echo.
echo =================================================
echo =           Storage cleanup complete!           =
echo =================================================
pause
