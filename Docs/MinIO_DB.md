# Atomosidea — MinIO ストレージ管理とデータベース

本ドキュメントは、Atomosideaのオブジェクトストレージ（MinIO）とリレーショナルデータベース（PostgreSQL）の構成・管理について解説します。

Atomosideaは1つのMinIOインスタンスと4つのPostgreSQLデータベースで構成されます。各コンテンツ種別（プロフィール・動画・ゲーム・static-site）とSFSP（Raw/Clean）は**1つのMinIO内のバケット**で論理分離されます。SFSPはかつてRaw/Cleanの2つの独立したMinIOで汚染範囲を隔離していましたが、現在は同一MinIOのバケット単位で隔離します。データベースは用途別に4分割され、テーブル単位で権限を絞り込みます。

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

Atomosideaの永続層は、論理分離と物理分離の両方でリスクを隔離する設計です。

| 層 | 種類 | 数 | 隔離の単位 |
|---|---|---|---|
| データベース | PostgreSQL 16 | 4 | データベース単位（種別・サービス別） |
| オブジェクトストレージ | MinIO | 1 | バケット単位（種別・スキャン状態別） |
| キュー・キャッシュ | Redis 7 | 1 | キュー名・キー名別（本ドキュメントの対象外） |

設計原則:

- **コンテンツ種別ごとに専用ストレージ**を用意し、1つのバケットの故障や容量超過が他種別へ波及しない。
- **SFSPはRaw（スキャン前）とClean（スキャン後）を同一MinIO内の別バケット**に分け、スキャンで問題が見つかったファイルがClean側へ到達しない構造にする。
- **データベースは権限を細分化**。各サービスは必要なテーブルへのアクセスのみを持つロールで接続し、最小権限の原則に従う。

---

## データベース（PostgreSQL）

4つのPostgreSQL 16インスタンスを運用します。すべてイメージ `postgres:16`、コンテナ起動ポリシー `restart: always`、ネットワーク `backend-db-network`（内部接続のみ）に属します。外部からは公開されません。

各データベースはDockerの永続ボリューム（`/var/lib/postgresql/data`）に保存され、初回起動時に`docker-entrypoint-initdb.d`経由で`init.sql`が実行されてスキーマが構築されます。

### データベース一覧

| サービス名 | コンテナ名 | データベース名 | ボリューム | 初期化ソース | 専用ネットワーク |
|---|---|---|---|---|---|
| `auth-db` | `atmosidea-auth-db` | `auth_db` | `auth_db_data` | `backend/auth/auth-datebase/auth-db/init.sql` | backend-db-network |
| `app-db` | `atmosidea-app-db` | `app_db` | `app_db_data` | `backend/datebase/app-db/init.sql` | backend-db-network |
| `profile-db` | `atmosidea-profile-db` | `profile_db` | `profile_db_data` | `backend/profile-service/profile-db/init.sql` | backend-db-network |
| `team-db` | `atmosidea-team-db` | `team_db` | `team_db_data` | `backend/team-service/team-db/init.sql` | backend-db-network |
| `sfsp-db` | `sfsp-db` | `sfsp_db` | `sfsp_db_data` | `backend/security/sfsp-db/`（ディレクトリ全体） | backend-db-network, sfsp-isolated-net |

`sfsp-db`のみ`sfsp-isolated-net`にも参加し、SFSPサービス（`sfsp-api`・`sfsp-worker`）から直接アクセスできます。

### 各データベースのスキーマ

#### auth-db（`auth_db`）— 認証・ユーザー

初期化ファイル: `backend/auth/auth-datebase/auth-db/init.sql`

複数のサービスが1つの`users`テーブルを共有する構成です。

- **ロール**
  - `auth_service_user` — `auth-service`が接続（登録・ログイン・Google認証）。パスワードは`.env`の`AUTH_SERVICE_DB_PASSWORD`と一致。
  - `mypage_service_user` — `mypage-service`が接続（投稿一覧の読み取り）。パスワードは`.env`の`MYPAGE_SERVICE_DB_PASSWORD`と一致。
- **テーブル: `users`**
  - `id` UUID（PK）, `username`, `google_name`, `email`（NOT NULL）, `password_hash`, `provider`（`local`/`google`）, `provider_id`, `is_admin` BOOLEAN, `status`, `created_at`
  - `UNIQUE(provider, email)` と `UNIQUE(provider, provider_id)` でGoogle認証の重複を防止
