@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=          Profile Storage Cleanup Script       ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'This script will permanently delete all profile-related assets'"
powershell -command "Write-Host 'from the storage directory.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Warning: This action is irreversible. It only deletes the files,'"
powershell -command "Write-Host '         and does not update the database records.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Important: Before running this script, ensure all Docker containers are stopped.'"
powershell -command "Write-Host '           (e.g., run ''stop.bat'' or ''docker-compose down'')'"
powershell -command "Write-Host ''"

set /p "are_you_sure=Are you sure you want to delete all profile assets? (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'Cleanup canceled.'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Step 1: Cleaning up the ''profile_storage_data'' directory...'"
if exist "profile_storage_data" (
    rmdir /S /Q "profile_storage_data"
    mkdir "profile_storage_data"
    powershell -command "Write-Host '  -> All profile assets have been deleted.'"
) else (
    powershell -command "Write-Host '  -> ''profile_storage_data'' does not exist.'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=         Profile Storage Cleanup Complete!     ='"
powershell -command "Write-Host '================================================='"
pause