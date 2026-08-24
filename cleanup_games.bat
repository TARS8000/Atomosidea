@echo off
cls
echo =================================================
echo =         Unity Game Cleanup Script           =
echo =================================================
echo.
echo This script will permanently delete:
echo   1. All game records from the 'games' table in the database.
echo   2. All corresponding game files from the MinIO 'games' bucket.
echo.
echo This action does NOT affect video data.
echo.
echo WARNING: This action is irreversible.
echo.

set /p "are_you_sure=Are you sure you want to delete all uploaded games? (y/n): "
if /i not "%are_you_sure%"=="y" (
    echo Cleanup canceled.
    pause
    goto :eof
)

echo.
echo Step 1: Deleting all game records from the database...
rem Use container's environment variables for psql by executing via sh -c
docker compose exec app-db sh -c "psql -U \"$POSTGRES_USER\" -d \"$POSTGRES_DB\" -c \"TRUNCATE TABLE games RESTART IDENTITY CASCADE;\""
if %errorlevel% neq 0 (
    echo   -> FAILED to delete game records. Please check if the application is running.
    pause
    goto :eof
) else (
    echo   -> Successfully deleted all records from the 'games' table.
)
echo.

echo Step 2: Deleting all game files from MinIO storage...
echo   (This may take a moment...)
rem The minio-init service already uses sh -c in its entrypoint, so this should be fine.
docker compose run --rm minio-init sh -c "mc alias set myminio http://game-storage:9000 \"$MINIO_ACCESS_KEY_ID\" \"$MINIO_SECRET_ACCESS_KEY\" && mc rm --recursive --force myminio/\"$MINIO_BUCKET_NAME\"/"
if %errorlevel% neq 0 (
    echo   -> FAILED to delete game files from MinIO.
    pause
    goto :eof
) else (
    echo   -> Successfully deleted all objects from the '%MINIO_BUCKET_NAME%' bucket.
)
echo.

echo =================================================
echo =           Game cleanup complete!            =
echo =================================================
pause