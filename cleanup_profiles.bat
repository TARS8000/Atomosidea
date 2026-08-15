@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=          Profile Storage Cleanup Script       ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'このスクリプトは、すべてのプロフィール関連アセットを'"
powershell -command "Write-Host 'ストレージディレクトリから完全に削除します。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '警告: この操作は元に戻せません。ファイルのみを削除し、'"
powershell -command "Write-Host '      データベースレコードは更新しません。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '重要: このスクリプトを実行する前に、すべてのDockerコンテナが停止していることを'"
powershell -command "Write-Host '      確認してください。(例: ''stop.bat'' または ''docker-compose down'' を実行)'"
powershell -command "Write-Host ''"

set /p "are_you_sure=すべてのプロフィールアセットを削除してもよろしいですか？ (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'クリーンアップはキャンセルされました。'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Step 1: ''profile_storage_data'' ディレクトリをクリーンアップしています...'"
if exist "profile_storage_data" (
    rmdir /S /Q "profile_storage_data"
    mkdir "profile_storage_data"
    powershell -command "Write-Host '  -> すべてのプロフィールアセットが削除されました。'"
) else (
    powershell -command "Write-Host '  -> ''profile_storage_data'' は存在しません。'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=         Profile Storage Cleanup Complete!     ='"
powershell -command "Write-Host '================================================='"
pause