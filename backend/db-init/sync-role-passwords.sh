#!/bin/sh
# DB のアプリ用ロールのパスワードを .env の値に合わせる（docker-compose.yml の db-role-init が毎回の起動時に実行する）。
#
# - init.sql はロールをパスワード無しで作成する（パスワードを SQL に直書きしない）。
# - このスクリプトは既存のボリュームにも適用されるので、.env のパスワードを変えて
#   `docker compose up -d` するだけでローテーションできる。
# - 接続は各 DB のスーパーユーザー（POSTGRES_USER / POSTGRES_PASSWORD）で行う。
# - パスワードは psql 変数（:'pw'）で渡すので、SQL インジェクションやシェルの展開は起きない。
set -eu

: "${PGUSER:?PGUSER (POSTGRES_USER) must be set}"
: "${PGPASSWORD:?PGPASSWORD (POSTGRES_PASSWORD) must be set}"
: "${AUTH_SERVICE_DB_PASSWORD:?AUTH_SERVICE_DB_PASSWORD must be set}"
: "${TEAM_SERVICE_DB_PASSWORD:?TEAM_SERVICE_DB_PASSWORD must be set}"
: "${SFSP_WORKER_DB_PASSWORD:?SFSP_WORKER_DB_PASSWORD must be set}"

wait_for() {
  host="$1"
  db="$2"
  i=0
  until pg_isready -q -h "$host" -d "$db"; do
    i=$((i + 1))
    if [ "$i" -ge 120 ]; then
      echo "db-role-init: $host is not ready" >&2
      exit 1
    fi
    sleep 1
  done
}

# set_password <host> <db> <role> <password>
set_password() {
  host="$1"
  db="$2"
  role="$3"
  pw="$4"
  wait_for "$host" "$db"
  exists=$(psql -h "$host" -d "$db" -v ON_ERROR_STOP=1 -qtA -v role="$role" <<'SQL'
SELECT count(*) FROM pg_roles WHERE rolname = :'role';
SQL
)
  if [ "$exists" != "1" ]; then
    echo "db-role-init: role $role does not exist in $host/$db (init.sql not applied?)" >&2
    exit 1
  fi
  psql -h "$host" -d "$db" -v ON_ERROR_STOP=1 -q -v role="$role" -v pw="$pw" <<'SQL'
ALTER ROLE :"role" WITH LOGIN PASSWORD :'pw';
SQL
  echo "db-role-init: password of $role on $host synced"
}

set_password auth-db auth_db auth_service_user "$AUTH_SERVICE_DB_PASSWORD"
set_password team-db team_db team_service_user "$TEAM_SERVICE_DB_PASSWORD"
set_password app-db app_db sfsp_worker "$SFSP_WORKER_DB_PASSWORD"
