@echo off
setlocal
cd /d "%~dp0"
cls
rem ---------------------------------------------------------------------------
rem Aggressive cleanup = 'docker compose down -v --remove-orphans --rmi local':
rem   - removes the containers and networks of this compose project
rem   - removes the compose-declared named volumes (see 'docker compose config --volumes'):
rem       auth_db_data, app_db_data, profile_db_data, team_db_data, sfsp_db_data
rem       (all PostgreSQL data) and sfsp_temp_scan
rem   - removes the images built by compose for this project (--rmi local)
rem   - prunes the Docker build cache (machine-wide, not only this project)
rem
rem It does NOT delete the MinIO object data: that is a bind mount
rem (.\backend\storage_data, shared by all buckets), not a Docker volume, and it
rem is never touched here. To delete it, run cleanup_storage.bat /
rem cleanup_sfsp_minio_data.bat BEFORE this script (MinIO must be running).
rem Without the DB volumes, the next start re-initializes the databases from
rem init.sql, so the old objects in storage_data would be orphaned.
rem ---------------------------------------------------------------------------
echo =================================================
echo =    Aggressively Cleaning Up Application...    =
echo =================================================
echo.
echo This will stop and remove all containers and networks of this project,
echo all its Docker volumes (ALL DATABASES), and its built images,
echo and prune the Docker build cache.
echo.
echo WARNING: All database data (users, videos, games, sites, teams, SFSP jobs)
echo          will be PERMANENTLY DELETED.
echo NOTE:    The MinIO files in 'backend\storage_data' are NOT deleted.
echo          (Use cleanup_storage.bat / cleanup_sfsp_minio_data.bat for them.)
echo.

set /p "are_you_sure=Are you sure you want to delete the containers, volumes and images? (y/n): "
if /i not "%are_you_sure%"=="y" (
    echo Cleanup canceled.
    pause
    goto :eof
)

echo.
echo Step 1: Stopping containers, removing volumes, orphans and built images...
docker compose down -v --remove-orphans --rmi local
if errorlevel 1 (
    echo   - Step 1 reported an error. If a network could not be removed, another
    echo     compose project ^(e.g. the monitoring service^) may still be attached to it.
    echo     Stop that project first and run this script again.
)
echo.

echo Step 2: Pruning Docker build cache...
docker builder prune --all --force
echo.

echo =================================================
echo =       Aggressive Cleanup Complete!            =
echo =================================================
echo You can now run 'setup.bat' or 'start.bat' to begin again.
pause
