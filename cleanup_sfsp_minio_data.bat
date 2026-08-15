@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=         SFSP MinIO Data Cleanup Script        ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'このスクリプトは、SFSP MinIOの永続データディレクトリ'"
powershell -command "Write-Host '(''sfsp_minio_data'')とその中のすべてのファイルを完全に削除します。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '警告: この操作は元に戻せません。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '重要: このスクリプトを実行する前に、関連するDockerコンテナが停止していることを'"
powershell -command "Write-Host '      確認してください。(例: ''stop.bat'' または ''docker-compose down'' を実行)'"
powershell -command "Write-Host ''"

set /p "are_you_sure=SFSP MinIOのデータを削除してもよろしいですか？ (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'クリーンアップはキャンセルされました。'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '''sfsp_minio_data'' ディレクトリをクリーンアップしています...'"
if exist "sfsp_minio_data" (
    rmdir /S /Q "sfsp_minio_data"
    mkdir "sfsp_minio_data"
    powershell -command "Write-Host '  -> SFSP MinIOのデータが削除されました。'"
) else (
    powershell -command "Write-Host '  -> ''sfsp_minio_data'' は存在しません。'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=       SFSP MinIO Data Cleanup Complete!       ='"
powershell -command "Write-Host '================================================='"
pause