- **デフォルト管理者**
  - 初回起動時に`admin@internal.local`（パスワード `AdminPassword123!`）が自動作成されます。`pgcrypto`の`crypt`で生成したハッシュを格納。
- **権限**
  - 両ロールに`users`テーブルのSELECT/INSERT/UPDATE/DELETEを付与。`public`スキーマのUSAGE/CREATEも付与。

> **注意:** `auth-db`の`users`テーブルはauth-serviceとmypage-serviceが**共有**します。mypage-serviceは`AUTH_DATABASE_URL`（ユーザー`mypage_service_user`、パスワード`MYPAGE_SERVICE_DB_PASSWORD`）で接続します。

#### app-db（`app_db`）— コンテンツメタデータ

初期化ファイル: `backend/datebase/app-db/init.sql`

動画・ゲーム・_static_site_の3種類のメタデータを管理します。各テーブルにはSFSPジョブID・サムネイルジョブID・更新トリガー・インデックスを定義します。

- **テーブル**
  - `videos` — `id` VARCHAR(10)（PK）, `uploader_id` UUID, `title`, `description`, `filename`（HLSプレイリストパス）, `thumbnail_path`, `status`（`processing`/`scanning`/`public`/`error`/`quarantined`）, `sfsp_job_id`, `thumbnail_sfsp_job_id`, `processing_details`, タイスタンプ
  - `games` — `id` VARCHAR(10)（PK）, `user_id` UUID, `title`, `description`, `status`, `sfsp_job_id`, `game_url`, `thumbnail_url`, `scale`, `offset_x/y`, `native_width/height`, タイスタンプ
  - `static_sites` — `id` VARCHAR(10)（PK）, `user_id` UUID, `title`, `description`, `status`, `sfsp_job_id`, `minio_path`, `entry_point_path`（デフォルト`index.html`）, `thumbnail_url`, タイスタンプ
- **インデックス** — `sfsp_job_id`・`thumbnail_sfsp_job_id`・`uploader_id`/`user_id`ごとに作成
- **自動更新トリガー** — 各テーブルに`updated_at`をUPDATE時に自動更新するトリガー（関数`update_updated_at_column()`）
- **ロール: `sfsp_worker`** — 読み取り専用（`videos`・`games`・`static_sites`へのSELECTのみ）。パスワードは`.env`の`SFSP_WORKER_DB_PASSWORD`（デフォルト`sfsp_worker_password`）と一致。SFSPワーカーがジョブのメタデータだけ確認时使用。

#### profile-db（`profile_db`）— プロフィール情報

初期化ファイル: `backend/profile-service/profile-db/init.sql`

- **テーブル: `users`**
  - `id` UUID（PK）, `username`, `bio` TEXT, `icon_url` TEXT, `background_image_url` TEXT, `icon_sfsp_job_id` UUID, `background_sfsp_job_id` UUID, `status`（デフォルト`offline`）, `created_at`, `updated_at`
- **テストデータ** — 初回起動時に`user_atomos`（UUIDv7形式）が`ON CONFLICT (id) DO NOTHING`で挿入。実環境では不要なら削除可能。

#### team-db（`team_db`）— チーム・コンテンツ共有

初期化ファイル: `backend/team-service/team-db/init.sql`

Discordの招待リンク風、トークン（URL）ベースのクローズドコンテンツ共有を管理します。`gen_random_uuid()`（`pgcrypto`）を使用。

- **テーブル**
  - `teams` — チーム。`id` UUID（PK）、`token` VARCHAR(24)（UNIQUE、base36・crypto/randで生成・約143ビットの熵）、`name` VARCHAR(255)（NOT NULL）、`description` TEXT、`is_public` BOOLEAN（デフォルト`false`）、`auto_approve` BOOLEAN（デフォルト`false`）、`allow_member_invite` BOOLEAN（デフォルト`false`）、`created_by` UUID、`created_at` タイスタンプ
  - `team_members` — チーム所属。`id` UUID（PK）、`team_id`（`teams.id`へのFK、`ON DELETE CASCADE`）、`user_id` UUID、`role`（`owner`/`admin`/`member`、デフォルト`member`）、`joined_at` タイスタンプ。`UNIQUE(team_id, user_id)`
  - `team_content` — 既存コンテンツ（動画・ゲーム・static-site）をチームに紐付けるためのリンク表（Phase 2用）。`id` UUID（PK）、`team_id`（`teams.id`へのFK）、`content_type`、`content_id`、`created_at`
  - `team_posts` — チーム原生の投稿（Phase 1）。`id` UUID（PK）、`team_id`（`teams.id`へのFK、`ON DELETE CASCADE`）、`author_id` UUID、`title` VARCHAR(255)、`body` TEXT、`created_at` タイスタンプ
