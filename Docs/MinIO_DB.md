# Atomosidea — MinIO ストレージ管理とデータベース

本ドキュメントは、Atomosideaのオブジェクトストレージ（MinIO）とリレーショナルデータベース（PostgreSQL）の構成・管理について解説します。

Atomosideaは1つのMinIOインスタンスと5つのPostgreSQLデータベースで構成されます。各コンテンツ種別（プロフィール・動画・ゲーム・static-site）とSFSP（Raw/Clean）は**1つのMinIO内のバケット**で論理分離されます。SFSPはかつてRaw/Cleanの2つの独立したMinIOで汚染範囲を隔離していましたが、現在は同一MinIOのバケット単位で隔離します。データベースは用途別に5分割され、サービスごとに接続ロールと権限を絞り込みます。

## 目次

- [概要](#概要)
- [データベース（PostgreSQL）](#データベースpostgresql)
- [MinIO ストレージ](#minio-ストレージ)
- [バケットと匿名アクセス](#バケットと匿名アクセス)
- [IAMポリシーと認証キー](#iamポリシーと認証キー)
- [MinIO自動初期化（minio-init）](#minio自動初期化minio-init)
- [データ永続化と運用](#データ永続化と運用)
- [関連ファイル一覧](#関連ファイル一覧)

---

## 概要

Atomosideaの永続層は、データベースはインスタンス単位（物理分離）、MinIOはバケット単位・IAMユーザー単位（論理分離）でリスクを隔離する設計です。

| 層 | 種類 | 数 | 隔離の単位 |
|---|---|---|---|
| データベース | PostgreSQL 16 | 5 | データベース単位（種別・サービス別） |
| オブジェクトストレージ | MinIO | 1 | バケット単位（種別・スキャン状態別） |
| キュー・キャッシュ | Redis 7 | 1 | キュー名・キー名別（`requirepass`でパスワード必須。本ドキュメントの対象外） |

設計原則:

- **コンテンツ種別ごとに専用バケットと専用IAMユーザー**を用意し、各サービスが自分のバケットだけを操作できるようにして、他種別への影響を限定する。
- **SFSPはRaw（スキャン前）とClean（スキャン後）を同一MinIO内の別バケット**に分け、スキャンで問題が見つかったファイルがClean側へ到達しない構造にする。
- **データベースは用途別に分割し、権限を細分化**。`auth-db`（`auth_service_user`／`mypage_service_user`）、`team-db`（`team_service_user`）、`app-db`のSFSPワーカー用（`sfsp_worker`、読み取り専用）は専用ロールで接続し、最小権限の原則に従う。それ以外（`app-db`・`profile-db`・`sfsp-db`への他のサービスの接続）は`POSTGRES_USER`で接続する。ロールのパスワードは`init.sql`に書かず、`db-role-init`が`.env`の値で設定する。

---

## データベース（PostgreSQL）

5つのPostgreSQL 16インスタンスを運用します。すべてイメージ `postgres:16`、`restart: always`で、`pg_isready`によるhealthcheckを持ちます。`auth-db`・`app-db`・`profile-db`・`team-db`はネットワーク `backend-db-network`、`sfsp-db`は専用の `sfsp-db-network` に属し、どちらも `internal: true`（外向き通信なし）です。ホストにポートは公開されません。

各データベースはDockerの永続ボリューム（`/var/lib/postgresql/data`）に保存され、ボリュームが空の初回起動時のみ、`docker-entrypoint-initdb.d`経由で`init.sql`が実行されてスキーマが構築されます。アプリ用ロールは`init.sql`でパスワードなしで作成され、起動のたびに実行される`db-role-init`サービス（`backend/db-init/sync-role-passwords.sh`）が`.env`の`AUTH_SERVICE_DB_PASSWORD`・`MYPAGE_SERVICE_DB_PASSWORD`・`TEAM_SERVICE_DB_PASSWORD`・`SFSP_WORKER_DB_PASSWORD`を`ALTER ROLE`で設定します（既存ボリュームにも適用されるため、`.env`を変えて`up`するだけでローテーションできます）。

### データベース一覧

| サービス名 | コンテナ名 | データベース名 | ボリューム | 初期化ソース | 専用ネットワーク |
|---|---|---|---|---|---|
| `auth-db` | `atmosidea-auth-db` | `auth_db` | `auth_db_data` | `backend/auth/auth-datebase/auth-db/init.sql` | backend-db-network |
| `app-db` | `atmosidea-app-db` | `app_db` | `app_db_data` | `backend/datebase/app-db/init.sql` | backend-db-network |
| `profile-db` | `atmosidea-profile-db` | `profile_db` | `profile_db_data` | `backend/profile-service/profile-db/init.sql` | backend-db-network |
| `team-db` | `atmosidea-team-db` | `team_db` | `team_db_data` | `backend/team-service/team-db/init.sql` | backend-db-network |
| `sfsp-db` | `sfsp-db` | `sfsp_db` | `sfsp_db_data` | `backend/security/sfsp-db/`（ディレクトリ全体） | sfsp-db-network |

`sfsp-db`は専用の`sfsp-db-network`のみに属し、SFSPサービス（`sfsp-api`・`sfsp-worker`）だけが接続できます（`sfsp-worker`は`app-db`にも`backend-db-network`経由で`sfsp_worker`ロールで接続します）。

### 各データベースのスキーマ

#### auth-db（`auth_db`）— 認証・ユーザー

初期化ファイル: `backend/auth/auth-datebase/auth-db/init.sql`

複数のサービスが1つの`users`テーブルを共有する構成です。

- **ロール**
  - `auth_service_user` — `auth-service`が接続（登録・ログイン・Google認証）。パスワードは`db-role-init`が`.env`の`AUTH_SERVICE_DB_PASSWORD`から設定。
  - `mypage_service_user` — `mypage-service`が接続（投稿一覧の読み取り）。パスワードは`db-role-init`が`.env`の`MYPAGE_SERVICE_DB_PASSWORD`から設定。
- **テーブル: `users`**
  - `id` UUID（PK）, `username`, `google_name`, `email`（NOT NULL）, `password_hash`, `provider`（`local`/`google`）, `provider_id`, `is_admin` BOOLEAN, `status`, `created_at`
  - `UNIQUE(provider, email)` と `UNIQUE(provider, provider_id)` でGoogle認証の重複を防止
- **デフォルト管理者はありません**
  - `init.sql`は管理者アカウントを投入しません（既知のパスワードが公開されるため）。管理者は`POST /api/auth/register`と`ADMIN_REGISTRATION_CODE`で作成します。
- **権限**
  - 両ロールに`users`テーブルのSELECT/INSERT/UPDATE/DELETEを付与。`public`スキーマのUSAGE/CREATEも付与。

> **注意:** `auth-db`の`users`テーブルはauth-serviceとmypage-serviceが**共有**します。mypage-serviceは`AUTH_DATABASE_URL`（ユーザー`mypage_service_user`、パスワード`MYPAGE_SERVICE_DB_PASSWORD`）で接続します。

#### app-db（`app_db`）— コンテンツメタデータ

初期化ファイル: `backend/datebase/app-db/init.sql`

動画・ゲーム・static-siteの3種類のメタデータを管理します。各テーブルにはSFSPジョブID・サムネイルジョブID・チーム限定公開用の`team_id`・更新トリガー・インデックスを定義します。

- **テーブル**
  - `videos` — `id` VARCHAR(10)（PK）, `uploader_id` UUID, `title`, `description`, `filename`（HLSプレイリストパス）, `thumbnail_path`, `status`（`scanning`→`processing`→`public`/`error`/`quarantined`。スキャン結果が`failed`/`invalid`ならその値。列のデフォルトは`processing`）, `sfsp_job_id`, `thumbnail_sfsp_job_id`, `processing_details`, `team_id` VARCHAR(24)（NULLなら全体公開、チームトークンがあればそのチーム限定）, タイムスタンプ
  - `games` — `id` VARCHAR(10)（PK）, `user_id` UUID, `title`, `description`, `status`, `sfsp_job_id`, `thumbnail_sfsp_job_id`, `processing_details`, `team_id`, `game_url`, `thumbnail_url`, `scale`, `offset_x/y`, `native_width/height`, タイムスタンプ
  - `static_sites` — `id` VARCHAR(10)（PK）, `user_id` UUID, `title`, `description`, `status`, `sfsp_job_id`, `thumbnail_sfsp_job_id`, `processing_details`, `team_id`, `minio_path`, `entry_point_path`（デフォルト`index.html`）, `thumbnail_url`, タイムスタンプ
- **インデックス** — `sfsp_job_id`・`thumbnail_sfsp_job_id`・`uploader_id`/`user_id`ごとに作成
- **自動更新トリガー** — 各テーブルに`updated_at`をUPDATE時に自動更新するトリガー（関数`update_updated_at_column()`）
- **ロール: `sfsp_worker`** — 読み取り専用（`videos`・`games`・`static_sites`へのSELECTのみ）。パスワードは`db-role-init`が`.env`の`SFSP_WORKER_DB_PASSWORD`から設定。SFSPワーカーがジョブのメタデータだけ確認する際に使用。以後に作成されるテーブルにもSELECTが付与されるデフォルト権限を設定。

#### profile-db（`profile_db`）— プロフィール情報

初期化ファイル: `backend/profile-service/profile-db/init.sql`

- **テーブル: `users`**
  - `id` UUID（PK）, `username`, `bio` TEXT, `icon_url` TEXT, `background_image_url` TEXT, `icon_sfsp_job_id` UUID, `background_sfsp_job_id` UUID, `status`（デフォルト`offline`）, `created_at`, `updated_at`
- **テストデータ** — 初回起動時に`user_atomos`（UUIDv7形式）が`ON CONFLICT (id) DO NOTHING`で挿入。実環境では不要なら削除可能。

#### team-db（`team_db`）— チーム・コンテンツ共有

初期化ファイル: `backend/team-service/team-db/init.sql`

Discordの招待リンク風、トークン（URL）ベースのクローズドコンテンツ共有を管理します。UUIDは`gen_random_uuid()`（PostgreSQL組み込み）で生成します。

- **テーブル**
  - `teams` — チーム。`id` UUID（PK）、`token` VARCHAR(24)（UNIQUE、24文字のbase36・crypto/randで生成・約124ビットのエントロピー）、`name` VARCHAR(255)（NOT NULL）、`description` TEXT、`is_public` BOOLEAN（デフォルト`false`）、`auto_approve` BOOLEAN（デフォルト`false`）、`allow_member_invite` BOOLEAN（デフォルト`false`）、`created_by` UUID、`created_at` タイムスタンプ
  - `team_members` — チーム所属。`id` UUID（PK）、`team_id`（`teams.id`へのFK、`ON DELETE CASCADE`）、`user_id` UUID、`role`（`owner`/`admin`/`member`、デフォルト`member`）、`joined_at` タイムスタンプ。`UNIQUE(team_id, user_id)`
  - `team_join_requests` — 承認型の加入リクエスト。`id` UUID（PK）、`team_id`（`teams.id`へのFK、`ON DELETE CASCADE`）、`user_id` UUID、`status`（`pending`/`approved`/`rejected`/`cancelled`、デフォルト`pending`）、`requested_at`、`reviewed_at`、`reviewed_by` UUID。`UNIQUE(team_id, user_id)`
  - `team_content` — 既存コンテンツ（動画・ゲーム・static-site）をチームに紐付けるためのリンク表（Phase 2用）。`id` UUID（PK）、`team_id`（`teams.id`へのFK、`ON DELETE CASCADE`）、`content_type`（`video`/`game`/`static-site`）、`content_id` VARCHAR(20)、`created_at`。`UNIQUE(team_id, content_type, content_id)`
  - `team_posts` — チーム独自の投稿（Phase 1）。`id` UUID（PK）、`team_id`（`teams.id`へのFK、`ON DELETE CASCADE`）、`author_id` UUID、`title` VARCHAR(255)、`body` TEXT、`created_at` タイムスタンプ
- **ロール: `team_service_user`** — `teams`・`team_members`・`team_join_requests`・`team_content`・`team_posts`へのSELECT/INSERT/UPDATE/DELETE。パスワードは`db-role-init`が`.env`の`TEAM_SERVICE_DB_PASSWORD`から設定。`team_posts`の`author_id`から著者名を取得する場合は、`app-db`ではなく`profile-service`の`GET /api/profile/:id`から取得（`app-db`に`users`テーブルは無い）。

#### sfsp-db（`sfsp_db`）— スキャンプラットフォーム

初期化ディレクトリ: `backend/security/sfsp-db/`（`001_sfsp_initial_schema.sql`）

ファイルスキャンの全履歴を管理します。`uuid-ossp`拡張を使用。

- **テーブル**
  - `files` — スキャン対象ファイルのメタデータ。`id` UUID, `filename`, `filesize` BIGINT, `mime_type`, `sha256`（UNIQUEなし＝重複アップロード許可）, `storage_path`, `file_type`（`video`/`html`/`zip`/`other`）, `target_service`（`video`/`game`/`static_site`/`other`等）, `created_at`
  - `scan_jobs` — ジョブ管理。`id` UUID, `file_id`（`files.id`へのFK、`ON DELETE CASCADE`）, `status`（`queued`/`running`/`clean`/`suspicious`/`malicious`/`failed`/`invalid`）, `processing_details`, `is_cleaned_up` BOOLEAN（クリーンアップフラグ）, `cleaned_up_at`, `event_published_at`（完了イベントをpublishした日時。再処理時の二重publish防止）, タイムスタンプ
  - `scan_results` — スキャン結果詳細。`id` UUID, `job_id`（`scan_jobs.id`へのFK、`ON DELETE CASCADE`）, `scanner`（`clamav`/`yara`）, `result`（`clean`/`suspicious`/`malicious`/`error`）, `details`, `raw_output` JSONB, `scanned_at`
- **インデックス** — `sha256`・`file_id`・`job_id`に加え、クリーンアップ対象を高速検索する複合インデックス`idx_scan_jobs_cleanup(status, is_cleaned_up)`
- **トリガー** — `scan_jobs`の`updated_at`を自動更新

---

## MinIO ストレージ

Atomosideaは**1つのMinIOインスタンス**で全オブジェクトストレージを統合します。イメージ `minio/minio:latest`、`restart: always`、データはプロジェクト内のホストディレクトリ`backend/storage_data`にバインドマウントして保存されます。APIポート`9000`とコンソールポート`9001`は`atmosidea-network`と`sfsp-isolated-net`の内部にのみ公開され、ホストにはポートを公開していません。

全バケットを1インスタンス内で**バケット単位**に分離し、種別・スキャン状態別の論理分離を実現します。各コンテンツ種別（プロフィール・動画・ゲーム・static-site）とSFSP（Raw/Clean）は同じMinIO内の別バケットとして配置されます。

### MinIOインスタンス一覧

| サービス名 | コンテナ名 | コンソール | ボリューム | 匿名アクセス |
|---|---|---|---|---|
| `minio` | `atmosidea-minio` | `:9001`（ホスト非公開） | `./backend/storage_data`（バインドマウント） | バケットごとに設定（後述） |

- rootの認証は`MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD`（`.env`）。`.env.example`の値は公開済みのため、必ず変更すること。
- `MINIO_USE_SSL`は`false`（内部通信は平文・内部ネットワーク限定）。
- コンソール（`:9001`）はホストにポートを公開していないため、`http://localhost:9001`では開けません。バケット・オブジェクトの確認は監視ダッシュボード（`monitoring-service`、`http://127.0.0.1:8090`）のStorage機能を使います。
- healthcheckはMinIOのライブエンドポイント（`/minio/health/live`）を`curl`で確認します。MinIOイメージには`mc`クライアントは同梱されていません。

### バケットの中身（実ディレクトリ構造）

MinIOはバケットごとにデータディレクトリ内にサブフォルダを作成します。例:

```
backend/storage_data/
├── .minio.sys/            # MinIO内部メタデータ（自動生成・手動編集禁止）
├── user-profiles/         # プロフィール画像
├── static-sites/          # 静的サイトの配信ファイル（＋サムネイル: `thumbnails/`プレフィックス）
├── videos/                # HLS変換後の動画（.m3u8, .ts）（＋サムネイル: `thumbnails/<id>`キー）
├── games/                 # ゲームファイル（＋サムネイル: `thumbnails/<id>`キー）
├── raw-files/             # スキャン前のオリジナル（SFSP）
├── clean-files/           # スキャン通過ファイル（SFSP）
└── quarantine/            # 悪性・疑義ファイルの隔離先（SFSP）
```

- `.minio.sys/` はMinIOが管理する内部ディレクトリ（設定・バケットメタデータ・プール情報）。**手動での削除・編集は不可**。
- 各バケット内には対応するコンテンツが格納されます（例：`videos/` にHLSのセグメント（`.ts`）とプレイリスト（`.m3u8`））。
- **サムネイルは専用バケットではありません。** 動画は`videos`バケット内の`thumbnails/<id>`、ゲームは`games`バケット内の`thumbnails/<id>`、static-siteは`static-sites`バケット内の`thumbnails/<ランダム>.<拡張子>`に格納されます。`minio-init`で`thumbnails`バケットは作成されません。

---

## バケットと匿名アクセス

**アプリ用バケット**はパブリックダウンロード（`mc anonymous set download`）が有効で、フロントエンドのNginxが直接ファイルを配信できます。**SFSP用バケット**は機微なため匿名アクセスを無効にし、IAMポリシーでアクセスを制限します。これらはすべて1つのMinIO内の別バケットとして分離されています。

### 匿名ダウンロード有効（アプリ用）

| バケット | 配信元 |
|---|---|
| `user-profiles` | Nginxがプロフィール画像を配信 |
| `games` | Nginxがゲームの`index.html`等を配信 |
| `static-sites` | Nginxが静的サイトを配信 |
| `videos` | NginxがHLSとサムネイル（`videos`内の`thumbnails/<id>`キー）を配信 |

### 匿名アクセス無効（SFSP用・IAM保護）

| バケット | 用途 |
|---|---|
| `raw-files` | スキャン前のオリジナルファイル（汚染可能性あり） |
| `clean-files` | スキャン通過ファイルのみ |
| `quarantine` | 悪性・疑義ファイルの隔離先 |

---

## IAMポリシーと認証キー

MinIOでは、アプリ用サービス・アップロードAPI・SFSP API・各ワーカーごとに**専用のIAMユーザーとポリシー**でアクセスを細分化します。すべては1つのMinIO内で管理され、各ユーザーは`minio-init`の実行時に作成され、ポリシーが割り当てられます。

### キーと役割の対応

| Access Key（`.env`） | 使用サービス | 権限（ポリシー） | 対象バケット・操作 |
|---|---|---|---|
| `GAME_MINIO_*` | game-upload-api、game-worker、auth-service（アカウント削除時） | `app-game-policy` | `games` に**全操作**＋バケット一覧 |
| `PROFILE_MINIO_*` | profile-service、auth-service（アカウント削除時） | `app-profile-policy` | `user-profiles` に**全操作**＋バケット一覧 |
| `STATIC_SITE_MINIO_*` | static-site-upload-api、static-site-worker、auth-service（アカウント削除時） | `app-static-site-policy` | `static-sites` に**全操作**＋バケット一覧 |
| `VIDEO_MINIO_*` | video-upload-api、video-worker | `app-video-policy` | `videos` に**全操作**＋バケット一覧 |
| `SFSP_UPLOAD_*` | 各upload API、profile-service | `sfsp-upload-policy` | `raw-files` に**PutObject**（スキャン用アップロード） |
| `SFSP_API_*` | `sfsp-api` | `sfsp-api-policy` | `raw-files` に**全操作**＋バケット一覧（ジョブ作成・保存） |
| `SFSP_WORKER_RAW_*` | `sfsp-worker` | `sfsp-worker-raw-policy` | `raw-files` から**GetObject/DeleteObject/ListBucket**（スキャン用取り込み・後処理削除） |
| `SFSP_WORKER_CLEAN_*` | `sfsp-worker`、`video-upload-api`（clean側） | `sfsp-worker-clean-policy` | `clean-files`・`quarantine`・`raw-files` に**全操作**＋バケット管理（Clean書込・Raw削除） |
| `SFSP_GAME_WORKER_*` | `game-upload-api`、`game-worker` | `sfsp-game-worker-policy` | `clean-files/*` から**GetObject**（展開用ダウンロード） |
| `SFSP_VIDEO_WORKER_*` | `video-worker` | `sfsp-video-worker-policy` | `clean-files/*` から**GetObject**（HLS変換用ダウンロード） |
| `SFSP_STATIC_SITE_WORKER_*` | `static-site-upload-api`、`static-site-worker` | `sfsp-static-site-worker-policy` | `clean-files/*` から**GetObject/DeleteObject**（展開用ダウンロード・処理後の削除） |
| `SFSP_PROFILE_WORKER_*` | `profile-service` | `sfsp-profile-worker-policy` | バケット一覧＋`clean-files/*` から**GetObject**（スキャン後アイコン取得） |

### ポリシー設計のポイント

- **Uploadユーザーは「書き込み専用」** — `raw-files`へのPutObjectのみ。読み取り不可により、アップロード側からスキャン結果への不正な干渉を防止。
- **Workerは「取り込み→書込み→削除」を1人が担当** — `sfsp-worker`がRawから読み取り、Cleanへ書き込み、処理済みRawを削除するフルサイクルを持つ。これによりスキャンパイプラインが完結。
- **各コンテンツワーカーは「Cleanからの読み取り中心」** — ゲーム・動画・static-site・profileのワーカーは`clean-files/*`のGetObjectが基本（static-siteのみ処理後の削除用にDeleteObjectも持つ）。アップロードしたRawにはアクセスしない。
- **アプリ用ユーザーは「自分のバケットだけ」** — `app-*-policy`は対応する1バケット（`games`・`user-profiles`・`static-sites`・`videos`）にのみ全操作を許可する。
- **APIは「Rawへの全操作」** — ジョブ作成とファイル保存のため`raw-files`に全権限。

ポリシーファイル: `backend/minio-init/` 配下の`sfsp-*-policy.json`と`app-*-policy.json`。旧`sfsp-worker-policy.json`は未使用だったため削除した。

---

## MinIO自動初期化（minio-init）

`minio-init`サービスは`minio/mc:latest`イメージを使用し、`restart: "no"`（終了後は再起動しない）で、`docker compose up`のたびに実行されて終了するバッチ処理です（`--ignore-existing`や`|| true`により、何度実行しても問題ない冪等な処理になっています）。

**役割:**

1. **エイリアス設定** — 単一のMinIOに`mc alias set`で接続情報を設定（MinIOが健康になるまでループで待機）。
2. **バケット作成** — `mc mb --ignore-existing`で全バケットを作成（既存は無視）。
3. **匿名アクセス設定** — アプリ用バケットに`mc anonymous set download`を適用。
4. **IAMポリシー作成** — `mc admin policy create`で12種類のポリシー（SFSP用8＋アプリ用4）をMinIOへ登録。
5. **IAMユーザー作成・割り当て** — `mc admin user add`で各サービス用ユーザーを作成し、`mc admin policy attach`でポリシーを紐付け。

**依存関係:**

- `minio`が`service_healthy`後に起動。
- `auth-service`・`profile-service`・各upload API・各worker・`sfsp-api`が`minio-init`の`service_completed_successfully`を待機。これにより、サービスがバケット/ポリシーの準備完了後に起動保証される。

`minio-init`のエントリポイントは`/bin/sh -c`で、全処理を`docker-compose.yml`内の1つのスクリプトで実行します。

---

## データ永続化と運用

### ボリュームとデータ失効

- **DB** はDockerの名付きボリューム、**MinIO** はプロジェクト内のホストディレクトリ`backend/storage_data`（バインドマウント）で保持されます。
- **DB** — コンテナを削除してもボリュームは残ります。`clean.bat`等で完全削除しない限りデータは維持。
- **MinIO** — `backend/storage_data`に書き込まれます。このディレクトリを削除すると全オブジェクトが消えます。
- `.minio.sys/` はMinIO内部状態。**手動削除するとメタデータが破損**し、バケットにアクセスできなくなります。
- `clean.bat`（`docker compose down -v`）はDBの名前付きボリュームを削除しますが、バインドマウントの`backend/storage_data`は削除しません。

### 運用コマンド

- **MinIOの確認** — コンソール（`:9001`）はホストに公開されていないため、監視ダッシュボード（`http://127.0.0.1:8090`、`MONITOR_USERNAME`/`MONITOR_PASSWORD`でログイン）のStorage機能を使う。
- **バケット・オブジェクト確認** — 監視ダッシュボード、または`atmosidea-network`に接続したコンテナからの`mc`コマンドで閲覧。
- **データリセット** — MinIOを停止（`docker compose stop minio`）して`backend/storage_data`内を削除後、`docker compose up -d`で再起動（バケット・IAMユーザー・ポリシーは`minio-init`で再作成）。
- **DBリセット** — `docker volume rm <volume_name>`（例: `auth_db_data`）後、`docker compose up -d`で`init.sql`が再実行（ロールのパスワードは`db-role-init`が再設定）。

### 変更時の注意点

- **バケット名・キー名・環境変数名**を変更した場合は、`docker-compose.yml`・各サービスの環境変数・`minio-init`のエントリポイント・ポリシーJSONを対で更新。
- **IAMポリシー**を変更した場合は、対応する`backend/minio-init/*-policy.json`と`minio-init`のエントリポイント（`mc admin policy create`/`attach`）を対で更新。
- **新しいバケット**を追加した場合は、`minio-init`の`mc mb`と匿名設定、および必要に応じてIAMポリシーへ反映。

---

## 関連ファイル一覧

### データベース初期化

| ファイル | 役割 |
|---|---|
| `backend/auth/auth-datebase/auth-db/init.sql` | auth-dbのスキーマ・ロール（パスワードなし）。管理者は投入しない |
| `backend/datebase/app-db/init.sql` | app-dbの3テーブル・インデックス・トリガー・sfsp_worker権限 |
| `backend/profile-service/profile-db/init.sql` | profile-dbのusersテーブル・テストデータ |
| `backend/team-service/team-db/init.sql` | team-dbのteams/team_members/team_join_requests/team_content/team_postsスキーマ |
| `backend/security/sfsp-db/001_sfsp_initial_schema.sql` | sfsp-dbのfiles/scan_jobs/scan_resultsスキーマ |
| `backend/db-init/sync-role-passwords.sh` | `db-role-init`が実行。`.env`の値でアプリ用DBロールのパスワードを設定 |

### MinIO初期化・ポリシー

| ファイル | 役割 |
|---|---|
| `backend/minio-init/sfsp-upload-policy.json` | upload API・profile-serviceのraw-filesへのPutObject |
| `backend/minio-init/sfsp-api-policy.json` | sfsp-apiのraw-filesへの全操作 |
| `backend/minio-init/sfsp-worker-raw-policy.json` | sfsp-workerのraw-filesからの読み取り・削除 |
| `backend/minio-init/sfsp-worker-clean-policy.json` | sfsp-workerのclean-files/quarantineへの書き込み・raw削除 |
| `backend/minio-init/sfsp-game-worker-policy.json` | game-workerのclean-filesからの読み取り |
| `backend/minio-init/sfsp-video-worker-policy.json` | video-workerのclean-filesからの読み取り |
| `backend/minio-init/sfsp-static-site-worker-policy.json` | static-site-workerのclean-filesからの読み取り・削除 |
| `backend/minio-init/sfsp-profile-worker-policy.json` | profile-serviceのclean-filesからの読み取り |
| `backend/minio-init/app-game-policy.json` | gamesバケットへの全操作（game系サービス） |
| `backend/minio-init/app-profile-policy.json` | user-profilesバケットへの全操作（profile-service） |
| `backend/minio-init/app-static-site-policy.json` | static-sitesバケットへの全操作（static-site系サービス） |
| `backend/minio-init/app-video-policy.json` | videosバケットへの全操作（video系サービス） |

### MinIOデータ（ホストディレクトリ・バインドマウント）

| ディレクトリ | 担当MinIO | バケット |
|---|---|---|
| `backend/storage_data` | `atmosidea-minio` | 全バケット（`user-profiles`・`static-sites`・`videos`・`games`・`raw-files`・`clean-files`・`quarantine`）。サムネイルは専用バケットではなく`videos`/`static-sites`内の`thumbnails/<id>`キーに格納 |