# Atomosidea — 完全技術仕様書（現行版）

このドキュメントは、`Atomosidea`（アトモシデア）プロジェクトの**現在の実装状態**を正確に記述したものではありません。本文中のパス・テーブル名・キュー名・サービス名は、すべて `docker-compose.yml` および実際のディレクトリ構造（`backend/` 配下）と照合した上での記述です。コードとドキュメントに相違が見つかれば、まず `docker-compose.yml` と `backend/` の実ディレクトリを正として優先してください。

**目的:** このドキュメントを読むだけで、プロジェクトの知識がない開発者（またはAI）が、システムの全体像と「なぜそうなっているのか（設計思想）」、「どこが誤情報になりやすいか（過去のドキュメントとの乖離）」、「変更時に絶対に壊してはいけない制約」を完全に理解できることを目的とします。

**絶対遵守（AIは必ずこれを読み守ること）:**
- 本文中の内容は絶対に消去しないでください。基本的に内容を追記していきます。
- 大規模な消去を行う場合は必ずユーザーの許可を取ってください。
- このドキュメントは「現在地」を記録します。過去の変更履歴（チャンジログ）を積み重ねる形式は廃止し、常に単一の現行状態を記述します。
- 実装パス・環境変数名・DBスキーマ・Redisキュー名・APIレスポンスを変更した場合は、必ず本节の対応箇所も合わせて更新してください。

---

## 目次

