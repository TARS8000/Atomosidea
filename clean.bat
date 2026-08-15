@echo off
cls
echo =================================================
echo =    Aggressively Cleaning Up Application...    =
echo =================================================
echo.
echo This will stop and remove all containers, networks, images, and volumes.
echo WARNING: All data will be PERMANENTLY DELETED.
echo.

echo Step 1: Stopping containers and removing anonymous volumes...
docker compose down -v --remove-orphans
echo.

echo Step 2: Removing named data volumes...
rem プロジェクトのプレフィックス（atomosidea_ 等）がついている想定です
rem ※もしエラーが出ても、Step 1で消えていれば問題ありません
docker volume rm atomosidea_postgres_data atomosidea_minio_data --force
echo.

echo Step 3: Removing Docker images for this project...
rem プロジェクト名を atomosidea に変更しています
docker image prune -a --force --filter "label=com.docker.compose.project=atomosidea"
echo.

echo Step 4: Pruning Docker build cache...
docker builder prune --all --force
echo.

echo =================================================
echo =       Aggressive Cleanup Complete!            =
echo =================================================
echo You can now run 'setup.bat' or 'start.bat' to begin again.
pause