@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Auth DB Cleanup Script            ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'This script will permanently delete the Docker volume associated'"
powershell -command "Write-Host 'with the authentication database (auth-db). This will result in the loss'"
powershell -command "Write-Host 'of all user accounts, authentication tokens, and related personal information.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Warning: This action is irreversible.'"
powershell -command "Write-Host 'It does not update other database records or MinIO storage.'"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'Important: Before running this script, ensure all Docker containers are stopped.'"
powershell -command "Write-Host '           (e.g., run ''stop.bat'' or ''docker-compose down'')'"
powershell -command "Write-Host ''"

where docker >nul 2>&1
if %errorlevel% neq 0 (
    powershell -command "Write-Host 'Error: Docker command not found. Please ensure Docker is installed and in your PATH.'"
    pause
    goto :eof
)

set /p "are_you_sure=Are you sure you want to delete the Auth DB Docker volume? (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'Cleanup canceled.'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Deleting Docker volume ''atomosidea_auth_db_data''...'"
docker volume rm atomosidea_auth_db_data

if %errorlevel% equ 0 (
    powershell -command "Write-Host '  -> Docker volume ''atomosidea_auth_db_data'' has been deleted.'"
    powershell -command "Write-Host '     It will be recreated and initialized the next time you run ''docker-compose up''.'"
) else (
    powershell -command "Write-Host '  -> Failed to delete Docker volume ''atomosidea_auth_db_data''.'"
    powershell -command "Write-Host '     Please ensure Docker is running and the volume exists.'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Auth DB Cleanup Complete!           ='"
powershell -command "Write-Host '================================================='"
pause