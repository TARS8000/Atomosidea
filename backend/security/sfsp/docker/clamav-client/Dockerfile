# Use the official ClamAV image as a base
FROM clamav/clamav:latest

# Ensure clamscan can write its temporary files/sockets
# ClamAV often tries to write to /var/clamav or /tmp
# We will use /tmp for temporary data and ensure it's writable.

# Create a writable directory for ClamAV's temporary files if needed
RUN mkdir -p /tmp/clamav_tmp && chmod 777 /tmp/clamav_tmp

# ウイルス定義をビルド時に最新化する。サンドボックスはネットワーク遮断で実行するため、
# 実行時には更新できない（定義を更新するには `docker compose build --pull sfsp-clamav-client` で再ビルドする）
RUN freshclam --stdout || echo "WARNING: freshclam failed; using the virus database bundled in the base image"

# A simple command to verify the image can be built
# We will override the ENTRYPOINT/CMD in the sandbox to run clamscan with specific options
CMD ["clamscan", "--version"]
