@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Deletes ALL objects of the SFSP (scan pipeline) buckets 'raw-files',
rem 'clean-files' and 'quarantine' in the single MinIO container
rem (atmosidea-minio). The old separate SFSP MinIO instances / host directories
rem (sfsp_raw_minio_data, sfsp_clean_minio_data) no longer exist.
rem Files only - rows in sfsp-db (sfsp_db: files / scan_jobs / scan_results) are
rem NOT touched. Runs against the RUNNING stack (docker exec); MinIO root
rem credentials come from the container's environment (.env).
rem ---------------------------------------------------------------------------
echo =================================================
echo =         SFSP MinIO Data Cleanup Script        =
echo =================================================
echo.
echo This script will permanently delete every object in the SFSP buckets
echo of the unified MinIO (container: atmosidea-minio):
echo   - raw-files     (uploads waiting to be scanned)
echo   - clean-files   (scanned files waiting to be picked up by the workers)
echo   - quarantine    (files that failed the scan)
echo.
echo Warning: This action is irreversible. It only deletes the files,
echo          and does not update the database records (sfsp-db).
echo          Do not run this while scans are in progress: pending jobs would fail.
echo.
echo NOTE: The container 'atmosidea-minio' must be running.
echo.

set /p "are_you_sure=Are you sure you want to delete the SFSP MinIO data? (y/n): "
if /i not "%are_you_sure%"=="y" (
    echo Cleanup canceled.
    pause
    goto :eof
)

set "_RUNNING="
for /f "delims=" %%S in ('docker inspect -f "{{.State.Running}}" atmosidea-minio 2^>nul') do set "_RUNNING=%%S"
if /i not "%_RUNNING%"=="true" (
    echo.
    echo ERROR: container 'atmosidea-minio' is not running. Start the stack with start.bat first.
    pause
    goto :eof
)

for %%B in (raw-files clean-files quarantine) do (
    echo.
    echo Deleting all objects from the MinIO '%%B' bucket...
    docker exec atmosidea-minio sh -c "export MC_HOST_local=http://$MINIO_ROOT_USER:$MINIO_ROOT_PASSWORD@localhost:9000; mc rm --recursive --force local/%%B"
    if errorlevel 1 (
        echo   - FAILED to delete objects from '%%B'.
        pause
        goto :eof
    )
    echo   - '%%B' has been emptied.
)

echo.
echo =================================================
echo =       SFSP MinIO Data Cleanup Complete!       =
echo =================================================
pause