- **ロール: `team_service_user`** — `teams`・`team_members`・`team_content`・`team_posts`へのSELECT/INSERT/UPDATE/DELETE。パスワードは`.env`の`TEAM_SERVICE_DB_PASSWORD`と一致。`team_posts`の`author_id`から著者名を取得する場合は`app-db`の`users`も読み取り。

#### sfsp-db（`sfsp_db`）— スキャン平台

初期化ディレクトリ: `backend/security/sfsp-db/`（`001_sfsp_initial_schema.sql`）

ファイルスキャンの全履歴を管理します。`uuid-ossp`拡張を使用。

- **テーブル**
  - `files` — スキャン対象ファイルのメタデータ。`id` UUID, `filename`, `filesize` BIGINT, `mime_type`, `sha256`（UNIQUEなし＝重複アップロード許可）, `storage_path`, `file_type`（`video`/`html`/`zip`/`other`）, `target_service`（`video`/`game`/`static_site`/`other`等）, `created_at`
  - `scan_jobs` — ジョブ管理。`id` UUID, `file_id`（`files.id`へのFK、`ON DELETE CASCADE`）, `status`（`queued`/`running`/`clean`/`suspicious`/`malicious`/`failed`/`invalid`）, `processing_details`, `is_cleaned_up` BOOLEAN（クリーンアップフラグ）, `cleaned_up_at`, タイスタンプ
  - `scan_results` — スキャン結果詳細。`id` UUID, `job_id`（`scan_jobs.id`へのFK、`ON DELETE CASCADE`）, `scanner`（`clamav`/`yara`）, `result`（`clean`/`suspicious`/`malicious`/`error`）, `details`, `raw_output` JSONB, `scanned_at`
- **インデックス** — `sha256`・`file_id`・`job_id`に加え、クリーンアップ対象を高速検索する複合インデックス`idx_scan_jobs_cleanup(status, is_cleaned_up)`
- **トリガー** — `scan_jobs`の`updated_at`を自動更新

---

## MinIO ストレージ

Atomosideaは**1つのMinIOインスタンス**で全オブジェクトストレージを統合します。イメージ `minio/minio:latest`、`restart: always`、データはプロジェクト内のホストディレクトリ`backend/storage_data`にバインドマウントして保存されます。APIポート`9000`とコンソールポート`9001`を`atmosidea-network`と`sfsp-isolated-net`の両方に公開します。

全バケットを1インスタンス内で**バケット単位**に分離し、種別・スキャン状態別の論理分離を実現します。各コンテンツ種別（プロフィール・動画・ゲーム・static-site）とSFSP（Raw/Clean）は同じMinIO内の別バケットとして配置されます。

### MinIOインスタンス一覧

| サービス名 | コンテナ名 | コンソール | ボリューム | 匿名アクセス |
|---|---|---|---|---|
| `minio` | `atmosidea-minio` | `:9001` | `./backend/storage_data`（バインドマウント） | —（全バケット統合） |

- 認証は`MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD`（`.env`、デフォルト`minioadmin`/`minioadminpassword`）。
- `MINIO_USE_SSL`は`false`（内部通信は平文・内部ネットワーク限定）。
- コンソールは`http://localhost:9001`で閲覧（APIの9000とは別ポート）。
- healthcheckはMinIOのライブエンドポイント（`/minio/health/live`）を`curl`で確認します。MinIOイメージには`mc`クライアントは同梱されていません。

### バケットの中身（実ディレクトリ構造）

MinIOはバケットごとにデータディレクトリ内にサブフォルダを作成します。例:

```
backend/storage_data/
├── .minio.sys/            # MinIO内部メタデータ（自動生成・手動編集禁止）
├── user-profiles/         # プロフィール画像
├── static-sites/          # 静的サイトの配信ファイル（＋サムネイル: `thumbnails/<id>`キー）
├── videos/                # HLS変換後の動画（.m3u8, .ts）（＋サムネイル: `thumbnails/<id>`キー）
├── games/                 # ゲームファイル
├── raw-files/             # スキャン前のオリジナル（SFSP）
├── clean-files/           # スキャン通過ファイル（SFSP）
└── quarantine/            # 悪性・疑義ファイルの隔離先（SFSP）
```

