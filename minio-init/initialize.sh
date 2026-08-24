#!/bin/sh
set -e

# Wait for MinIO to be online
/usr/bin/mc alias set myminio http://game-storage:9000 ${MINIO_ROOT_USER} ${MINIO_ROOT_PASSWORD}
/usr/bin/mc admin info myminio

# --- Initialize 'games' bucket ---
GAMES_BUCKET="games"
if /usr/bin/mc ls myminio/${GAMES_BUCKET} > /dev/null 2>&1; then
    echo "Bucket '${GAMES_BUCKET}' already exists."
else
    echo "Creating bucket '${GAMES_BUCKET}'."
    /usr/bin/mc mb myminio/${GAMES_BUCKET}
fi
echo "Setting bucket policy for '${GAMES_BUCKET}' to download."
/usr/bin/mc anonymous set download myminio/${GAMES_BUCKET}

# --- Initialize 'videos' bucket ---
VIDEOS_BUCKET="videos"
if /usr/bin/mc ls myminio/${VIDEOS_BUCKET} > /dev/null 2>&1; then
    echo "Bucket '${VIDEOS_BUCKET}' already exists."
else
    echo "Creating bucket '${VIDEOS_BUCKET}'."
    /usr/bin/mc mb myminio/${VIDEOS_BUCKET}
fi
echo "Setting bucket policy for '${VIDEOS_BUCKET}' to download."
/usr/bin/mc anonymous set download myminio/${VIDEOS_BUCKET}

echo "MinIO initialization complete for all buckets."
