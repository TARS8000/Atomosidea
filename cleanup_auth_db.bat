@echo off
chcp 65001 > nul
cls
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Auth DB Cleanup Script            ='"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host ''"
powershell -command "Write-Host 'このスクリプトは、認証データベース (auth-db) に関連付けられた'"
powershell -command "Write-Host 'Dockerボリュームを完全に削除します。これにより、すべてのユーザーアカウント、'"
powershell -command "Write-Host '認証トークン、および関連する個人情報が失われます。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '警告: この操作は元に戻せません。'"
powershell -command "Write-Host '他のデータベースレコードやMinIOストレージは更新されません。'"
powershell -command "Write-Host ''"
powershell -command "Write-Host '重要: このスクリプトを実行する前に、すべてのDockerコンテナが停止していることを'"
powershell -command "Write-Host '確認してください。(例: ''stop.bat'' または ''docker-compose down'' を実行)'"
powershell -command "Write-Host ''"

where docker >nul 2>&1
if %errorlevel% neq 0 (
    powershell -command "Write-Host 'エラー: Dockerコマンドが見つかりません。Dockerがインストールされ、PATHに追加されていることを確認してください。'"
    pause
    goto :eof
)

set /p "are_you_sure=認証DBのDockerボリュームを削除してもよろしいですか？ (y/n): "
if /i not "%are_you_sure%"=="y" (
    powershell -command "Write-Host 'クリーンアップはキャンセルされました。'"
    pause
    goto :eof
)

powershell -command "Write-Host ''"
powershell -command "Write-Host 'Dockerボリューム ''atomosidea_auth_db_data'' を削除しています...'"
docker volume rm atomosidea_auth_db_data

if %errorlevel% equ 0 (
    powershell -command "Write-Host '  -> Dockerボリューム ''atomosidea_auth_db_data'' が削除されました。'"
    powershell -command "Write-Host '     次回 ''docker-compose up'' を実行すると、再作成され初期化されます。'"
) else (
    powershell -command "Write-Host '  -> Dockerボリューム ''atomosidea_auth_db_data'' の削除に失敗しました。'"
    powershell -command "Write-Host '     Dockerが実行されており、ボリュームが存在することを確認してください。'"
)

powershell -command "Write-Host ''"
powershell -command "Write-Host '================================================='"
powershell -command "Write-Host '=           Auth DB クリーンアップ完了!           ='"
powershell -command "Write-Host '================================================='"
pause