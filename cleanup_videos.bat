@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Video Storage Cleanup Script        ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'This script will permanently delete all uploaded video files'"
powershell -command "Write-Host 'and thumbnail files from the storage directories.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Warning: This action is irreversible. It only deletes the files,'"
powershell -command "Write-Host '         and does not update the database records.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Important: Before running this script, ensure all Docker containers are stopped.'"
powershell -command "Write-Host '           (e.g., run ''stop.bat'' or ''docker-compose down'')'"
powershell -command "Write-Host ''"

set /p "are_you_sure=Are you sure you want to delete all video files? (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'Cleanup canceled.'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Step 1: Cleaning up the ''video_storage_data\\videos'' directory...'"
if exist "video_storage_data\videos" (
    rmdir /S /Q "video_storage_data\videos"
    mkdir "video_storage_data\videos"
    powershell -command "Write-Host '  -> All video files have been deleted.'"
) else (
    powershell -command "Write-Host '  -> ''video_storage_data\\videos'' does not exist.'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Step 2: Cleaning up the ''video_storage_data\\thumbnails'' directory...'"
if exist "video_storage_data\thumbnails" (
    rmdir /S /Q "video_storage_data\thumbnails"
    mkdir "video_storage_data\thumbnails"
    powershell -command "Write-Host '  -> All thumbnail files have been deleted.'"
) else (
    powershell -command "Write-Host '  -> ''video_storage_data\\thumbnails'' does not exist.'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Video Storage Cleanup Complete!     ='"
powershell -command "Write-Host '================================================='"
pause