- `.minio.sys/` はMinIOが管理する内部ディレクトリ（設定・バケットメタデータ・プール情報）。**手動での削除・編集は不可**。
- 各バケット内には対応するコンテンツが格納されます（例：`videos/` にHLSのセグメント（`.ts`）とプレイリスト（`.m3u8`））。
- **サムネイルは専用バケットではありません。** 動画サムネイルは`videos`バケット内の`thumbnails/<id>`キー、static-siteサムネイルは`static-sites`バケット内の`thumbnails/<id>`キーに格納されます。`minio-init`で`thumbnails`バケットは作成されません。

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
|---|---|---|
| `raw-files` | スキャン前のオリジナルファイル（汚染可能性あり） |
| `clean-files` | スキャン通過ファイルのみ |
| `quarantine` | 悪性・疑義ファイルの隔離先 |

---

## IAMポリシーと認証キー

SFSP用MinIOは、アップロードAPI・SFSP API・各ワーカーごとに**専用のIAMユーザーとポリシー**でアクセスを細分化します。すべては1つのMinIO内で管理され、各ユーザーは`minio-init`起動時に作成され、ポリシーが割り当てられます。

### キーと役割の対応

| Access Key（`.env`） | 使用サービス | 権限（ポリシー） | 対象バケット・操作 |
|---|---|---|---|
| `SFSP_UPLOAD_*` | 各upload API、profile-service | `sfsp-upload-policy` | `raw-files` に**PutObject**（スキャン用アップロード） |
| `SFSP_API_*` | `sfsp-api` | `sfsp-api-policy` | `raw-files` に**全操作**＋バケット一覧（ジョブ作成・保存） |
| `SFSP_WORKER_RAW_*` | `sfsp-worker` | `sfsp-worker-raw-policy` | `raw-files` から**GetObject/DeleteObject/ListBucket**（スキャン用取り込み・後処理削除） |
| `SFSP_WORKER_CLEAN_*` | `sfsp-worker` | `sfsp-worker-clean-policy` | `clean-files`・`quarantine`・`raw-files` に**全操作**＋バケット管理（Clean書込・Raw削除） |
| `SFSP_GAME_WORKER_*` | `game-worker` | `sfsp-game-worker-policy` | `clean-files/*` から**GetObject**（展開用ダウンロード） |
| `SFSP_VIDEO_WORKER_*` | `video-worker` | `sfsp-video-worker-policy` | `clean-files/*` から**GetObject**（HLS変換用ダウンロード） |
| `SFSP_STATIC_SITE_WORKER_*` | `static-site-worker` | `sfsp-static-site-worker-policy` | `clean-files/*` から**GetObject**（展開用ダウンロード） |
| `SFSP_PROFILE_WORKER_*` | `profile-service` | `sfsp-profile-worker-policy` | バケット一覧＋`clean-files/*` から**GetObject**（スキャン後アイコン取得） |

### ポリシー設計のポイント

- **Uploadユーザーは「書き込み専用」** — `raw-files`へのPutObjectのみ。読み取り不可により、アップロード側からスキャン結果への不正な干渉を防止。
- **Workerは「取り込み→書込み→削除」を1人が担当** — `sfsp-worker`がRawから読み取り、Cleanへ書き込み、処理済みRawを削除するフルサイクルを持つ。これによりスキャンパイプラインが完結。
- **各コンテンツワーカーは「Cleanからの読み取り専用」** — ゲーム・動画・static-site・profileのワーカーは`clean-files/*`のGetObjectのみ。アップロードしたRawにはアクセスしない。
- **APIは「Rawへの全操作」** — ジョブ作成とファイル保存のため`raw-files`に全権限。

ポリシーファイル: `backend/minio-init/` 配下の`sfsp-*-policy.json`。

---

## MinIO自動初期化（minio-init）

`minio-init`サービスは`minio/mc:latest`イメージを使用し、`restart: "no"`（再起動しない）で**初回のみ**実行されるバッチ処理です。

**役割:**

1. **エイリアス設定** — 単一のMinIOに`mc alias set`で接続情報を設定（MinIOが健康になるまでループで待機）。
2. **バケット作成** — `mc mb --ignore-existing`で全バケットを作成（既存は無視）。
3. **匿名アクセス設定** — アプリ用バケットに`mc anonymous set download`を適用。
4. **IAMポリシー作成** — `mc admin policy create`で8種類のポリシーをMinIOへ登録。
5. **IAMユーザー作成・割り当て** — `mc admin user add`で各サービス用ユーザーを作成し、`mc admin policy attach`でポリシーを紐付け。

