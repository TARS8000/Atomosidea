@echo off
cls
echo =================================================
echo =           Storage Cleanup Script            =
echo =================================================
echo.
echo This script will permanently delete all uploaded videos,
echo game assets, profile images, and static sites from their respective
echo storage directories.
echo.
echo WARNING: This action is irreversible and only deletes files.
echo It does NOT update the database records.
echo.
echo IMPORTANT: Ensure all Docker containers are stopped before running this script.
echo            (e.g., by running 'stop.bat' or 'docker-compose down')
echo.

set /p "are_you_sure=Are you sure you want to delete all stored media files? (y/n): "
if /i not "%are_you_sure%"=="y" (
    echo Cleanup canceled.
    pause
    goto :eof
)

echo.
echo Step 1: Cleaning up 'backend\video-service\video_storage_data/videos' directory...
if exist "backend\video-service\video_storage_data\videos" (
    rmdir /S /Q "backend\video-service\video_storage_data\videos"
    mkdir "backend\video-service\video_storage_data\videos"
    echo   -> All video files have been deleted.
) else (
    echo   -> 'backend\video-service\video_storage_data\videos' does not exist.
)

echo.
echo Step 2: Cleaning up 'backend\video-service\video_storage_data/thumbnails' directory...
if exist "backend\video-service\video_storage_data\thumbnails" (
    rmdir /S /Q "backend\video-service\video_storage_data\thumbnails"
    mkdir "backend\video-service\video_storage_data\thumbnails"
    echo   -> All thumbnail files have been deleted.
) else (
    echo   -> 'backend\video-service\video_storage_data\thumbnails' does not exist.
)

echo.
echo Step 3: Cleaning up 'backend\game-service\game_storage_data' directory (MinIO game assets)...
if exist "backend\game-service\game_storage_data" (
    rmdir /S /Q "backend\game-service\game_storage_data"
    mkdir "backend\game-service\game_storage_data"
    echo   -> All game assets have been deleted.
) else (
    echo   -> 'backend\game-service\game_storage_data' does not exist.
)

echo.
echo Step 4: Cleaning up 'backend\auth\auth-storage\profile_storage_data' directory (MinIO profile assets)...
if exist "backend\auth\auth-storage\profile_storage_data" (
    rmdir /S /Q "backend\auth\auth-storage\profile_storage_data"
    mkdir "backend\auth\auth-storage\profile_storage_data"
    echo   -> All profile assets have been deleted.
) else (
    echo   -> 'backend\auth\auth-storage\profile_storage_data' does not exist.
)

echo.
echo Step 5: Cleaning up 'backend\static-site-service\static_site_storage_data' directory (MinIO static site assets)...
if exist "backend\static-site-service\static_site_storage_data" (
    rmdir /S /Q "backend\static-site-service\static_site_storage_data"
    mkdir "backend\static-site-service\static_site_storage_data"
    echo   -> All static site assets have been deleted.
) else (
    echo   -> 'backend\static-site-service\static_site_storage_data' does not exist.
)

echo.
echo =================================================
echo =           Storage cleanup complete!           =
echo =================================================
pause