1. [プロジェクト概要](#1-プロジェクト概要)
2. [技術スタック](#2-技術スタック)
3. [アーキテクチャ全体](#3-アーキテクチャ全体)
4. [サービス一覧（コンテナ・ポート・依存）](#4-サービス一覧コンテナポート依存)
5. [データベース（4基のPostgreSQL）](#5-データベース4基のpostgresql)
6. [オブジェクトストレージ（6基のMinIO）](#6-オブジェクトストレージ6基のminio)
7. [ディレクトリ構造と主要ファイル](#7-ディレクトリ構造と主要ファイル)
8. [共有パッケージ（shared）](#8-共有パッケージshared)
9. [主要なワークフロー](#9-主要なワークフロー)
10. [APIエンドポイント一覧](#10-apiエンドポイント一覧)
11. [Nginx（APIゲートウェイ）の規約と設計思想](#11-nginxapiゲートウェイの規約と設計思想)
12. [認証・認可の仕様](#12-認証認可の仕様)
13. [セキュリティモデル（SFSP + JWT + iframe sandbox）](#13-セキュリティモデルsfsp--jwt--iframe-sandbox)
14. [セットアップ・開発フロー](#14-セットアップ開発フロー)
15. [管理用スクリプト](#15-管理用スクリプト)
16. [トラブルシューティングとエラー対策](#16-トラブルシューティングとエラー対策)
17. [AIが変更を行う際のチェックリスト](#17-aiが変更を行う際のチェックリスト)

---

## 1. プロジェクト概要

`Atomosidea`は、**動画共有・Unity WebGLゲーム配信・静态サイト（Static Site）ホスティング・ユーザープロフィール**を統合した多機能デジタルコンテンツプラットフォームです。

マイクロサービスアーキテクチャを採用し、各機能が独立したGoサービスとして開発・運用されます。すべてのサービスはDocker Composeで統合管理され、Nginxが単一の入り口（APIゲートウェイ＋静的配信）を担当します。

**主な価値:**
- クリエイターが動画・ゲーム・ウェブサイトを1つのプラットフォームで安全に配信・管理できる。
- 各サービスが独立しているため、負荷が高い機能だけスケールアウトできる。
- アップロードファイルは専用セキュリティプラットフォーム（SFSP）によりマルwaresスキャンされ、プラットフォームが保護される。

**ターゲットユーザー:**
- コンテンツクリエイター（動画制作者、ゲーム開発者、ウェブデザイナー）
- 一般ユーザー（動画の視聴、ゲームのプレイ、ウェブサイトの閲覧）

---

## 2. 技術スタック

| カテゴリ | 技術 | 選定理由・役割 |
|---|---|---|
| **フロントエンド** | React, Vite, TypeScript | モダンで高速なUI開発。型安全な大規模開発に対応。 |
| | MUI (@mui/material, @mui/icons-material) | 高品質なUIコンポーネント・アイコンの迅速な構築。 |
| | Axios | 堅牢なHTTPリクエスト処理。 |
| | React Router DOM | SPAのルーティング管理。 |
| | hls.js | HLS動画をブラウザで再生。 |
| | react-image-crop | アイコン・背景画像のクロップ（切り抜き）。 |
| **バックエンド** | Go 1.25 | 高性能・並行処理・静的型付け。マイクロサービスに適す。 |
| | Gin | 高速なHTTPルーター・ミドルウェア。 |
| | pgx/v5 (jackc) | PostgreSQLクライアント。プレースホルダ使用でSQLインジェクション対策。 |
| | minio-go/v7 | S3互換ストレージ（MinIO）クライアント。 |
| | go-redis/v8 | Redisクライアント（キュー・キャッシュ）。 |
| | golang-jwt/v5 | JWT発行・検証。 |
| **データベース** | PostgreSQL 16 | 高機能・信頼性のリレーショナルDB。トランザクション整合性。4基分割。 |
| | Redis 7 | インメモリDB。キュー（スキャンジョブ・完了イベント）とJWTブロックリスト。 |
| **ストレージ** | MinIO | S3互換オブジェクトストレージ。動画・ゲーム・プロフィール・サイトファイルを管理。6基分割。 |
| **コンテナ** | Docker, Docker Compose | 開発/本番の差異排除。ポータビリティ・再現性。 |
| **セキュリティ** | ClamAV, YARA | アンチウイルスエンジン・マルウェアパターン検出。Docker Sandbox内で実行。 |
| **動画処理** | FFmpeg | 動画のHLS変換・サムネイル生成。 |
| **監視** | monitoring-service | 運用・監視ダッシュボード（Dockerソケット経由）。 |

---

## 3. アーキテクチャ全体

Atomosideaは、Docker Composeで管理される**19のサービス群**で構成されます。すべては3つのネットワークに分割されています。

```
[ユーザー] → [ブラウザ] → [Nginx (frontend:80, ホスト3001)]
    │
    ├─ /api/auth/**        → auth-service        → auth-db
    ├─ /api/profile/**     → profile-service     → profile-db, profile-storage
    ├─ /api/my/**          → mypage-service      → auth-db, app-db
    ├─ /api/videos/**      → video-upload-api    → app-db, sfsp-api, video-storage
    │                        video-worker        → app-db, video-storage, sfsp-clean-minio
    ├─ /api/games/**       → game-upload-api     → app-db, sfsp-api, game-storage
    │                        game-worker         → app-db, sfsp-clean-minio, game-storage
    ├─ /api/static-sites/** → static-site-upload-api → app-db, sfsp-api, static-site-storage
    │                        static-site-worker    → app-db, sfsp-clean-minio, static-site-storage
    │
    ├─ (非同期) Redis ← 全upload-api ↔ 全worker
    │
    └─ (セキュリティ) 各upload-api → sfsp-api → sfsp-worker → ClamAV/YARA(Sandbox)
                          ↓
                    sfsp-raw-minio → sfsp-clean-minio (clean/quarantine)
```

### 3つのネットワーク
- **atmosidea-network**: メインアプリ全体（frontend、各API/worker、Redis、各MinIO、auth/profile等）。
- **backend-db-network**: データベース接続（auth-db、app-db、profile-db、sfsp-db、Redis、各サービス）。
- **sfsp-isolated-net**: **セキュリティ分離ネットワーク**。SFSP関連サービス（sfsp-api、sfsp-worker、sfsp-raw-minio、sfsp-clean-minio）のみが接続。外部からのアクセスを遮断し、スキャン処理を隔離。

> **設計思想:** SFSP（セキュリティファイルスキャンプラットフォーム）はマルウェアを処理するため、他サービスから隔離されたネットワークに配置されます。ただし処理対象コンテンツの公開先（game-storage等）へはアクセス必要があるため、SFSP関連サービスは `atmosidea-network` にも参加します（`sfsp-worker` が app-db / game-storage にアクセスするため）。

---

## 4. サービス一覧（コンテナ・ポート・依存）

ビルド元はすべて `context: .`（プロジェクトルート）で、各 `Dockerfile` が `COPY . .` 後に `WORKDIR /app/<パス>` でビルドします（理由: 12章・17章参照）。

| サービス名 | コンテナ名 | 内部ポート | ビルド元 (Dockerfile) | 実装ディレクトリ | 責務 |
|---|---|---|---|---|---|
| `frontend` | atmosidea-frontend | 80 (ホスト3001) | `./frontend` | `frontend/` | NginxでReactを配信＋APIゲートウェイ＋HLS配信 |
| `auth-service` | atmosidea-auth-service | 8080 | `backend/auth/auth-worker/Dockerfile` | `backend/auth/auth-worker/` | 認証（ローカル+Google OAuth）、JWT、アカウント削除 |
| `profile-service` | atmosidea-profile-service | 8084 | `backend/profile-service/profile-worker/Dockerfile` | `backend/profile-service/profile-worker/` | プロフィール管理＋アイコン/背景のSFSPスキャン |
| `mypage-service` | atmosidea-mypage-service | 8083 | `backend/profile-service/mypage-worker/Dockerfile` | `backend/profile-service/mypage-worker/` | マイページ（投稿コンテンツ一覧） |
| `video-upload-api` | atmosidea-video-upload-api | 8080 | `backend/video-service/video-upload-api/Dockerfile` | `backend/video-service/video-upload-api/` | 動画アップロード受付＋SFSPスキャン依頼 |
| `video-worker` | atmosidea-video-worker | 8081 | `backend/video-service/video-worker/Dockerfile` | `backend/video-service/video-worker/` | 動画のHLS変換・サムネイル生成・MinIO保存 |
| `game-upload-api` | atmosidea-game-upload-api | 8082 | `backend/game-service/game-upload-api/Dockerfile` | `backend/game-service/game-upload-api/` | ゲームZIPアップロード受付＋SFSPスキャン依頼 |
| `game-worker` | atmosidea-game-worker | - | `backend/game-service/game-worker/Dockerfile` | `backend/game-service/game-worker/` | ゲームZIP展開・解像度抽出・MinIOデプロイ |
| `static-site-upload-api` | atmosidea-static-site-upload-api | 8085 | `backend/static-site-service/static-site-upload-api/Dockerfile` | `backend/static-site-service/static-site-upload-api/` | 静态サイトZIPアップロード受付＋SFSPスキャン依頼 |
| `static-site-worker` | atmosidea-static-site-worker | - | `backend/static-site-service/static-site-worker/Dockerfile` | `backend/static-site-service/static-site-worker/` | 静态サイトZIP展開・MinIOデプロイ |
| `sfsp-api` | sfsp-api | 8080 | `backend/security/sfsp/docker/api/Dockerfile` | `backend/security/sfsp/` | ファイル受け付け・SHA256重複排除・ジョブキューイング |
| `sfsp-worker` | sfsp-worker | - | `backend/security/sfsp/docker/worker/Dockerfile` | `backend/security/sfsp/` | ClamAV/YARAスキャン（Docker Sandbox内） |
| `sfsp-clamav-client` | sfsp-clamav-client-builder | - | `backend/security/sfsp/docker/clamav-client/Dockerfile` | `backend/security/sfsp/` | ClamAVスキャナイメージ（build-only） |
| `sfsp-yara-client` | sfsp-yara-client-builder | - | `backend/security/sfsp/docker/yara-client/Dockerfile` | `backend/security/sfsp/` | YARAスキャナイメージ（build-only） |

> **補足 (過去ドキュメントとの乖離に注意):** 旧EditAI.mdは `backend/auth/auth-worker/auth-service/` と `backend/auth/auth-worker/profile-service/` を別ディレクトリとして記載していますが、**現行は単一ファイル構成**です。`auth-service` は `backend/auth/auth-worker/main.go` 1ファイル、`profile-service` は `backend/profile-service/profile-worker/main.go` 1ファイル、`mypage-service` は `backend/profile-service/mypage-worker/main.go` 1ファイルが正です。

> **補足:** `video-worker` は動画処理ワーカーと動画ストリーミングAPIを同一ビルドで担当します（`/api/videos` 一覧・詳細・ストリーミングを処理）。

---

## 5. データベース（4基のPostgreSQL）

すべて `postgres:16`。内部接続のみ（ポート公開なし）。`backend-db-network` に接続。

| コンテナ | DB名 | 初期化スクリプト | 役割 |
|---|---|---|---|
| `auth-db` | `auth_db` | `backend/auth/auth-datebase/auth-db/init.sql` | ユーザー情報・プロフィール状態・認証情報 |
| `app-db` | `app_db` | `backend/datebase/app-db/init.sql` | 動画・ゲーム・静态サイトのメタデータ |
| `profile-db` | `profile_db` | `backend/profile-service/profile-db/init.sql` | プロフィール詳細（bio、アイコンURL等） |
| `sfsp-db` | `sfsp_db` | `backend/security/sfsp-db/` | スキャン結果・ジョブ・ファイルメタデータ |

> **注意:** 旧ドキュメントでは `postgres/init.sql` 等と記載されていますが、現行の実際のパスは上記の通りです。`app-db` の初期化スクリプトは `backend/datebase/app-db/` （`datebase` は誤記ではなく実ディレクトリ名）に存在します。

### 主要テーブル

**auth-db.users**
| カラム | 型 | 説明 |
|---|---|---|
| `id` | UUID, PK | ユーザーID |
| `username` | VARCHAR | ユーザー名 |
| `email` | VARCHAR | メールアドレス |
| `password_hash` | VARCHAR | パスワードハッシュ（ローカル認証用） |
| `provider` | VARCHAR | `'local'` または `'google'` |
| `provider_id` | VARCHAR | GoogleのユーザーID |
| `is_admin` | BOOLEAN | 管理者フラグ |
| `status` | VARCHAR | `'active'` / `'deleted_data'` |

**profile-db.users**
| カラム | 型 | 説明 |
|---|---|---|
| `id` | UUID, PK | ユーザーID |
| `username` | VARCHAR | ユーザー名 |
| `bio` | TEXT | 自己紹介 |
| `icon_url` | TEXT | アイコン画像URL |
| `background_image_url` | TEXT | 背景画像URL |
| `icon_sfsp_job_id` | UUID | アイコンのSFSPジョブID |
| `background_sfsp_job_id` | UUID | 背景画像のSFSPジョブID |
| `status` | VARCHAR | `'offline'` / `'active'` / `'pending'` 等 |
| `created_at` / `updated_at` | TIMESTAMP | 作成/更新日時 |

**app-db.videos**
| カラム | 型 | 説明 |
|---|---|---|
| `id` | VARCHAR(10), PK | 動画ID（ランダム英数字） |
| `title` | VARCHAR | タイトル |
| `description` | TEXT | 説明 |
| `filename` | VARCHAR | HLSプレイリストパス |
| `thumbnail_path` | VARCHAR | サムネイルパス |
| `uploader_id` | UUID | アップロード者ID |
| `status` | VARCHAR | `'scanning'`/`'processing'`/`'public'`/`'error'`/`'quarantined'` |
| `sfsp_job_id` | UUID | SFSPジョブID |

**app-db.games**
| カラム | 型 | 説明 |
|---|---|---|
| `id` | VARCHAR(10), PK | ゲームID（ランダム英数字） |
| `user_id` | UUID | アップロード者ID |
| `title` | VARCHAR | タイトル |
| `status` | VARCHAR | 処理状態（videosと同様） |
| `game_url` | VARCHAR | ゲームURL |
| `thumbnail_url` | VARCHAR | サムネイルURL |
| `native_width` / `native_height` | INT | ゲームのネイティブ解像度 |
| `scale` / `offset_x` / `offset_y` | - | ユーザー調整値 |
| `sfsp_job_id` | UUID | SFSPジョブID |

**app-db.static_sites**
| カラム | 型 | 説明 |
|---|---|---|
| `id` | VARCHAR(10), PK | サイトID（ランダム英数字） |
| `user_id` | UUID | アップロード者ID |
| `title` | VARCHAR | タイトル |
| `status` | VARCHAR | 処理状態 |
| `entry_point_path` | VARCHAR | エントリーポイント（index.html）のパス |
| `sfsp_job_id` | UUID | SFSPジョブID |

**sfsp-db.files / scan_jobs / scan_results**
- **files**: `id`(UUID), `filename`, `filesize`(BIGINT), `mime_type`, `sha256`, `storage_path`, `file_type`(`video`/`zip`), `target_service`(`stream`/`game`/`static-site`/`profile`), `created_at`
- **scan_jobs**: `id`(UUID), `file_id`(FK), `status`(`queued`/`running`/`completed`/`failed`/`invalid`), `processing_details`, `is_cleaned_up`, `cleaned_up_at`, `created_at`, `updated_at`
- **scan_results**: `id`(UUID), `job_id`(FK), `scanner`(`clamav`/`yara`), `result`(`clean`/`suspicious`/`malicious`/`error`), `details`, `raw_output`(JSONB), `scanned_at`

---

## 6. オブジェクトストレージ（6基のMinIO）

すべて `minio/minio:latest`。コンソールは各々別ポート。`atmosidea-network` に接続（SFSP関連は `sfsp-isolated-net` も）。

| コンテナ | コンソール | バケット | 物理ディレクトリ | 役割 |
|---|---|---|---|---|
| `profile-storage` | :9000 | `user-profiles` | `backend/auth/auth-storage/profile_storage_data` | プロフィール画像（アイコン・背景） |
| `game-storage` | :9000 | `games` | `backend/game-service/game_storage_data` | ゲームアセット |
| `static-site-storage` | :9000 | `static-sites` | `backend/static-site-service/static_site_storage_data` | 静态サイトファイル |
| `video-storage` | :9003 | `videos`, `thumbnails` | `backend/video-service/video_storage_data` | 動画HLS・サムネイル |
| `sfsp-raw-minio` | :9000 | `raw-files` | `backend/security/sfsp-storage/sfsp_raw_minio_data` | スキャン前ファイル（隔離） |
| `sfsp-clean-minio` | :9000 | `clean-files`, `quarantine` | `backend/security/sfsp-storage/sfsp_clean_minio_data` | スキャン後ファイル・隔離先（隔離） |

> **注意:** 旧ドキュメントではバケット名が `profile-storage` の場合 `user-profiles`、`game-storage` が `games` 等と混在していましたが、現行は `docker-compose.yml` の `minio-init` の `mc mb` 処理が正です。`video-storage` のみコンソールポートが `9003`、他は `9000`（コンテナ内ポート）です。

### MinIOバケット自動初期化（minio-init）
`minio/mc` イメージ。`restart: "no"`（一度だけ実行）。各MinIOが起動した後、バケット作成 (`mc mb`)・匿名ダウンロードポリシー設定 (`mc anonymous set download`)・SFSP用ユーザー/ポリシー作成 (`mc admin user add` / `mc admin policy attach`) を実行します。`depends_on` で各MinIOの `service_started` を待機するため、起動競合による権限エラーは発生しません。

---

## 7. ディレクトリ構造と主要ファイル

```
.
├── backend/
│   ├── auth/
│   │   ├── auth-worker/            # auth-service (main.go 1ファイル)
│   │   │   ├── Dockerfile
│   │   │   ├── go.mod / go.sum
│   │   │   └── main.go
│   │   ├── auth-datebase/          # auth-db 初期スクリプト
│   │   │   └── auth-db/init.sql
│   │   ├── auth-db/                # (旧・互換ディレクトリ)
│   │   └── auth-storage/           # profile-storage の永続化
│   │       └── profile_storage_data/
│   ├── profile-service/
│   │   ├── profile-worker/         # profile-service (main.go 1ファイル)
│   │   │   ├── Dockerfile / go.mod / go.sum / main.go
│   │   │   └── placeholder.txt
│   │   ├── mypage-worker/          # mypage-service (main.go 1ファイル)
│   │   │   ├── Dockerfile / go.mod / go.sum / main.go
│   │   │   └── placeholder.txt
│   │   ├── profile-db/             # profile-db 初期スクリプト
│   │   │   └── init.sql
│   │   └── profile_storage_data/   # profile-storage の永続化
│   ├── video-service/
│   │   ├── video-upload-api/       # 動画アップロードAPI
│   │   ├── video-worker/           # 動画処理ワーカー＋ストリーミングAPI（同一ビルド）
│   │   ├── video_storage_data/     # video-storage の永続化
│   │   └── upload_workdir/         # ローカル作業ボリューム（/storageへマウント）
│   ├── game-service/
│   │   ├── game-upload-api/        # ゲームアップロードAPI
│   │   ├── game-worker/            # ゲーム処理ワーカー
│   │   └── game_storage_data/      # game-storage の永続化（バケット: games）
│   ├── static-site-service/
│   │   ├── static-site-upload-api/ # 静态サイトアップロードAPI
│   │   ├── static-site-worker/     # 静态サイト処理ワーカー
│   │   └── static_site_storage_data/  # static-site-storage の永続化（バケット: static-sites）
│   ├── security/
│   │   └── sfsp/                   # セキュリティプラットフォーム（SFSP）
│   │       ├── cmd/                # sfsp-api / sfsp-worker のエントリポイント
│   │       ├── internal/           # api, config, database, model, queue, sandbox, scanner, storage, worker
│   │       ├── docker/             # api/worker/clamav-client/yara-client のDockerfile
│   │       ├── yara-rules/         # general.yar
│   │       ├── sfsp-db/            # sfsp-db 初期スキーマ
│   │       ├── sfsp-storage/       # sfsp-raw/clean-minio の永続化
│   │       ├── docs/               # ADR.md, Runbook.md, Threat_Model.md, Incident_Guide.md
│   │       ├── reports/ scripts/ test-samples/ scanners/
│   │       └── sandbox/
│   ├── minio-init/                 # SFSP用MinIOポリシーJSON
│   │   ├── sfsp-upload-policy.json
│   │   ├── sfsp-api-policy.json
│   │   ├── sfsp-game-worker-policy.json
│   │   ├── sfsp-video-worker-policy.json
│   │   ├── sfsp-static-site-worker-policy.json
│   │   ├── sfsp-profile-worker-policy.json
│   │   ├── sfsp-worker-raw-policy.json
│   │   └── sfsp-worker-clean-policy.json
│   ├── datebase/                   # app-db 初期スクリプト（注: "datebase" は実ディレクトリ名）
│   │   └── app-db/init.sql
│   └── shared/                     # サービス間共有パッケージ (github.com/atmosidea/shared)
│       ├── config/ config.go
│       ├── event/ event.go         # ScanCompletionEvent定義
│       ├── model/ model.go
│       └── queue/ queue.go         # キュー名定義（sfsp:scan_queue, sfsp:completed:*）
├── frontend/                       # React/Vite/TypeScript + Nginx
│   ├── src/
│   │   ├── App.tsx / main.tsx / index.css / theme.ts
│   │   ├── components/             # Header, PrivateRoute, ImageCropperModal, AccountDeletionModal
│   │   ├── context/                # AuthContext
│   │   └── pages/                  # 16ページ（後述）
│   ├── nginx.conf                  # HLS配信も担うNginx設定
│   ├── Dockerfile / index.html / package.json / tsconfig.json / vite.config.ts
│   └── dist/
├── monitoring-service/             # 運用・監視ダッシュボード
│   ├── backend/ / frontend/
│   ├── docker-compose.monitoring.yml
│   └── nginx.conf
├── docker-compose.yml              # 全サービスの構成定義（正）
├── .env.example                    # 環境変数テンプレート
├── .env                            # 実環境変数（gitignore）
├── db.sql
├── EditAI.md                       # 本ドキュメント
├── SECURITY_EXPLANATION.md         # セキュリティ設計説明書
├── README.md                       # 完全ガイド＆技術仕様書
└── *.bat                           # 管理用スクリプト
```

### フロントエンドページ（16）
`HomePage`, `LoginPage`, `LoginSuccessPage`, `RegisterPage`, `MyPage`, `VideoDetailPage`, `GameDetailPage`, `StaticSiteDetailPage`, `UploadPage`, `UploadGamePage`, `UploadStaticSitePage`, `EditVideoPage`, `EditGamePage`, `EditStaticSitePage`, `EditProfilePage`, `AdjustGamePage`

---

## 8. 共有パッケージ（shared）

`backend/shared/` は `github.com/atmosidea/shared` というモジュール名で、全Goサービスが共有します。

- **config/**: 環境変数の読み込み（Viper）。
- **event/**: `ScanCompletionEvent` の定義（スキャン完了イベントの構造）。
- **model/**: 共有モデル定義。
- **queue/**: キュー名定義（`sfsp:scan_queue`, `sfsp:completed:stream`, `sfsp:completed:game`, `sfsp:completed:static-site`, `sfsp:completed:profile`）。

### Goの依存関係とDockerビルドの重要制約（必ず守ること）

1. **`replace` ディレクティブ**: 全Goサービスの `go.mod` 末尾に `replace github.com/atmosidea/shared => ../../shared` が記述されています。これにより、GitHubへの通信が発生せず、ローカルの `shared` を直接参照します。
   - ビルドはルートコンテキストで `COPY . .` した後に `WORKDIR /app/backend/<サービス>` 移動するため、`../../shared` で `backend/shared` が解決されます。全サービスで同一の `../../shared` を使用します。
   - **AIは新しいGoサービスを追加・修正する際、必ずこの `replace` を正しく追記してください。** 忘れると `exit status 128`（GitHub認証エラー）でビルドが失敗します。

2. **Dockerビルドの方式**: 全サービスのビルド `context` はプロジェクトルート `.` です。`Dockerfile` は `COPY . .` でプロジェクト全体（`shared` 含む）をコピーした後、`WORKDIR /app/<サービスパス>` に移動してビルドします。これにより `../shared` の相対パスが解決されます。
   - **AIは `Dockerfile` の `COPY . .` と `WORKDIR` の組み合わせ、およびビルドコンテキストをルートに指定することを破換しないでください。**
   - **`Dockerfile` 内の `RUN go mod tidy` は削除済み**（コンテナ内ではGitHubへ通信するため）。ローカルで `go.sum` を揃え、コンテナ内では `RUN go mod download` だけを実行します。

3. **Finalイメージ**: `alpine:latest` を使用。`ca-certificates` と `tzdata` を追加し、HTTPS通信とタイムゾーンに対応しています。

---

## 9. 主要なワークフロー

### 9.1. ユーザー認証フロー
1. ユーザーがログイン情報を入力 → `frontend` が `/api/auth/login` にPOST。
2. Nginx が `auth-service` に転送。
3. `auth-service` が `auth-db` のユーザー情報を検証（パスワードは `bcrypt` で比較）。
4. 成功すれば JWT（ペイロードに `userID`, `username`, `isAdmin`）を生成・返却。
5. `frontend` がJWTを `localStorage` に保存し、以降のAPIリクエストの `Authorization: Bearer <token>` ヘッダーに付与。

> **Google OAuth:** 一般ユーザーの新規登録・ログインはGoogle OAuth2が主流。`/api/auth/google/login` で開始、`/api/auth/google/callback` でコールバック処理。初回ログイン時、`auth-service` は `users` を `status: 'pending'` で作成し、`profile-service` に初期プロフィール作成を依頼します。フロントエンドは `status` が `pending` の場合、`/edit-profile` へ強制リダイレクトします。

### 9.2. ゲームアップロードと処理フロー（最重要・非同期）
1. **アップロード**: ユーザーが `frontend` からゲームZIPをアップロード。
2. **API受付**: `frontend` → `/api/games/upload` → Nginx → `game-upload-api`。
3. **一次保存とJob発行**:
   - `game-upload-api` がZIPを `sfsp-api` (`/api/v1/files`, `target_service: 'game'`) に転送。
   - `sfsp-api` がSHA256を計算し、`sfsp-db` で重複チェック。新規なら `sfsp-raw-minio/raw-files` に保存し、`scan_jobs` にレコード作成後、Redis `sfsp:scan_queue` にジョブIDをプッシュ。
   - `game-upload-api` が `app-db.games` に `status: 'scanning'` でレコード作成。
   - `frontend` に `gameId` を返し、`AdjustGamePage` へリダイレクト。
4. **SFSPスキャン**: `sfsp-worker` が `sfsp:scan_queue` からジョブを取得 → `sfsp-raw-minio` からZIPをDL → **Docker Sandbox内でClamAV/YARAスキャン** → 結果を `scan_results` に保存 → `clean` なら `sfsp-clean-minio/clean-files` にコピー → Redis `sfsp:completed:game` に `ScanCompletionEvent` を発行。
5. **非同期処理**: `game-worker` が `sfsp:completed:game` からイベントを取得 → `clean` のみ処理 → `sfsp-clean-minio` からZIPをDL → 展開・`index.html`解析でネイティブ解像度抽出 → CSS注入 → `game-storage/games` に再アップロード → `app-db.games` を `status: 'public'` に更新。
6. **ポーリングと表示**: `AdjustGamePage` が `status` を `'public'` になるまで `/api/games/{id}` をポーリング。`public` になったら `game_url` と解像度でプレビュー表示。

### 9.3. 動画アップロードと処理フロー
1. `video-upload-api` がZIPではない動画ファイルとメタデータを `sfsp-api` に転送 → `sfsp:scan_queue` にジョブ。
2. `app-db.videos` に `status: 'scanning'` でレコード作成。
3. `sfsp-worker` がスキャン → `clean` なら `sfsp:completed:stream` にイベント発行。
4. `video-worker` がイベントを取得 → `sfsp-clean-minio` から動画をDL → **FFmpegで360p/480p/720p/1080pのHLSに変換** → サムネイル生成 → ローカル作業ディレクトリ（`upload_workdir`）で処理 → `video-storage/videos` と `video-storage/thumbnails` にアップロード → `app-db.videos` を `status: 'public'` に更新。
5. **HLS再生**: `VideoDetailPage` が `/api/videos/{id}/stream/playlist.m3u8` をリクエスト → Nginx がローカルファイル `/storage/videos/{id}/...` にマッピング（`alias`）→ hls.js が再生。

> **ローカル作業ディレクトリ（`upload_workdir`）:** `video-upload-api` は読み書き用（`/storage`）、`video-worker` は読み取り専用（`/storage:ro`）でマウント。処理完了後HLSをMinIOへ展開し、ローカルの一時ファイルは削除。データはMinIOに永続化されるため、このディレクトリの一時内容は失われても自動再生成され、動作に影響しません。

### 9.4. 静态サイトアップロードと処理フロー
1. `static-site-upload-api` がZIPを `sfsp-api` に転送 → `sfsp:scan_queue`。
2. `app-db.static_sites` に `status: 'scanning'` でレコード作成。
3. SFSPスキャン → `sfsp:completed:static-site` にイベント。
4. `static-site-worker` がイベントを取得 → `sfsp-clean-minio` からZIPをDL → 展開（`index.html` を再帰探索してルート特定）→ `static-site-storage/static-sites` に `{siteId}/` プレフィックスで再アップロード → `app-db.static_sites` を `status: 'public'` に更新（`entry_point_path` を保存）。
5. **表示**: サブドメイン `http://{siteId}.localhost:3001/{entry_point_path}` で配信（Nginx が `static-site-storage` にプロキシ）。これによりメインアプリとオリジン分離（セキュリティ強化）。

### 9.5. SFSPスキャンの全体像（セキュリティの中核）
```
upload-api ──POST /api/v1/files──▶ sfsp-api ──SHA256重複排除──▶ sfsp-raw-minio/raw-files
        └─────────────────────────────────────────────────────▶ Redis sfsp:scan_queue
                                                                ▼
sfsp-worker ◀──BlockingPOP── Redis sfsp:scan_queue
   │  Docker Sandbox内で ClamAV + YARA スキャン
   ▼
sfsp-clean-minio (clean→clean-files / malicious→quarantine)
   ▼
Redis sfsp:completed:{stream,game,static-site,profile}
   ▼
各worker が clean イベントのみ処理 → 公開
```

---

## 10. APIエンドポイント一覧

### Auth Service (`auth-service`) — ベースパス `/api/auth`
| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `POST` | `/register` | 不要 | 管理者アカウント登録（`ADMIN_REGISTRATION_CODE` 必要） |
| `POST` | `/login` | 不要 | ローカルログイン（JWT返却） |
| `GET` | `/google/login` | 不要 | Google OAuth開始 |
| `GET` | `/google/callback` | 不要 | Google OAuthコールバック |
| `POST` | `/logout` | JWT | JWTをRedisブロックリストに追加して無効化 |
| `DELETE` | `/me` | JWT | アカウントと関連コンテンツを完全削除 |
| `GET` | `/user/:userId` | JWT | ユーザーの認証プロバイダ取得 |

### Profile Service (`profile-service`) — ベースパス `/api/profile`
| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `GET` | `/me` | JWT | 自分のプロフィール取得 |
| `GET` | `/:userId` | 不要 | 特定ユーザーの公開プロフィール |
| `GET` | `/status` | JWT | 自分のアカウント状態取得 |
| `PUT` | `` | JWT | プロフィール（名前・自己紹介）更新 |
| `PUT` | `/icon` | JWT | アイコン更新（SFSPスキャン後） |
| `PUT` | `/background` | JWT | 背景画像更新（SFSPスキャン後） |
| `POST` | `/internal/create` | 内部 | auth-serviceからの初期プロフィール作成 |

### Video Upload API & Stream Service — ベースパス `/api/videos`
| メソッド | エンドポイント | サービス | 認証 | 説明 |
|---|---|---|---|---|
| `POST` | `/upload` | video-upload-api | JWT | 動画アップロード |
| `DELETE` | `/delete/:id` | video-upload-api | JWT | 動画削除 |
| `GET` | `` | video-worker | 不要 | 動画リスト（`q=` で検索） |
| `GET` | `/:id` | video-worker | 不要 | 動画詳細 |
| `PUT` | `/:id` | video-worker | JWT | 動画メタデータ更新 |
| `GET` | `/:id/stream/playlist.m3u8` | video-worker | 不要 | HLSストリーミング（Nginxがaliasで配信） |

### Game Upload API — ベースパス `/api/games`
| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `POST` | `/upload` | JWT | ゲームアップロード（SFSPスキャン＋Job発行） |
| `GET` | `` | 不要 | ゲームリスト（`q=` で検索） |
| `GET` | `/:id` | 不要 | ゲーム詳細 |
| `PUT` | `/:id` | JWT | ゲームメタデータ更新 |
| `PUT` | `/adjust/:id` | JWT | ゲームの表示調整値（scale/offset）保存 |
| `DELETE` | `/:id` | JWT | ゲーム削除 |

### Static Site Upload API — ベースパス `/api/static-sites`
| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `POST` | `/upload` | JWT | 静态サイトアップロード（SFSPスキャン＋Job発行） |
| `GET` | `` | 不要 | 静态サイトリスト（`q=` で検索） |
| `GET` | `/:id` | 不要 | 静态サイト詳細 |
| `PUT` | `/:id` | JWT | 静态サイトメタデータ更新 |
| `DELETE` | `/:id` | JWT | 静态サイト削除 |

### MyPage Service (`mypage-service`) — ベースパス `/api/my`
| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `GET` | `/videos` | JWT | 自分の動画一覧 |
| `GET` | `/games` | JWT | 自分のゲーム一覧 |
| `GET` | `/static-sites` | JWT | 自分の静态サイト一覧 |

### SFSP API (`sfsp-api`) — ベースパス `/api/v1`
| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `POST` | `/files` | 内部 | ファイルスキャン依頼 |
| `GET` | `/jobs/:id` | 内部 | ジョブステータス確認 |
| `GET` | `/results/:id` | 内部 | スキャン結果取得 |
| `GET` | `/health` | 不要 | ヘルスチェック |

---

## 11. Nginx（APIゲートウェイ）の規約と設計思想

`frontend` コンテナ内のNginxが、システム全体の入り口（APIゲートウェイ＋静的ファイル配信）を担当します。

### 11.1. APIゲートウェイとしての役割
- **静的コンテンツ**: `/` 等、API以外のパスにはビルドされたReactアプリ（`index.html`）を返す。
- **動的コンテンツ**: `/api/...` へのリクエストをパスに応じてバックエンドサービスに転送。

### 11.2. `proxy_pass`の規約: 末尾のスラッシュ（最重要）
**`proxy_pass` のURL末尾にスラッシュを付けません。**
```nginx
# 正しい例
location /api/auth {
    proxy_pass http://auth_service;   # 末尾スラッシュなし
}
```
- **理由**: 末尾スラッシュなし → リクエストURIがそのまま渡される（`GET /api/auth/me` → `http://auth_service/api/auth/me`）。各Goサービスが `/api/auth` プレフィックスを含めてルーティングできる。
- 末尾スラッシュを付けると、`location` でマッチした部分が削られ `/me` だけが送られ、Goのルーティングが壊れる。**現在の設計はこの規約に依存しているため変更禁止。**

### 11.3. ヘッダー転送
すべての `proxy_pass` ブロックに以下を含める:
```nginx
proxy_set_header Host $http_host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

### 11.4. `location`ブロックの評価順序
1. **`=` (完全一致)**: 競合しない特定パスに使用。
2. **`~` (正規表現)**: パス内に動的IDが含まれる場合に使用（例: `/api/videos/([0-9]+)`）。
3. **プレフィックス (修飾子なし)**: 特定プレフィックスで始まる全リクエストを捕捉。

### 11.5. `if`文の例特的使用（技術的負債・注意）
```nginx
location ~ ^/api/videos/([0-9]+)$ {
    if ($request_method = DELETE) {
        proxy_pass http://video_upload_service;
        break;
    }
    proxy_pass http://video_worker_service;
}
```
- 同じURLで `GET`（video-worker）と `DELETE`（video-upload-api）が担当サービスが異なるため、暫定的に `if` でメソッドを判定。**「if is evil」** として知られ予期せぬ挙動の原因となるため、将来的には削除専用エンドポイントへのリファクタリングが望ましい。AIが動画関連のルーティングを変更する際は、この `if` の影響を必ず確認してください。

### 11.6. HLS配信（alias）
```nginx
location ~ ^/api/videos/([0-9]+)/stream/(.*)$ {
    alias /storage/videos/$1/$2;   # proxy_pass ではなく alias
    # MIMEタイプ、CORS、CSP (media-src 'self' blob:)
}
```
- 動画ファイルはMinIOではなく**ローカルファイルシステム**（`./frontend/nginx.conf` 経由で `video-storage` の `./backend/video-service/video_storage_data`）に配置されるため、`alias` でマッピングします。`proxy_pass` ではない点に注意。

### 11.7. 静态サイトのサブドメイン分離
```nginx
server {
    server_name ~^([a-z0-9]{10})\.localhost$;   # 引用符で囲む
    set $site_id $1;
    # MinIOはディレクトリインデックス不支持 → rewrite で index.html を付加
    # Permissions-Policy / CSP を強化
}
```
- 静态サイトをサブドメイン `http://{siteId}.localhost:3001/` で配信し、メインアプリとオリジン分離。これにより `iframe sandbox` のセキュリティを強化。
- **注意**: `server_name` の正規表現は引用符で囲む（バックスラッシュ含むため）。AIがNginx設定を編集後は、必ず `docker-compose restart frontend` で反映し、構文エラーが出ていないか確認してください。

---

## 12. 認証・認可の仕様

### 12.1. JWT方式
- トークンは `localStorage` に保存（Cookieベースのセッション管理ではない）。
- 各サービスが同じ `JWT_SECRET` を保持し、署名を検証。無効/期限切れなら `401 Unauthorized`。

### 12.2. 削除・編集時の認可
- **フロントエンドの責務**: `DELETE`/`PUT` 時に `Authorization: Bearer <token>` を付与。
- **バックエンドの責務**: `authMiddleware` がJWTを検証し `userID`/`isAdmin` をセット。各ハンドラがコンテンツの `uploader_id` と一致するか（または `isAdmin`）を検証。不满足なら `403 Forbidden`。

### 12.3. アカウント削除フロー
`DELETE /api/auth/me` が実行されると:
1. `auth-db` のユーザーレコードを論理削除（`status: 'deleted_data'`）または物理削除（ローカルアカウント）。
2. 関連サービスからプロフィール画像、動画、ゲーム、静态サイトを物理削除。
3. 最後に `auth-db` のユーザーレコードを物理削除。

---

## 13. セキュリティモデル（SFSP + JWT + iframe sandbox）

詳細は `SECURITY_EXPLANATION.md` を参照。本節では要点のみ。

### 13.1. 多層防御（Defense in Depth）
認証・認可・入力検証・インフラ保護の複数の防御層。最小権限・Secure by Default・Zero Trustを原則。

### 13.2. SFSPサンドボックス
`sfsp-worker` はスキャナを隔離されたDockerコンテナ内で実行:
- **`ReadonlyRootfs: true`**: ファイルシステム読み取り専用。
- **読み取り専用マウント (`:ro`)**: スキャン対象ファイルを変更不可。
- **リソース制限**: メモリ2GB・CPU1制限（ZIP爆弾等DoS対策）。
- **`tmpfs`**: ClamAVが一時ファイルを書き込めるよう `/run/clamav` 等をRAMマウント。

### 13.3. JWT + localStorage のトレードオフ（重要）
- **強み**: CSRF攻撃を原理的に阻止（Cookieの自動送信に依存しない）。
- **リスク**: XSSが1箇所でも刺さると `localStorage` のJWTが一瞬で盗まれる。
- **対策**: Reactの自動エスケープ、厳格なCSP、依存関係の監査（`npm audit`）。
- **iframe sandbox**: アップロード静态サイトを `sandbox` 付きiframeで表示。ただしカメラ機能等のため `allow-same-origin` を許可（サブドメイン分離で補強）。

### 13.4. その他の対策
- **SQLインジェクション**: プレースホルダ（`$1` 等）のみの使用。
- **XSS**: Reactのデフォルトエスケープ。`dangerouslySetInnerHTML` の使用回避。
- **機密情報**: すべて `.env` 管理（`.gitignore`）。ハードコーディング禁止。
- **Dockerセキュリティ**: 軽量イメージ（alpine）、ネットワーク分離（不要なポート公開なし）。

---

## 14. セットアップ・開発フロー

### 14.1. 前提要件
- Docker
- Docker Compose

### 14.2. 環境変数の設定
1. `.env.example` をコピーして `.env` を作成。
2. 各項目（`POSTGRES_PASSWORD`, `JWT_SECRET`, 各種MinIOキー等）を設定。**シークレットキーはランダム文字列に変更。**
3. Google OAuth利用時は `GOOGLE_CLIENT_ID` と `GOOGLE_CLIENT_SECRET` を設定。

### 14.3. 起動手順
1. `setup.bat` で初回セットアップ＋ビルド＋起動。
2. 2回目以降は `start.bat` で起動、`stop.bat` で停止。
3. `http://localhost:3001` にアクセス。
4. （任意）監視ダッシュボード: `monitoring-service` 内で `docker-compose -f docker-compose.monitoring.yml up -d --build` → `http://localhost:8090`。
5. ログ: `docker-compose logs -f <service_name>`。

### 14.4. 日常の開発サイクル
1. コード変更後、`update.bat` で全サービスをビルド・起動（完全再起動）。
2. ブラウザで `http://localhost:3001` を確認。

---

## 15. 管理用スクリプト

| スクリプト | 説明 |
|---|---|
| `setup.bat` | 初回セットアップ。Google OAuth情報を対話的に設定し `.env` を生成後、全サービスをビルド・起動。 |
| `start.bat` | 通常起動（`docker-compose up -d`）。 |
| `update.bat` | 完全再起動（全コンテナ停止後、キャッシュを使わない完全ビルド）。変更反映用。 |
| `stop.bat` | 通常停止（`docker-compose down`）。 |
| `clean.bat` | 完全クリーンアップ（コンテナ・ネットワーク・全ボリューム・イメージ削除）。**データ全消失。** |
| `cleanup_games.bat` | ゲームデータ（DBレコード＋MinIOファイル）のみ削除。 |
| `cleanup_videos.bat` | 動画・サムネイルファイルのみ削除（DBレコードは残る）。 |
| `cleanup_static_sites.bat` | 静态サイトデータ（DBレコード＋MinIOファイル）のみ削除。 |
| `create_go_work.bat` | Goワークスペース設定。 |

---

## 16. トラブルシューティングとエラー対策

### 16.1. 一般的な問題
- **サービスが起動しない**: `docker-compose logs <service_name>` で確認。環境変数のミスや依存サービス（DB等）の起動失敗が主因。
- **ファイルがアップロードできない**: `video-upload-api` / `sfsp-api` のログを確認。SFSP利用不可またはMinIO接続失敗。
- **コンテンツが処理されない**: `sfsp-worker` / 各worker のログを確認。Redis接続、スキャンプロセス、FFmpegエラー。
- **コンテンツが表示されない**: `frontend` のNginx設定、各storageのバケットポリシー、ファイルパスを確認。監視ダッシュボードのストレージブラウザで確認可能。

### 16.2. 特殊な問題
- **動画処理のローカル作業ディレクトリ（`upload_workdir`）が存在しない**: 問題なし。Dockerが空ディレクトリを自動生成。データはMinIOに永続化。
- **MinIOアップロードの一時的エラー（`Access Denied`等）**: 自動的にリトライ（最大5回）。恒久的に失敗する場合はバケットポリシー/認証キー/ `VIDEO_MINIO_*` 環境変数を確認。
- **Goビルドの `exit status 128`（GitHub認証エラー）**: `go.mod` の `replace github.com/atmosidea/shared => ...` が不足。正しく追記。
- **`replacement directory ../shared does not exist`**: Dockerビルドコンテキストがルート以外。`COPY . .` と `WORKDIR` の組み合わせを確認。
- **Nginx構文エラー（`server_name`）**: 正規表現を引用符で囲む（例: `server_name ~^([a-z0-9]{10})\.localhost$;`）。
- **HLS再生エラー（`メディアを利用できません`）**: `streamUrl` が `/api/videos/{id}/stream/playlist.m3u8` になっているか、Nginxの `alias` ブロックが存在するか、CSPに `media-src 'self' blob:` が含まれているか確認。

---

## 17. AIが変更を行う際のチェックリスト

本プロジェクトで変更を行う前必ず確認してください:

1. **正のソースは `docker-compose.yml` と `backend/` の実ディレクトリ**。ドキュメント（本編・README・旧EditAI.md）と相違があれば、実装を優先。
2. **パス・テーブル名・キュー名・環境変数名**は、変更後に本ドキュメントの対応箇所を必ず更新。
3. **Goサービス追加/修正**: `go.mod` に正しい `replace github.com/atmosidea/shared => ...` を追記。`Dockerfile` の `COPY . .` と `WORKDIR` とビルドコンテキスト（ルート）を破換しない。`RUN go mod tidy` を追加しない。
4. **Nginx設定編集後**: 必ず `docker-compose restart frontend` で反映し、構文エラーを確認。末尾スラッシュ無しの `proxy_pass` 規約を遵守。
5. **SF相关政策/ユーザー変更**: `docker-compose.yml` の `minio-init` エントポイントと `backend/minio-init/*.json` を対で更新。
6. **DBスキーマ変更**: 対応するDBの `init.sql` を更新（`auth-db`/`app-db`/`profile-db`/`sfsp-db`）。
7. **JWT/iframe sandbox関連**: `localStorage` のリスクと `sandbox` 属性のトレードオフを理解した上で変更。
8. **大規模な消去を行う場合は必ずユーザーの許可を取得**（本ドキュメントの絶対遵守事項）。

---

> 本ドキュメントは「現在地」を記録します。実装を変更した場合は、本节の対応箇所を合わせて更新してください。詳細なセキュリティ設計は `SECURITY_EXPLANATION.md` を、運用監視は `monitoring-service/` を参照してください。