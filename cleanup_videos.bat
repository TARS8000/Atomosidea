@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Deletes ALL video files: every object in the 'videos' bucket (HLS segments
rem and thumbnails) of the single MinIO container (atmosidea-minio), and empties
rem the ffmpeg scratch directory backend\video-service\upload_workdir (videos\ and
rem thumbnails\ are recreated empty).
rem Files only - rows in public.videos (app-db) are NOT touched.
rem Runs against the RUNNING stack (docker exec); MinIO root credentials come
rem from the container's environment (.env).
rem ---------------------------------------------------------------------------
echo =================================================
echo =           Video Storage Cleanup Script        =
echo =================================================
echo.
echo This script will permanently delete all uploaded video files
echo and thumbnail files:
echo   1. Every object in the MinIO 'videos' bucket (container: atmosidea-minio).
echo   2. Leftovers in the scratch directory 'backend\video-service\upload_workdir'.
echo.
echo Warning: This action is irreversible. It only deletes the files,
echo          and does not update the database records (videos table in app-db).
echo.
echo NOTE: The container 'atmosidea-minio' must be running.
echo       Do not run this while a video upload / conversion is in progress.
echo.

set /p "are_you_sure=Are you sure you want to delete all video files? (y/n): "
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
echo Step 1: Deleting all objects from the MinIO 'videos' bucket...
docker exec atmosidea-minio sh -c "export MC_HOST_local=http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000; mc rm --recursive --force local/videos"
if errorlevel 1 (
    echo   - FAILED to delete video files from MinIO.
    pause
    goto :eof
)
echo   - All video and thumbnail objects have been deleted.

echo.
echo Step 2: Cleaning up the 'backend\video-service\upload_workdir' scratch directory...
for %%D in (videos thumbnails) do (
    if exist "backend\video-service\upload_workdir\%%D" rmdir /S /Q "backend\video-service\upload_workdir\%%D"
    mkdir "backend\video-service\upload_workdir\%%D"
)
echo   - Scratch directory has been emptied.

echo.
echo =================================================
echo =           Video Storage Cleanup Complete!     =
echo =================================================
pause
