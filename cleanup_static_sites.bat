@echo off
echo =================================================
echo =      Cleaning up Static Site Data...          =
echo =================================================

echo.
echo Step 1: Removing static site storage directory...
if exist .\\static_site_storage_db (
    rmdir /s /q .\\static_site_storage_db
    echo  - Directory 'static_site_storage_db' removed.
) else (
    echo  - Directory 'static_site_storage_db' not found.
)

echo.
echo Step 2: Truncating 'static_sites' table in app-db...
docker-compose exec -T app-db psql -U %POSTGRES_USER% -d app_db -c "TRUNCATE TABLE static_sites RESTART IDENTITY;"

echo.
echo =================================================
echo =      Static Site Cleanup Complete!             =
echo =================================================
