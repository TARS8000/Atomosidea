# Use the official ClamAV image as a base
FROM clamav/clamav:latest

# clamscan の一時ファイルは /tmp に書かれる。サンドボックスは /tmp にサイズ上限付きの tmpfs をマウントするため、イメージ側での用意は不要

# ウイルス定義をビルド時に最新化する。サンドボックスはネットワーク遮断で実行するため、
# 実行時には更新できない（定義を更新するには `docker compose build --pull sfsp-clamav-client` で再ビルドする）
RUN freshclam --stdout || echo "WARNING: freshclam failed; using the virus database bundled in the base image"

# A simple command to verify the image can be built
# We will override the ENTRYPOINT/CMD in the sandbox to run clamscan with specific options
CMD ["clamscan", "--version"]
