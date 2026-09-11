@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=         SFSP MinIO Data Cleanup Script        ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'This script will permanently delete the SFSP MinIO persistent data directories'"
powershell -command "Write-Host '(''backend\security\sfsp-storage\sfsp_raw_minio_data'' and ''backend\security\sfsp-storage\sfsp_clean_minio_data'') and all files within them.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Warning: This action is irreversible.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Important: Before running this script, ensure the relevant Docker containers are stopped.'"
powershell -command "Write-Host '           (e.g., run ''stop.bat'' or ''docker-compose down'')'"
powershell -command "Write-Host ''"

set /p "are_you_sure=Are you sure you want to delete the SFSP MinIO data? (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'Cleanup canceled.'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Cleaning up the ''backend\security\sfsp-storage\sfsp_raw_minio_data'' directory...'"
if exist "backend\security\sfsp-storage\sfsp_raw_minio_data" (
    rmdir /S /Q "backend\security\sfsp-storage\sfsp_raw_minio_data"
    mkdir "backend\security\sfsp-storage\sfsp_raw_minio_data"
    powershell -command "Write-Host '  -> SFSP Raw MinIO data has been deleted.'"
) else (
    powershell -command "Write-Host '  -> ''backend\security\sfsp-storage\sfsp_raw_minio_data'' does not exist.'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Cleaning up the ''backend\security\sfsp-storage\sfsp_clean_minio_data'' directory...'"
if exist "backend\security\sfsp-storage\sfsp_clean_minio_data" (
    rmdir /S /Q "backend\security\sfsp-storage\sfsp_clean_minio_data"
    mkdir "backend\security\sfsp-storage\sfsp_clean_minio_data"
    powershell -command "Write-Host '  -> SFSP Clean MinIO data has been deleted.'"
) else (
    powershell -command "Write-Host '  -> ''backend\security\sfsp-storage\sfsp_clean_minio_data'' does not exist.'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=       SFSP MinIO Data Cleanup Complete!       ='"
powershell -command "Write-Host '================================================='"
pause