**依存関係:**

- `minio`が`service_healthy`後に起動。
- `auth-service`・`profile-service`・各upload API・各worker・`sfsp-api`が`minio-init`の`service_completed_successfully`を待機。これにより、サービスがバケット/ポリシーの準備完了後に起動保証される。

`mc`（MinIO Client）のエントポイントは`/bin/sh -c`で、全プロセスを1つのスクリプトで実行します。

---

## データ永続化と運用

### ボリュームとデータ失効

- **DB** はDockerの名付きボリューム、**MinIO** はプロジェクト内のホストディレクトリ`backend/storage_data`（バインドマウント）で保持されます。
- **DB** — コンテナを削除してもボリュームは残ります。`clean.bat`等で完全削除しない限りデータは維持。
- **MinIO** — `backend/storage_data`に書き込まれます。このディレクトリを削除すると全オブジェクトが消えます。
- `.minio.sys/` はMinIO内部状態。**手動削除するとメタデータが破損**し、バケットが inaccessible に。

### 運用コマンド

- **MinIOコンソール確認** — `http://localhost:9001`へ、`.env`のROOTユーザー/パスワードでログイン。
- **バケット・オブジェクト確認** — `mc`コマンドまたはコンソールから閲覧。
- **データリセット** — `backend/storage_data`内を削除後、`docker compose up -d`でMinIOを再起動（バケットは`minio-init`で再作成）。
- **DBリセット** — `docker volume rm <volume_name>`（例: `auth_db_data`）後、`docker compose up -d`で`init.sql`が再実行。

### 変更時の注意点

- **バケット名・キー名・環境変数名**を変更した場合は、`docker-compose.yml`・各サービスの環境変数・`minio-init`のエントポイント・ポリシーJSONを対で更新。
- **IAMポリシー**を変更した場合は、対応する`backend/minio-init/*-policy.json`と`minio-init`のエントポイント（`mc admin policy create`/`attach`）を対で更新。
- **新しいバケット**を追加した場合は、`minio-init`の`mc mb`と匿名設定、および必要に応じてIAMポリシーへ反映。

---

## 関連ファイル一覧

### データベース初期化

| ファイル | 役割 |
|---|---|
| `backend/auth/auth-datebase/auth-db/init.sql` | auth-dbのスキーマ・ロール・デフォルト管理者 |
| `backend/datebase/app-db/init.sql` | app-dbの3テーブル・インデックス・トリガー・sfsp_worker権限 |
| `backend/profile-service/profile-db/init.sql` | profile-dbのusersテーブル・テストデータ |
| `backend/team-service/team-db/init.sql` | team-dbのteams/team_members/team_join_requests/team_content/team_postsスキーマ |
| `backend/security/sfsp-db/001_sfsp_initial_schema.sql` | sfsp-dbのfiles/scan_jobs/scan_resultsスキーマ |

### MinIO初期化・ポリシー

| ファイル | 役割 |
|---|---|
| `backend/minio-init/initialize.sh` | バケット・匿名設定・IAM作成のスクリプト（docker-composeのエントポイントに埋め込み） |
| `backend/minio-init/sfsp-upload-policy.json` | upload API・profile-serviceのraw-filesへのPutObject |
| `backend/minio-init/sfsp-api-policy.json` | sfsp-apiのraw-filesへの全操作 |
| `backend/minio-init/sfsp-worker-raw-policy.json` | sfsp-workerのraw-filesからの読み取り・削除 |
| `backend/minio-init/sfsp-worker-clean-policy.json` | sfsp-workerのclean-files/quarantineへの書き込み・raw削除 |
| `backend/minio-init/sfsp-game-worker-policy.json` | game-workerのclean-filesからの読み取り |
| `backend/minio-init/sfsp-video-worker-policy.json` | video-workerのclean-filesからの読み取り |
| `backend/minio-init/sfsp-static-site-worker-policy.json` | static-site-workerのclean-filesからの読み取り |
| `backend/minio-init/sfsp-profile-worker-policy.json` | profile-serviceのclean-filesからの読み取り |

### MinIOデータ（ホストディレクトリ・バインドマウント）

| ディレクトリ | 担当MinIO | バケット |
|---|---|---|
| `backend/storage_data` | `atmosidea-minio` | 全バケット（`user-profiles`・`static-sites`・`videos`・`games`・`raw-files`・`clean-files`・`quarantine`）。サムネイルは専用バケットではなく`videos`/`static-sites`内の`thumbnails/<id>`キーに格納 |