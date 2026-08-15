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
echo Step 1: Cleaning up 'video_storage_data/videos' directory...
if exist "video_storage_data\videos" (
    rmdir /S /Q "video_storage_data\videos"
    mkdir "video_storage_data\videos"
    echo   -> All video files have been deleted.
) else (
    echo   -> 'video_storage_data\videos' does not exist.
)

echo.
echo Step 2: Cleaning up 'video_storage_data/thumbnails' directory...
if exist "video_storage_data\thumbnails" (
    rmdir /S /Q "video_storage_data\thumbnails"
    mkdir "video_storage_data\thumbnails"
    echo   -> All thumbnail files have been deleted.
) else (
    echo   -> 'video_storage_data\thumbnails' does not exist.
)

echo.
echo Step 3: Cleaning up 'game_storage_data' directory (MinIO game assets)...
if exist "game_storage_data" (
    rmdir /S /Q "game_storage_data"
    mkdir "game_storage_data"
    echo   -> All game assets have been deleted.
) else (
    echo   -> 'game_storage_data' does not exist.
)

echo.
echo Step 4: Cleaning up 'profile_storage_data' directory (MinIO profile assets)...
if exist "profile_storage_data" (
    rmdir /S /Q "profile_storage_data"
    mkdir "profile_storage_data"
    echo   -> All profile assets have been deleted.
) else (
    echo   -> 'profile_storage_data' does not exist.
)

echo.
echo Step 5: Cleaning up 'static_site_storage_data' directory (MinIO static site assets)...
if exist "static_site_storage_data" (
    rmdir /S /Q "static_site_storage_data"
    mkdir "static_site_storage_data"
    echo   -> All static site assets have been deleted.
) else (
    echo   -> 'static_site_storage_data' does not exist.
)

echo.
echo =================================================
echo =           Storage cleanup complete!           =
echo =================================================
pause