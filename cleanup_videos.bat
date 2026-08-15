@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Video Storage Cleanup Script        ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'このスクリプトは、すべてのアップロードされた動画ファイルと'"
powershell -command "Write-Host 'サムネイルファイルをストレージディレクトリから完全に削除します。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '警告: この操作は元に戻せません。ファイルのみを削除し、'"
powershell -command "Write-Host '      データベースレコードは更新しません。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '重要: このスクリプトを実行する前に、すべてのDockerコンテナが停止していることを'"
powershell -command "Write-Host '      確認してください。(例: ''stop.bat'' または ''docker-compose down'' を実行)'"
powershell -command "Write-Host ''"

set /p "are_you_sure=すべての動画ファイルを削除してもよろしいですか？ (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'クリーンアップはキャンセルされました。'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Step 1: ''video_storage_data\videos'' ディレクトリをクリーンアップしています...'"
if exist "video_storage_data\videos" (
    rmdir /S /Q "video_storage_data\videos"
    mkdir "video_storage_data\videos"
    powershell -command "Write-Host '  -> すべての動画ファイルが削除されました。'"
) else (
    powershell -command "Write-Host '  -> ''video_storage_data\videos'' は存在しません。'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Step 2: ''video_storage_data\thumbnails'' ディレクトリをクリーンアップしています...'"
if exist "video_storage_data\thumbnails" (
    rmdir /S /Q "video_storage_data\thumbnails"
    mkdir "video_storage_data\thumbnails"
    powershell -command "Write-Host '  -> すべてのサムネイルファイルが削除されました。'"
) else (
    powershell -command "Write-Host '  -> ''video_storage_data\thumbnails'' は存在しません。'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Video Storage Cleanup Complete!     ='"
powershell -command "Write-Host '================================================='"
pause