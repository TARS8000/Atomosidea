# Atomosidea — AI 向け技術仕様書（現行版）

このドキュメントは、`Atomosidea`（アトモシデア）の**現在の実装**を、AI がコードを変更するときに参照できる形で記述したものです。
記載しているパス・テーブル名・キュー名・環境変数名・ポートは、`docker-compose.yml` と `backend/` 以下の実ファイルと照合済みです（最終照合: 2026-10-26、既知の問題への対応後）。

---

## 0. このドキュメントの使い方（AI は最初に必ず読むこと）

### 0.1. 絶対遵守のルール

1. **本文の内容を消去しない。** 基本的に追記・修正で更新する。
2. **大規模な消去を行う場合は、必ずユーザーの許可を取る。**
3. このドキュメントは「現在地」を記録する。変更履歴（チェンジログ）は積み重ねず、常に単一の現行状態を記述する。
   - 古い記述が誤りだと分かった場合は、本文を正しい内容に直したうえで、元の誤った記述を [14. よくある誤解](#14-よくある誤解旧ドキュメントとの差分) に残す。
4. 実装パス・環境変数名・DB スキーマ・Redis キュー名・API レスポンスを変更したら、**このドキュメントの対応箇所も同じ変更で更新する。**

### 0.2. 情報の優先順位

ドキュメントとコードが食い違ったときは、次の順に信頼する。

1. `docker-compose.yml`（サービス・ネットワーク・環境変数・ボリュームの正）
2. `backend/` 以下の実コードと `init.sql`
3. このドキュメント（`EditAI.md`）
4. `README.md`、`AI_PROJECT_GUIDE.md`、`Docs/*.md`、`SECURITY_EXPLANATION.md`

### 0.3. 目的別の参照先

| やりたいこと | 見る節 |
|---|---|
| どのサービスがどのファイルにあるか | [4. サービス一覧](#4-サービス一覧) |
| テーブル・カラムを確認したい | [5. データベース](#5-データベース) |
| バケット・MinIO ユーザーを確認したい | [6. オブジェクトストレージ（MinIO）](#6-オブジェクトストレージminio) |
| キュー名・イベント形式を確認したい | [7. Redis と共有パッケージ](#7-redis-と共有パッケージshared) |
| アップロード処理を変更したい | [8. 処理フロー](#8-処理フロー) |
| エンドポイントを追加・変更したい | [9. API 一覧](#9-api-一覧) と [10. Nginx](#10-nginxapi-ゲートウェイ) |
| 認証・権限を変更したい | [11. 認証・認可](#11-認証認可) |
| スキャンやサンドボックスを変更したい | [12. セキュリティモデル](#12-セキュリティモデル) |
| ビルドが失敗する | [13. ビルドと依存関係の制約](#13-ビルドと依存関係の制約) と [16. トラブルシューティング](#16-トラブルシューティング) |
| 変更前の最終確認 | [17. 変更時のチェックリスト](#17-変更時のチェックリスト) |

---

## 目次

0. [このドキュメントの使い方](#0-このドキュメントの使い方ai-は最初に必ず読むこと)
1. [プロジェクト概要](#1-プロジェクト概要)
2. [技術スタック](#2-技術スタック)
3. [アーキテクチャ全体](#3-アーキテクチャ全体)
4. [サービス一覧](#4-サービス一覧)
5. [データベース](#5-データベース)
6. [オブジェクトストレージ（MinIO）](#6-オブジェクトストレージminio)
7. [Redis と共有パッケージ（shared）](#7-redis-と共有パッケージshared)
8. [処理フロー](#8-処理フロー)
9. [API 一覧](#9-api-一覧)
10. [Nginx（API ゲートウェイ）](#10-nginxapi-ゲートウェイ)
11. [認証・認可](#11-認証認可)
12. [セキュリティモデル](#12-セキュリティモデル)
13. [ビルドと依存関係の制約](#13-ビルドと依存関係の制約)
14. [よくある誤解（旧ドキュメントとの差分）](#14-よくある誤解旧ドキュメントとの差分)
15. [既知の問題（未対応）](#15-既知の問題未対応)
16. [トラブルシューティング](#16-トラブルシューティング)
17. [変更時のチェックリスト](#17-変更時のチェックリスト)
18. [セットアップと管理用スクリプト](#18-セットアップと管理用スクリプト)

---

## 1. プロジェクト概要

- **何か**: 動画共有・Unity WebGL ゲーム配信・静的サイトホスティング・ユーザープロフィール・チームを統合したコンテンツプラットフォーム。
- **構成**: 機能ごとに独立した Go サービス（マイクロサービス）を Docker Compose で動かす。入口は `frontend` コンテナの Nginx だけ。
- **公開までの原則**: アップロードされたファイルは、必ず SFSP（Secure File Scanning Platform）でスキャンされ、`clean` と判定されたものだけが公開される。
- **利用者**: コンテンツクリエイター（動画制作者・ゲーム開発者・Web デザイナー）と閲覧者。

---

## 2. 技術スタック

| 分類 | 技術 | 用途 |
|---|---|---|
| フロントエンド | React, Vite, TypeScript | SPA |
| | MUI (`@mui/material`, `@mui/icons-material`) | UI コンポーネント |
| | Axios | HTTP。`AuthContext.tsx` のインターセプターが `Authorization` を付与 |
| | React Router DOM | ルーティング |
| | hls.js | HLS 再生 |
| | jwt-decode | JWT のデコード（期限切れ判定・ユーザー情報） |
| | react-image-crop | アイコン・背景画像の切り抜き |
| バックエンド | Go 1.25 | 全サービス |
| | Gin | HTTP ルーター（監視サービスのみ gorilla/mux + gorilla/websocket） |
| | pgx/v5 | PostgreSQL（プレースホルダ `$1` を使用。static-site-upload-api / static-site-worker は lib/pq） |
| | minio-go/v7 | MinIO |
| | go-redis/v8 | Redis |
| | golang-jwt/jwt/v5 | JWT（HS256） |
| データ | PostgreSQL 16 ×5 | `auth-db` / `app-db` / `profile-db` / `team-db` / `sfsp-db` |
| | Redis 7 | ジョブキュー・完了イベント・JWT ブロックリスト |
| | MinIO ×1 | オブジェクトストレージ（バケットとユーザーで分離） |
| セキュリティ | ClamAV, YARA | アップロードのスキャン（Docker サンドボックス内） |
| 動画 | FFmpeg | HLS 変換・サムネイル生成（`video-upload-api` 内で実行） |
| 監視 | monitoring-service | Docker ソケット経由のダッシュボード（別 compose） |

---

## 3. アーキテクチャ全体

### 3.1. リクエストの流れ

```
[ブラウザ] ──:3001──▶ [frontend: Nginx]
   │
   ├─ /                         → React（/usr/share/nginx/html）
   ├─ /api/auth/**              → auth-service:8080
   ├─ /api/profile/**           → profile-service:8084
   ├─ /api/my/**                → mypage-service:8083
   ├─ /api/teams/**             → team-service:8080
   ├─ /api/games/**             → game-upload-api:8082
   ├─ /api/static-sites/**      → static-site-upload-api:8085
   ├─ /api/videos/upload        → video-upload-api:8080
   ├─ /api/videos/delete/:id    → video-upload-api:8080（/api/videos/:id に書き換え）
   ├─ /api/videos/:id/thumbnail → video-upload-api:8080（サムネイル削除）
   ├─ /api/videos/:id/stream/*  → MinIO videos/<id>/*（HLS）
   ├─ /api/videos/**            → video-worker:8081
   ├─ /videos|games|static-sites/thumbnails/*, /user-profiles/* → MinIO
   └─ /games/<id>/*             → 404（メインオリジンでは配信しない）

[ブラウザ] ──:3001, Host: <10桁id>.localhost──▶ [frontend: Nginx サブドメイン用 server]
   └─ /* → MinIO games/<id>/*、無ければ static-sites/<id>/*
```

### 3.2. 非同期処理（スキャン → 公開）

```
アップロード API ──POST /api/v1/files──▶ sfsp-api ──▶ MinIO raw-files
                                            └──LPUSH──▶ Redis sfsp:scan_queue
sfsp-worker ◀──BRPOPLPUSH── sfsp:scan_queue（→ sfsp:scan_queue:processing）
   │ zip を上限付きで展開 → 展開したディレクトリ全体を ClamAV(-r) + YARA(-r) でスキャン（サンドボックス）
   ├─ clean                → MinIO clean-files
   ├─ malicious/suspicious → MinIO quarantine
   └─ LPUSH ScanCompletionEvent ──▶ sfsp:completed:<target> → 終端状態の記録とイベント発行の後に Ack
各ワーカー ◀──BRPOPLPUSH── sfsp:completed:<target>（→ :processing。clean のときだけ公開処理、終端状態を DB に書いてから Ack）
```

- 取り出しは `backend/shared/queue` の `DequeueReliable` / `Ack` / `RecoverProcessing`（SFSP は `internal/queue` に同じもの）。起動時に `RecoverProcessing` で `<queue>:processing` に残ったメッセージを元のキューに戻す。各キューのコンシューマーは 1 つである前提。

### 3.3. ネットワーク（`docker-compose.yml` より）

| ネットワーク | 接続しているサービス |
|---|---|
| `atmosidea-network`（外向き通信あり） | redis, minio, minio-init, frontend, auth-service, profile-service, team-service, mypage-service, video-upload-api, video-worker, game-upload-api, game-worker, static-site-upload-api, static-site-worker |
| `backend-db-network`（`internal: true`） | auth-db, app-db, profile-db, team-db, db-role-init, auth-service, profile-service, team-service, mypage-service, 各アップロード API / ワーカー, sfsp-worker（app-db 用） |
| `sfsp-isolated-net`（`internal: true`） | redis, minio, minio-init, 各アップロード API / ワーカー, profile-service, sfsp-api, sfsp-worker |
| `sfsp-db-network`（`internal: true`） | sfsp-db, sfsp-api, sfsp-worker のみ |
| `sfsp-docker-net`（`internal: true`） | sfsp-docker-proxy, sfsp-worker のみ |

- `internal: true` のネットワークは外へ出られない。Google OAuth など外部通信が必要なサービスは `atmosidea-network` にも参加させる。
- ネットワーク定義を変えた後は `docker compose down` → `up -d` で作り直す必要がある（そのままだと「network needs to be recreated」で失敗する）。
- ホストに公開されているポートは `frontend` の `3001:80` だけ。DB・Redis・MinIO はホストに公開していない。
- スキャナーを実行するサンドボックスのコンテナは `NetworkMode: none`（ネットワークなし）。

---

## 4. サービス一覧

ビルドは `context: .`（プロジェクトルート）で、各 Dockerfile が `COPY . .` の後 `WORKDIR /app/<サービスのパス>` でビルドする（理由は [13 章](#13-ビルドと依存関係の制約)）。

| サービス | コンテナ名 | 内部ポート | 実装（`main.go` の場所） | 責務 |
|---|---|---|---|---|
| `frontend` | atmosidea-frontend | 80（ホスト 3001） | `frontend/`（`nginx.conf` はボリュームでマウント） | React 配信、API ゲートウェイ、MinIO へのプロキシ |
| `auth-service` | atmosidea-auth-service | 8080 | `backend/auth/auth-worker/` | 登録・ログイン・Google OAuth・JWT・ログアウト・アカウント削除 |
| `profile-service` | atmosidea-profile-service | 8084 | `backend/profile-service/profile-worker/` | プロフィール CRUD、画像のスキャン依頼、`sfsp:completed:profile` の処理 |
| `mypage-service` | atmosidea-mypage-service | 8083 | `backend/profile-service/mypage-worker/` | 自分の投稿一覧 |
| `team-service` | atmosidea-team-service | 8080 | `backend/team-service/team-worker/` | チーム・メンバー・加入申請・投稿・権限確認 |
| `video-upload-api` | atmosidea-video-upload-api | 8080 | `backend/video-service/video-upload-api/` | 動画アップロード、**`sfsp:completed:stream` の処理（FFmpeg で HLS 変換）**、動画削除 |
| `video-worker` | atmosidea-video-worker | 8081 | `backend/video-service/video-worker/` | 動画の一覧・詳細・更新 API、`sfsp:completed:video-thumbnail` の処理 |
| `game-upload-api` | atmosidea-game-upload-api | 8082 | `backend/game-service/game-upload-api/` | ゲームのアップロード・一覧・詳細・更新・調整・削除 |
| `game-worker` | atmosidea-game-worker | — | `backend/game-service/game-worker/` | `sfsp:completed:game` / `game-thumbnail` の処理 |
| `static-site-upload-api` | atmosidea-static-site-upload-api | 8085 | `backend/static-site-service/static-site-upload-api/` | 静的サイトのアップロード・一覧・詳細・更新・削除 |
| `static-site-worker` | atmosidea-static-site-worker | — | `backend/static-site-service/static-site-worker/` | `sfsp:completed:static-site` / `site-thumbnail` の処理 |
| `sfsp-api` | sfsp-api | 8080 | `backend/security/sfsp/cmd/sfsp-api/`、`internal/api/` | スキャン依頼の受付、ジョブ登録 |
| `sfsp-worker` | sfsp-worker | — | `backend/security/sfsp/cmd/sfsp-worker/`、`internal/worker/` | スキャン実行、結果イベントの発行、公開後のクリーンアップ |
| `sfsp-clamav-client` | sfsp-clamav-client-builder | — | `backend/security/sfsp/docker/clamav-client/` | ClamAV イメージ（`sfsp-clamav-client:latest`）のビルド専用 |
| `sfsp-yara-client` | sfsp-yara-client-builder | — | `backend/security/sfsp/docker/yara-client/` | YARA イメージ（`sfsp-yara-client:latest`）のビルド専用 |
| `sfsp-scan-volume-init` | sfsp-scan-volume-init | — | docker-compose.yml（`sfsp-yara-client` イメージを root で一時利用） | 起動のたびに 1 回だけ動き、スキャン用ボリューム `sfsp_temp_scan` の所有者を 10001 に揃える（非 root 化前に作られた既存ボリューム用）。`CHOWN` / `FOWNER` / `DAC_READ_SEARCH` のみ許可、ネットワークなし、読み取り専用ルート。`sfsp-worker` はこの完了を待つ |
| `sfsp-docker-proxy` | sfsp-docker-proxy | 2375 | `tecnativa/docker-socket-proxy`（compose で定義） | sfsp-worker 用の Docker API プロキシ。`CONTAINERS` / `IMAGES` / `POST` / `PING` / `VERSION` のみ許可 |
| `db-role-init` | atmosidea-db-role-init | — | `backend/db-init/sync-role-passwords.sh` | 起動のたびに 1 回だけ動き、`.env` の値で DB ロールのパスワードを `ALTER ROLE` する |
| `auth-db` ほか DB | atmosidea-*-db / sfsp-db | 5432 | 各 `init.sql` | [5 章](#5-データベース) |
| `redis` | atmosidea-redis | 6379 | — | [7 章](#7-redis-と共有パッケージshared) |
| `minio` | atmosidea-minio | 9000 | データ: `./backend/storage_data` | [6 章](#6-オブジェクトストレージminio) |
| `minio-init` | atmosidea-minio-init | — | `docker-compose.yml` の entrypoint、`backend/minio-init/*.json` | バケット・ポリシー・ユーザーの作成（起動のたびに 1 回実行） |

監視ダッシュボード（別 compose: `monitoring-service/docker-compose.monitoring.yml`）

| サービス | コンテナ名 | ポート | 実装 |
|---|---|---|---|
| `mon-nginx` | mon-nginx | `127.0.0.1:8090:80` | `monitoring-service/nginx.conf` |
| `mon-backend` | mon-backend | 8080 | `monitoring-service/backend/main.go`（Docker ソケットを `:ro` でマウント。全ルートで HTTP Basic 認証） |
| `mon-frontend` | mon-frontend-builder | — | `monitoring-service/frontend/`（ビルドして `dist/` に出力するだけ） |

### 4.1. フロントエンドのルート（`frontend/src/App.tsx`）

| パス | ページ |
|---|---|
| `/` | `HomePage` |
| `/upload`（`?type=game` / `?type=site`） | `UnifiedUploadPage` |
| `/upload-game`, `/upload-static-site` | `/upload?type=...` へリダイレクト |
| `/login`, `/register`, `/login/success` | `LoginPage`, `RegisterPage`, `LoginSuccessPage` |
| `/videos/:id`, `/games/:id`, `/static-sites/:id` | `VideoDetailPage`, `GameDetailPage`, `StaticSiteDetailPage` |
| `/edit-video/:id`, `/edit-game/:id`, `/edit-static-site/:id` | `EditVideoPage`, `EditGamePage`, `EditStaticSitePage` |
| `/adjust-game/:id` | `AdjustGamePage` |
| `/mypage`, `/edit-profile` | `MyPage`, `EditProfilePage` |
| `/teams`, `/teams/:token` | `TeamListPage`, `TeamDetailPage` |

共通コンポーネント: `components/Header.tsx`, `PrivateRoute.tsx`, `ImageCropperModal.tsx`, `AccountDeletionModal.tsx`。チーム API のクライアント: `api/team.ts`。認証状態: `context/AuthContext.tsx`。

---

## 5. データベース

すべて `postgres:16`。ホストにポートを公開していない。

| コンテナ | DB 名 | 初期化スクリプト（compose がマウントするもの） | 接続ユーザー |
|---|---|---|---|
| `auth-db` | `auth_db` | `backend/auth/auth-datebase/auth-db/init.sql` | auth-service: `auth_service_user`、mypage-service: `mypage_service_user` |
| `app-db` | `app_db` | `backend/datebase/app-db/init.sql`（同じディレクトリの `001_*.sql` / `002_*.sql` は既存ボリューム向けのマイグレーションで、compose はマウントしない） | `POSTGRES_USER`（sfsp-worker は `sfsp_worker`） |
| `profile-db` | `profile_db` | `backend/profile-service/profile-db/init.sql` | `POSTGRES_USER` |
| `team-db` | `team_db` | `backend/team-service/team-db/init.sql` | `team_service_user` |
| `sfsp-db` | `sfsp_db` | `backend/security/sfsp-db/`（ディレクトリごと） | `POSTGRES_USER` |

> - `datebase` は誤記ではなく実ディレクトリ名。
> - `backend/auth/auth-db/init.sql` も存在するが、compose がマウントしているのは `backend/auth/auth-datebase/auth-db/init.sql`。
> - `init.sql` が実行されるのはボリュームが空のとき（初回）だけ。既存の DB にスキーマ変更を反映するには、マイグレーション SQL を手動で流すか、ボリュームを作り直す。

### 5.1. auth-db

**users**

| カラム | 型 | 説明 |
|---|---|---|
| `id` | UUID PK | ユーザー ID（UUID v7 で生成） |
| `username` | VARCHAR(255) | ユーザー名 |
| `google_name` | VARCHAR(255) | Google のアカウント名 |
| `email` | VARCHAR(255) NOT NULL | メールアドレス |
| `password_hash` | VARCHAR(255) | bcrypt ハッシュ（ローカルアカウントのみ） |
| `provider` | VARCHAR(50) | `local` / `google` |
| `provider_id` | VARCHAR(255) | Google のユーザー ID |
| `is_admin` | BOOLEAN | 管理者フラグ |
| `status` | VARCHAR(50) | `active` / `pending` / `deleted_data` |
| `created_at` | TIMESTAMPTZ | 作成日時 |

制約: `UNIQUE(provider, email)`, `UNIQUE(provider, provider_id)`。初期データの管理者は投入しない（管理者は `ADMIN_REGISTRATION_CODE` で登録する）。古いボリュームに残っている `admin@internal.local` は `DELETE FROM users WHERE email = 'admin@internal.local' AND provider = 'local';` で削除する。
- ログインできるのは `status` が `active` / `pending` のユーザーだけ（`deleted_data` などはパスワードログイン・Google ログイン・`authMiddleware` のすべてで拒否）。

### 5.2. profile-db

**users**（auth-db とは別のテーブル。ID は共通）

| カラム | 型 | 説明 |
|---|---|---|
| `id` | UUID PK | ユーザー ID |
| `username` | VARCHAR(255) | 表示名 |
| `bio` | TEXT | 自己紹介 |
| `icon_url` | TEXT | `/user-profiles/icons/...` |
| `background_image_url` | TEXT | `/user-profiles/backgrounds/...` |
| `icon_sfsp_job_id` | UUID | スキャン中のアイコンのジョブ ID |
| `background_sfsp_job_id` | UUID | スキャン中の背景のジョブ ID |
| `status` | VARCHAR(50) | `offline`（既定）/ `active` / `pending` / `online` |
| `created_at` / `updated_at` | TIMESTAMP | 作成・更新日時 |

- `GET /api/profile/me` は、行が無ければ JWT の `username` で行を作成する（`username` が空なら `status = 'pending'`）。
- フロントエンドは `status === 'pending'` のとき `/edit-profile` へ遷移させる。

### 5.3. app-db

**videos**

| カラム | 型 | 説明 |
|---|---|---|
| `id` | VARCHAR(10) PK | 動画 ID（ランダムな小文字英数字 10 桁） |
| `uploader_id` | UUID NOT NULL | 投稿者 |
| `title` | VARCHAR(255) NOT NULL | タイトル |
| `description` | TEXT | 説明 |
| `filename` | VARCHAR(255) NOT NULL | 公開後は `/videos/<id>/playlist.m3u8` |
| `thumbnail_path` | VARCHAR(255) | `/videos/thumbnails/<id>` |
| `status` | VARCHAR(50) | `scanning` → `processing` → `public` / `error` / `quarantined`（スキャン結果が `failed` / `invalid` ならその値） |
| `sfsp_job_id` | UUID | 本体のスキャンジョブ |
| `thumbnail_sfsp_job_id` | UUID | サムネイル差し替えのスキャンジョブ |
| `processing_details` | TEXT | 失敗理由など |
| `team_id` | VARCHAR(24) | チームトークン。NULL なら全体公開、値があればそのチーム限定 |
| `created_at` / `updated_at` | TIMESTAMPTZ | 作成・更新日時 |

**games**

| カラム | 型 | 説明 |
|---|---|---|
| `id` | VARCHAR(10) PK | ゲーム ID |
| `user_id` | UUID NOT NULL | 投稿者（※ videos は `uploader_id`、games / static_sites は `user_id`） |
| `title` / `description` | | |
| `status` | VARCHAR(50) | `scanning` → `processing` → `public` / `quarantined` / `failed`（処理エラーは `failed`。videos の `error` に相当）/ `invalid` |
| `sfsp_job_id` / `thumbnail_sfsp_job_id` | UUID | |
| `processing_details` | TEXT | |
| `team_id` | VARCHAR(24) | |
| `game_url` | VARCHAR(255) | `http://<id>.<STATIC_SITE_DOMAIN>:<port>/index.html` |
| `thumbnail_url` | VARCHAR(255) | `/games/thumbnails/<id>` |
| `scale` | REAL（既定 1.0） | 表示倍率（ユーザー調整） |
| `offset_x` / `offset_y` | INTEGER（既定 0） | 表示位置（ユーザー調整） |
| `native_width` / `native_height` | INTEGER（既定 1280 / 720） | `index.html` から抽出したネイティブ解像度 |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

**static_sites**

| カラム | 型 | 説明 |
|---|---|---|
| `id` | VARCHAR(10) PK | サイト ID |
| `user_id` | UUID NOT NULL | 投稿者 |
| `title` / `description` | | |
| `status` | VARCHAR(50) | `scanning` → `processing` → `public` / `quarantined` / `error`（不正なパス・`index.html` が無い等）/ `failed`（展開上限超過・panic 等） |
| `sfsp_job_id` / `thumbnail_sfsp_job_id` | UUID | |
| `processing_details` | TEXT | |
| `team_id` | VARCHAR(24) | |
| `minio_path` | VARCHAR(255) NOT NULL | `static-sites` バケット内のプレフィックス |
| `entry_point_path` | VARCHAR(255)（既定 `index.html`） | エントリーポイント |
| `thumbnail_url` | VARCHAR(255) | `/static-sites/thumbnails/<ランダム>.<拡張子>` |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

> app-db に `users` テーブルは無い。team-service は投稿者名を profile-service の `GET /api/profile/:id`（`PROFILE_SERVICE_URL`）から取得する（最大 8 並列、5 分キャッシュ。失敗時は空文字）。
> ゲーム・静的サイトのアップロード API は、**SFSP に送る前に**行を `status = 'scanning'`（job_id は NULL）で INSERT し、sfsp-api の応答ごとに `sfsp_job_id` / `thumbnail_sfsp_job_id` を UPDATE する。送信に失敗したら行を削除する。動画は逆順で、sfsp-api の応答を受けてから `sfsp_job_id` 付きで INSERT する（`video-upload-api` の `findVideoByJob` が、完了イベントが先に届いた場合に 2 秒間隔で最大 10 回探し直す）。

### 5.4. team-db

| テーブル | カラム | 制約 |
|---|---|---|
| `teams` | `id` UUID PK, `token` VARCHAR(24), `name`, `description`, `is_public` BOOL, `auto_approve` BOOL, `allow_member_invite` BOOL, `created_by` UUID, `created_at` | `UNIQUE(token)` |
| `team_members` | `id` UUID PK, `team_id` UUID FK→teams, `user_id` UUID, `role` VARCHAR(20)（`owner` / `admin` / `member`）, `joined_at` | `UNIQUE(team_id, user_id)`、チーム削除で CASCADE |
| `team_join_requests` | `id` UUID PK, `team_id` UUID FK, `user_id` UUID, `status`（`pending` / `approved` / `rejected` / `cancelled`）, `requested_at`, `reviewed_at`, `reviewed_by` UUID | `UNIQUE(team_id, user_id)` |
| `team_content` | `id` UUID PK, `team_id` UUID FK, `content_type`（`video` / `game` / `static-site`）, `content_id` VARCHAR(20), `created_at` | `UNIQUE(team_id, content_type, content_id)` |
| `team_posts` | `id` UUID PK, `team_id` UUID FK, `author_id` UUID, `title`, `body`, `created_at` | |

- `token` は crypto/rand から作る 24 文字の base36（約 124 ビット）。URL にそのまま使う。
- ロールの強さ: `owner`(3) > `admin`(2) > `member`(1)。`requireTeamRole(c, "admin")` のようにランクで判定する。
- `team_members.team_id` は UUID。URL の `token` をそのまま入れてはいけない（`SELECT id FROM teams WHERE token = $1` で引く）。
- `team_join_requests` の `UNIQUE(team_id, user_id)` はそのまま。却下・取り消し済み（または脱退済みで approved のまま）の行は、再申請時に UPSERT で `pending` に戻す。

### 5.5. sfsp-db

| テーブル | カラム |
|---|---|
| `files` | `id` UUID PK, `filename`, `filesize` BIGINT, `mime_type`, `sha256`（UNIQUE なし）, `storage_path`, `file_type`（`video` / `html` / `zip` / `other`）, `target_service`, `created_at` |
| `scan_jobs` | `id` UUID PK, `file_id` FK→files, `status`, `processing_details`, `is_cleaned_up` BOOL, `cleaned_up_at`, `created_at`, `updated_at` |
| `scan_results` | `id` UUID PK, `job_id` FK→scan_jobs, `scanner`（`clamav` / `yara`）, `result`（`clean` / `suspicious` / `malicious` / `error`）, `details`, `raw_output` JSONB（`stdout` / `stderr`）, `scanned_at` |

- `scan_jobs.status` の実際の値: `queued` → `running` → `clean` / `malicious` / `suspicious` / `failed` / `invalid`。
- `scan_jobs.event_published_at`（TIMESTAMPTZ）: 完了イベントを発行した日時。再処理を冪等にするために使う（sfsp-worker が起動時に `ADD COLUMN IF NOT EXISTS`）。
- `target_service` に使える値は [7.2](#72-スキャン完了イベントのキュー) の表を参照。

---

## 6. オブジェクトストレージ（MinIO）

MinIO は **1 インスタンス**（`atmosidea-minio`、`minio:9000`）。データはホストの `./backend/storage_data` に保存される。権限はバケットとユーザーで分ける。

### 6.1. バケット

`minio-init` が作成する（`mc mb --ignore-existing`）。

| バケット | 匿名ダウンロード | オブジェクトのキー |
|---|---|---|
| `videos` | 可 | `<videoId>/playlist.m3u8`、`<videoId>/<360p…1080p>/...`、`thumbnails/<videoId>` |
| `games` | 可 | `<gameId>/...`（展開したゲーム）、`thumbnails/<gameId>` |
| `static-sites` | 可 | `<siteId>/...`（展開したサイト）、`thumbnails/<32桁hex>.<jpg/png/gif/webp>` |
| `user-profiles` | 可 | `icons/<userId>-<uuid>.<ext>`、`backgrounds/<userId>-<uuid>.<ext>` |
| `raw-files` | 不可 | `<fileId>`（スキャン前） |
| `clean-files` | 不可 | `<fileId>`（スキャン済み・clean） |
| `quarantine` | 不可 | `<fileId>`（malicious / suspicious） |

### 6.2. ユーザーとポリシー

`minio-init` が、`.env` の値でユーザーを作り、`backend/minio-init/*.json` のポリシーを付ける。

| 環境変数（アクセスキー） | ポリシー | 権限 | 使うサービス |
|---|---|---|---|
| `MINIO_ROOT_USER` | （root） | すべて | `minio-init` のみ |
| `GAME_MINIO_ACCESS_KEY_ID` | `app-game-policy` | `games` に `s3:*` | game-upload-api, game-worker, auth-service（削除時） |
| `PROFILE_MINIO_ACCESS_KEY_ID` | `app-profile-policy` | `user-profiles` に `s3:*` | profile-service, auth-service（削除時） |
| `STATIC_SITE_MINIO_ACCESS_KEY_ID` | `app-static-site-policy` | `static-sites` に `s3:*` | static-site-upload-api, static-site-worker, auth-service（削除時） |
| `VIDEO_MINIO_ACCESS_KEY_ID` | `app-video-policy` | `videos` に `s3:*` | video-upload-api, video-worker |
| `SFSP_UPLOAD_ACCESS_KEY` | `sfsp-upload-policy` | `raw-files` に PutObject | 各アップロード API（raw 側の設定） |
| `SFSP_API_ACCESS_KEY` | `sfsp-api-policy` | `raw-files` に `s3:*` | sfsp-api |
| `SFSP_WORKER_RAW_ACCESS_KEY` | `sfsp-worker-raw-policy` | `raw-files` に Get / Delete / List | sfsp-worker |
| `SFSP_WORKER_CLEAN_ACCESS_KEY` | `sfsp-worker-clean-policy` | `clean-files` / `quarantine` / `raw-files` に `s3:*` | sfsp-worker, video-upload-api（clean 側） |
| `SFSP_GAME_WORKER_ACCESS_KEY` | `sfsp-game-worker-policy` | `clean-files` に GetObject | game-upload-api, game-worker |
| `SFSP_STATIC_SITE_WORKER_ACCESS_KEY` | `sfsp-static-site-worker-policy` | `clean-files` に Get / Delete | static-site-upload-api, static-site-worker |
| `SFSP_PROFILE_WORKER_ACCESS_KEY` | `sfsp-profile-worker-policy` | `clean-files` に GetObject | profile-service |
| `SFSP_VIDEO_WORKER_ACCESS_KEY` | `sfsp-video-worker-policy` | `clean-files` に GetObject | video-worker |

- どのアプリ用ポリシーにも `s3:ListAllMyBuckets` と `s3:GetBucketLocation` が付いている（起動時の `ListBuckets` / `BucketExists` に必要）。
- 旧 `backend/minio-init/sfsp-worker-policy.json` と `initialize.sh`（旧マルチ MinIO 構成用）は未使用だったため削除した。`minio-init` が使うポリシーは compose がマウントする 12 個の `*-policy.json` だけ。
- **ユーザーやポリシーを変更するときは、`docker-compose.yml` の `minio-init`（environment / entrypoint / volumes の 3 か所）と `backend/minio-init/*.json` を同じ変更で更新する。**
- `.env.example` では、アクセスキーをサービスごとに別の値にしている。同じ値にすると MinIO 上では 1 ユーザーになり、ポリシーが合算される。

---

## 7. Redis と共有パッケージ（shared）

### 7.1. Redis のキー

| キー | 型 | 書き込み | 読み出し |
|---|---|---|---|
| `sfsp:scan_queue` | List | sfsp-api（LPUSH） | sfsp-worker（`DequeueReliable`） |
| `sfsp:completed:<target>` | List | sfsp-worker（LPUSH） | 各サービス（`DequeueReliable`） |
| `<queue>:processing` | List | `DequeueReliable`（BRPOPLPUSH） | `Ack`（LREM）、`RecoverProcessing`（起動時に元のキューへ戻す） |
| `sfsp:scan_queue:dead` | List | sfsp-worker（5 回失敗し、`failed` の確定もできなかったジョブ） | 手動確認用 |
| `blocklist:<jti>` | String（TTL = JWT の残り有効期限） | auth-service（ログアウト、アカウント削除） | JWT を検証する全サービスの `authMiddleware` |
| `oauth_code:<code>` | String（TTL 60 秒） | auth-service（Google コールバック） | auth-service `POST /api/auth/oauth/exchange`（GETDEL で 1 回だけ） |

Redis は `requirepass`（`REDIS_PASSWORD`）で保護している。接続には `REDIS_ADDR` + `REDIS_PASSWORD`、または `REDIS_URL=redis://:<password>@redis:6379`（パスワードは URL エンコードが必要）を使う。

### 7.2. スキャン完了イベントのキュー

`backend/shared/queue/queue.go` の `EnqueueScanCompletionEvent` が `target_service` からキューを決める。

| `target_service` | キュー | 読むサービス | 処理 |
|---|---|---|---|
| `stream` | `sfsp:completed:stream` | **video-upload-api** | HLS 変換・サムネイル生成 |
| `video-thumbnail` | `sfsp:completed:video-thumbnail` | video-worker | サムネイル差し替え |
| `game` | `sfsp:completed:game` | game-worker | zip 展開・公開 |
| `game-thumbnail` | `sfsp:completed:game-thumbnail` | game-worker | サムネイル |
| `static-site` | `sfsp:completed:static-site` | static-site-worker | zip 展開・公開 |
| `site-thumbnail` | `sfsp:completed:site-thumbnail` | static-site-worker | サムネイル |
| `thumbnail` | `sfsp:completed:site-thumbnail`（※ site と同じ） | static-site-worker | 互換用の別名 |
| `profile` | `sfsp:completed:profile` | profile-service | アイコン・背景の反映 |

それ以外の値は sfsp-api が拒否する。各ワーカーは `DequeueReliable` で取り出し、DB を終端状態にしてから `Ack` する。処理エラーは終端状態（`failed` / `error`）を記録して `Ack` する（無限再試行はしない）。DB に書けないときだけ `Ack` せず、次の起動時に `RecoverProcessing` で再処理される。既に終端状態の行への再配信は無視する。

### 7.3. ScanCompletionEvent（`backend/shared/event/event.go`）

```json
{
  "job_id": "uuid",
  "file_id": "uuid",
  "target_service": "game",
  "final_status": "clean | malicious | suspicious | failed | invalid",
  "scanned_at": "RFC3339",
  "sha256": "hex",
  "filename": "元のファイル名"
}
```

ワーカーは `job_id` を `app-db` の `sfsp_job_id`（サムネイルは `thumbnail_sfsp_job_id`）と照合して対象の行を特定し、`file_id`（または `<file_id>/<filename>`）をキーに `clean-files` から取得する。

### 7.4. shared パッケージ（`github.com/atmosidea/shared`）

| パッケージ | 内容 |
|---|---|
| `config/` | 環境変数の読み込み（Viper）、`Retry` |
| `event/` | `ScanCompletionEvent` |
| `model/` | 共通モデル |
| `queue/` | Redis 接続、キュー名の定数、`EnqueueJob` / `DequeueJob` / `EnqueueScanCompletionEvent`、信頼性キュー用の `DequeueReliable` / `Ack` / `RecoverProcessing` |

SFSP（`backend/security/sfsp/`）は shared を使わず、`internal/model`・`internal/queue` に同じ定義を持っている。**キュー名やイベントの形を変えるときは両方を直す。**

---

## 8. 処理フロー

### 8.1. ログイン

1. ローカル: `POST /api/auth/login`（email / password）→ auth-db で bcrypt 照合 → JWT を返す。
2. Google: `GET /api/auth/google/login`（32 バイトの state を host-only・HttpOnly の Cookie に保存）→ Google → `/api/auth/google/callback`（state を定数時間で照合し Cookie を削除。userinfo は HTTP 200・id / email・`verified_email` を確認）→ 初回は auth-db に `status = 'active'` で作成し、profile-service の `http://profile-service:8084/internal/create`（`X-Internal-Key`）を呼ぶ → JWT を Redis の `oauth_code:<code>` に 60 秒保存し `/login/success?code=...` にリダイレクト → `LoginSuccessPage` が URL からコードを消して `POST /api/auth/oauth/exchange` で JWT を受け取る。**JWT を URL に載せない。**
   - 無効なユーザー（`deleted_data` 等）はパスワードログインで 403、Google ログインで `/login?error=account_disabled`。
3. フロントエンド（`AuthContext.tsx`）がトークンを `localStorage['token']` に保存 → `GET /api/profile/me` でユーザー情報を取得。
   - トークンの期限切れ: logout API は呼ばず、ブラウザ側の状態だけを消して `/login` へ。
   - `/api/profile/me` が **401** のときだけトークンを消す。それ以外のエラーではログイン状態を保ち、JWT の内容でユーザーを組み立てる。

### 8.2. 管理者登録

`POST /api/auth/register` `{"email", "password", "adminCode"}`

1. `adminCode` を `ADMIN_REGISTRATION_CODE` と定数時間で比較（不一致は 403。入力値はログに出さない）。
2. email に `@` が必要、password は 8 文字以上（違反は 400）。
3. 同じ email のローカルアカウントがあれば 409。
4. `is_admin = TRUE` で作成し、ユーザー名は email の `@` より前。profile-service の `/internal/create` を呼ぶ。

### 8.3. ゲーム

1. `POST /api/games/upload`（multipart: `game` zip、`thumbnail`、`title`、`description`、任意で `team_id`）。
2. game-upload-api が `games` に `status = 'scanning'` で先に INSERT → sfsp-api（`target_service=game`、ストリーミング送信）、サムネイルは `game-thumbnail` で別途送る → 応答ごとに job_id を UPDATE。上限: `MAX_GAME_UPLOAD_BYTES`（既定 1 GiB）/ `MAX_IMAGE_UPLOAD_BYTES`（既定 10 MiB）を超えると 413、画像以外のサムネイルは 415。
3. sfsp-worker がスキャン → `sfsp:completed:game`。
4. game-worker: `clean` なら `processing` → `clean-files` から取得 → 上限付きで展開（`GAME_MAX_EXTRACTED_BYTES` 2 GiB / `GAME_MAX_ZIP_ENTRIES` 20000 / `GAME_MAX_ENTRY_BYTES` 1 GiB。超過・`../`・絶対パス・symlink は `failed`）→ `index.html` を探してルートを決定 → 解像度を抽出して CSS を注入 → `games/<id>/` へアップロード → `status = 'public'`、`game_url`・`native_width/height` を保存。`clean` 以外なら `quarantined` / `failed` / `invalid`。行が見つからないイベント（INSERT との競合）は 1/2/4/8/16 秒間隔で探し直す。
5. `AdjustGamePage` が `/api/games/:id` をポーリングし、`public` になったら `game_url` を iframe で表示。

### 8.4. 静的サイト

1. `POST /api/static-sites/upload`（multipart: `file` zip、`thumbnail`、`title`、`description`）→ 先に `static_sites` に INSERT → sfsp-api（`static-site`、ストリーミング送信）、サムネイルは `site-thumbnail`。上限: `MAX_SITE_UPLOAD_BYTES`（既定 500 MiB）、画像は `MAX_IMAGE_UPLOAD_BYTES`。展開の上限: `SITE_MAX_EXTRACTED_BYTES` 1 GiB / `SITE_MAX_ZIP_ENTRIES` 20000 / `SITE_MAX_ENTRY_BYTES` 256 MiB（超過は `failed`）。
   - サムネイル差し替えは `thumbnail_sfsp_job_id` が一致するときだけトランザクション内で確定し、古いオブジェクト（`thumbnails/` 配下で他サイトが参照していないもの）を削除する。却下時は job_id を NULL に戻す。
2. static-site-worker: `clean` なら zip を取得 → 最初に見つかった `index.html` のディレクトリをルートとする → 各エントリを `static-sites/<id>/<相対パス>` にアップロード。**相対パスが `..` で外に出る・絶対パスのエントリがあれば `status = 'error'`（`Illegal file path in ZIP`）で中止。** → `status = 'public'`。
3. 表示: `http://<id>.localhost:3001/`（Nginx が `games/<id>/` を試し、404 なら `static-sites/<id>/` を返す。末尾が `/` の URL には `index.html` を補う）。

### 8.5. 動画

1. `POST /api/videos/upload`（multipart: `video`、`title`、`description`）→ sfsp-api（`stream`）→ `videos` に `status = 'scanning'`。
   - 上限は `MAX_VIDEO_UPLOAD_BYTES`（既定 1 GiB、超過は 413）。sfsp-api へは io.Pipe でストリーミング送信する。
2. **video-upload-api 内の goroutine**（`videoWorker`）が `sfsp:completed:stream` を `DequeueReliable` で取り出す → `clean` なら `processVideoAsync` を goroutine で実行:
   - `clean-files` から `UPLOAD_DIR`（`./backend/video-service/upload_workdir` を `/storage` にマウント）の `job-<id>-*` 一時ディレクトリへダウンロード（ユーザーのファイル名は使わない）。
   - FFmpeg で 360p / 480p / 720p / 1080p の HLS とマスタープレイリストを作成し、サムネイルを生成。同時実行数は `FFMPEG_MAX_CONCURRENCY`（既定 2）、ffmpeg 1 回ごとのタイムアウトは `FFMPEG_TIMEOUT`（既定 2h）。
   - 処理中に動画が削除されると、削除ハンドラーがジョブをキャンセルし、ジョブ側も行の存在を再確認して `videos/<id>/` と `thumbnails/<id>` を消す。
   - `videos/<id>/...` と `videos/thumbnails/<id>` にアップロード（失敗時は最大 5 回までバックオフしてリトライ）。
   - `filename = /videos/<id>/playlist.m3u8`、`status = 'public'` に更新し、ローカルの一時ファイルを削除。
3. 再生: `VideoDetailPage` → `/api/videos/<id>/stream/playlist.m3u8` → Nginx が MinIO の `videos/<id>/playlist.m3u8` にプロキシ。
4. サムネイル差し替え: `PUT /api/videos/:id`（video-worker、投稿者 / 管理者のみ）→ `video-thumbnail` でスキャン → video-worker が画像形式を確認して保存。

### 8.6. プロフィール画像

`PUT /api/profile/icon`（`icon`）/ `PUT /api/profile/background`（`background`）→ sfsp-api（`profile`）→ `icon_sfsp_job_id` 等を保存 → profile-service 内のワーカーが `sfsp:completed:profile` を処理 → 画像形式（jpeg / png / gif / webp）なら `user-profiles` に保存して URL を更新し、古い画像を削除。アップロード時点でも先頭 512 バイトで形式を確認し（それ以外は 415）、`MAX_IMAGE_UPLOAD_BYTES` を超えると 413。フロントエンドはスキャン中はローカルプレビューを表示し、ポーリングで完了を検知する。

### 8.7. チーム

- 作成: `POST /api/teams` → 作成者が `owner`。
- 加入: 公開チームに `POST /:token/join` → `auto_approve` なら即 `member`、そうでなければ `team_join_requests` に `pending`。非公開チームは加入申請できない（403）。
- 承認: `PATCH /:token/join-requests/:requestId?action=approve`（admin 以上）→ トランザクション内で `pending` の申請だけを `approved` にし、**申請者本人**を `member` として追加（既存メンバーのロールは変えない）。該当する保留中の申請が無ければ 404。
- チーム限定コンテンツ: 各アップロード API が `GET /api/teams/:token/permission` で投稿権限を確認し、コンテンツの `team_id` にトークンを保存する。閲覧時も動画・ゲーム・サイトの詳細 API と一覧の `?team=` が同じエンドポイントで `can_view` を確認する（Authorization を転送）。
- 退出・削除: 自分自身はいつでも退出できる（最後の owner は 409 `code: last_owner`）。他人を削除できるのは admin 以上で、相手のロールが自分より弱いときだけ。

### 8.8. アカウント削除

`DELETE /api/auth/me` → ローカルアカウントは物理削除、Google アカウントは `deleted_data` として扱い、app-db の投稿・MinIO（`user-profiles` / `games` / `static-sites` / `videos` の `<id>/` と `thumbnails/<id>`）を削除する。さらに `X-Internal-Key` 付きで次を呼ぶ。
- profile-service `DELETE /internal/users/:userId` → profile-db の行と icon / background を削除。
- team-service `DELETE /internal/users/:userId` → メンバー情報・加入申請・本人のチーム投稿を削除。唯一の owner だったチームは、最もロールが強く最も早く参加したメンバーに移譲し、誰もいなければチームごと削除。
- どこかが失敗しても残りは続け、200 `{"message","cleanupErrors":[...]}` で失敗箇所を返す。使用中のトークンの jti はブロックリストに入れる。

### 8.9. SFSP のクリーンアップ

sfsp-worker は 30 秒ごとに app-db を見て、`public` になったコンテンツの `clean-files` オブジェクトを削除し、`scan_jobs.is_cleaned_up` を立てる（`profile` は対象外）。

---

## 9. API 一覧

認証の列: `—` = 不要、`JWT` = `Authorization: Bearer <token>`、`投稿者` = JWT かつ投稿者本人または管理者。

### 9.1. auth-service — `/api/auth`

| メソッド | パス | 認証 | リクエスト | レスポンス |
|---|---|---|---|---|
| POST | `/register` | 管理者コード | `{"email","password","adminCode"}` | 201 `{"message","userID"}` / 400 / 403 / 409 |
| POST | `/login` | — | `{"email","password"}` | 200 `{"token"}` / 403（無効なユーザー） |
| GET | `/google/login` | — | — | Google へリダイレクト（`GOOGLE_CLIENT_ID/SECRET` があるときだけ登録されるルート） |
| GET | `/google/callback` | — | クエリ | `/login/success?code=...` へリダイレクト（コードは 60 秒・1 回限り） |
| POST | `/oauth/exchange` | — | `{"code"}` | 200 `{"token"}` / 400 / 401（無効・期限切れ） |
| POST | `/logout` | JWT | — | 200 `{"message":"Successfully logged out"}` |
| DELETE | `/me` | JWT | — | 200 `{"message","cleanupErrors":[...]}` |
| GET | `/user/:userId` | — | — | `{"provider":"local\|google"}` |

### 9.2. profile-service — `/api/profile`

| メソッド | パス | 認証 | 説明 |
|---|---|---|---|
| GET | `/me` | JWT | 自分のプロフィール（無ければ作成） |
| GET | `/status` | JWT | `{"status"}` |
| GET | `/:userId` | — | 公開プロフィール（無ければ 404） |
| PUT | （ベースパス） | JWT | `{"username","bio"}`、`status` を `active` に |
| PUT | `/icon` | JWT | multipart `icon` → 202 `{"job_id","status":"scanning"}` |
| PUT | `/background` | JWT | multipart `background` → 202 |
| POST | `/internal/create`（旧パス。移行用） | `X-Internal-Key` | `{"user_id","username"}`。Nginx は `/api/profile/internal/` を 404 にする |

内部専用（`/api/profile` の外。Nginx からは届かない。`X-Internal-Key` == `INTERNAL_API_KEY`、未設定なら常に 403）:

| メソッド | パス | 説明 |
|---|---|---|
| POST | `/internal/create` | `{"user_id","username"}`（auth-service が使う） |
| DELETE | `/internal/users/:userId` | プロフィールと画像を削除。冪等、204 |

`PUT /icon` / `PUT /background` は画像以外 415、`MAX_IMAGE_UPLOAD_BYTES` 超過は 413。

### 9.3. 動画 — `/api/videos`

| メソッド | パス | サービス | 認証 | 説明 |
|---|---|---|---|---|
| POST | `/upload` | video-upload-api | JWT | multipart `video`, `title`, `description` |
| DELETE | `/delete/:id` | video-upload-api（Nginx が `/api/videos/:id` に書き換え） | 投稿者 | 動画と HLS・サムネイルを削除 |
| DELETE | `/:id/thumbnail` | video-upload-api（Nginx の専用 location で振り分け） | 投稿者 | サムネイルを MinIO から削除し `thumbnail_path` を NULL に |
| GET | （ベースパス） | video-worker | — | 一覧（`?q=`、`?team=`）。`status='public'` のみ。`?team=` が無ければ `team_id IS NULL`、あれば `can_view` が必要（無ければ 403） |
| GET | `/:id` | video-worker | 任意 | 詳細。投稿者・管理者以外は `public` かつ（`team_id` があれば `can_view`）のときだけ。見せられなければ 404 |
| PUT | `/:id` | video-worker | 投稿者 | multipart `title`, `description`, 任意で `thumbnail` |
| GET | `/:id/stream/<path>` | Nginx → MinIO | — | HLS |

### 9.4. ゲーム — `/api/games`（game-upload-api）

| メソッド | パス | 認証 | 説明 |
|---|---|---|---|
| POST | `/upload` | JWT | multipart `game`, `thumbnail`, `title`, `description` |
| GET | （ベースパス） | — | 一覧（`?q=`、`?team=`、`status='public'` のみ。`?team=` は `can_view` が必要） |
| GET | `/:id` | 任意 | 詳細。投稿者・管理者以外は `public` のみ（それ以外 404）、`team_id` があれば `can_view`（無ければ 403、team-service 不達は 502） |
| PUT | `/:id` | 投稿者本人のみ（管理者でも 403） | multipart `title`, `description`, `thumbnail` |
| PUT | `/adjust/:id` | 投稿者本人のみ | `{"scale","offset_x","offset_y"}` |
| DELETE | `/:id` | 投稿者 | ゲームと MinIO のファイルを削除（管理者も可） |
| DELETE | `/:id/thumbnail` | 投稿者本人のみ | サムネイル削除 |

### 9.5. 静的サイト — `/api/static-sites`（static-site-upload-api）

| メソッド | パス | 認証 | 説明 |
|---|---|---|---|
| POST | `/upload` | JWT | multipart `file`, `thumbnail`, `title`, `description` |
| GET | （ベースパス） | — | 一覧（`?q=`、`?team=`。public かつ `team_id IS NULL`、`?team=` は `can_view` が必要） |
| GET | `/:id` | 任意 | 詳細。投稿者・管理者以外は `public` かつ（`team_id` があれば `can_view`）のときだけ、それ以外 404 |
| PUT | `/:id` | 投稿者 | タイトル・説明 |
| DELETE | `/:id` | 投稿者 | サイトと MinIO のファイルを削除 |
| DELETE | `/:id/thumbnail` | 投稿者 | サムネイル削除 |

### 9.6. mypage-service — `/api/my`

| メソッド | パス | 認証 | 説明 |
|---|---|---|---|
| GET | `/videos` | JWT | `uploader_id` が自分の動画 |
| GET | `/games` | JWT | `user_id` が自分のゲーム |
| GET | `/static-sites` | JWT | `user_id` が自分のサイト |

### 9.7. team-service — `/api/teams`

| メソッド | パス | 認証 | 説明 |
|---|---|---|---|
| GET | （ベースパス） | — | 公開チーム一覧 |
| GET | `/mine` | JWT | 参加中のチーム |
| POST | （ベースパス） | JWT | `{"name","description","is_public","auto_approve","allow_member_invite"}` |
| GET | `/:token` | — | チーム詳細 |
| PATCH | `/:token` | owner | チーム情報の更新 |
| DELETE | `/:token` | owner | チーム削除（メンバー等は CASCADE） |
| GET | `/:token/permission` | 任意 | `{"is_public","can_view","can_post","role"}`。JWT を先に評価し、`can_view` = 公開チームまたはメンバー、`can_post` = member 以上 |
| GET | `/:token/members` | member 以上 | メンバー一覧 |
| POST | `/:token/members` | admin 以上 | `{"user_id","role"}`。付与できるのは自分より弱いロールだけ（**例外: owner は owner を付与できる**＝オーナー移譲用）。新規 201 / 変更 200 |
| DELETE | `/:token/members/:userId` | JWT（自分は退出可） | 他人の削除は admin 以上かつ相手が自分より弱いとき。最後の owner は 409 |
| POST | `/:token/join` | JWT | 加入申請 |
| GET | `/:token/join-requests` | admin 以上 | 申請一覧 |
| PATCH | `/:token/join-requests/:requestId` | admin 以上 | `?action=approve\|reject` → `{"status","user_id"}` / 404 |
| DELETE | `/:token/join-requests` | JWT | 自分の申請を取り消し |
| POST | `/:token/content` | member 以上 | `{"title","body"}` |
| GET | `/:token/content` | 公開チームは不要 | 投稿一覧 |
| GET | `/:token/content/:contentID` | 同上 | 投稿 1 件 |
| DELETE | `/internal/users/:userId`（`/api/teams` の外） | `X-Internal-Key` | アカウント削除用。冪等、204 |

- `PATCH /:token` は送られたフィールドだけを更新する（省略したフィールドは変えない）。

### 9.8. sfsp-api（内部専用。Nginx からは到達できない）

| メソッド | パス | 説明 |
|---|---|---|
| POST | `/api/v1/files` | multipart `file`, `target_service` → 202 `{"file_id","job_id","status":"queued"}` |
| GET | `/api/v1/jobs/:id` | ジョブの状態 |
| GET | `/api/v1/results/:id` | スキャン結果 |
| GET | `/health` | ヘルスチェック（`/api/v1` の外） |

`/api/v1/*` にはヘッダー `X-SFSP-API-Key`（== `SFSP_API_KEY`）が必須（違えば 401）。`SFSP_API_KEY` が空なら sfsp-api は起動しない。アップロードは MultipartReader で MinIO にストリーミングし、`SFSP_MAX_UPLOAD_BYTES`（既定 2 GiB-1）超過は 413。

### 9.9. 監視ダッシュボード（mon-backend、`http://127.0.0.1:8090`）

| メソッド | パス | 説明 |
|---|---|---|
| GET / HEAD | `/auth/check` | mon-nginx の `auth_request` 用（Basic 認証の確認だけ行う） |
| GET | `/api/containers` | コンテナ一覧 |
| GET | `/api/containers/stats` | CPU・メモリ |
| POST | `/api/containers/restart/{name}` | 再起動 |
| GET | `/api/connections/count` | `atmosidea-frontend` のログから直近 5 分のユニーク IP 数 |
| GET | `/api/topology` | 構成図のデータ |
| GET | `/api/volumes` | Docker ボリューム |
| GET | `/api/minio/list/{containerName}?bucket=&prefix=` | MinIO の一覧 |
| POST | `/api/minio/upload/{containerName}?bucket=&prefix=` | MinIO へアップロード（対象コンテナの root 資格情報を使う） |
| DELETE | `/api/minio/delete/{containerName}?bucket=&key=` | MinIO から削除 |
| GET (WS) | `/ws/logs?container=` または `/ws/logs/{id}` | ログのストリーミング |

- `isTrustedRequest` が、Host が許可リスト（`localhost` / `127.0.0.1` / `::1` と `MONITORING_ALLOWED_HOSTS`）にあり、かつ Origin があれば同じホストであることを確認する。満たさなければ 403（WebSocket の `CheckOrigin` も同じ）。
- CORS ヘッダーは返さない（フロントエンドは同じ nginx から配信される）。
- 全ルート（`/api`、`/ws`）で HTTP Basic 認証（`MONITOR_USERNAME` / `MONITOR_PASSWORD`、定数時間比較）。未設定なら 503。mon-nginx は `auth_request` で UI も保護する。ループバック公開と同一オリジン制限も維持。

---

## 10. Nginx（API ゲートウェイ）

設定ファイル: `frontend/nginx.conf`（compose が `/etc/nginx/nginx.conf` に読み取り専用でマウント）。**変更したら `nginx -t` で構文を確認し、`docker compose restart frontend` で反映する。**

### 10.1. server ブロック

| server | `server_name` | 役割 |
|---|---|---|
| サブドメイン用 | `"~^(?<subdomain>[a-z0-9]{10})\.localhost$"` | ゲーム・静的サイトの配信（アプリと別オリジン） |
| メイン | `localhost 127.0.0.1`（`default_server`） | React・API・MinIO へのプロキシ |

`server_name` の正規表現は引用符で囲む（バックスラッシュを含むため）。

### 10.2. メインの location（評価順に注意）

| location | 転送先 | 備考 |
|---|---|---|
| `/` | `/usr/share/nginx/html`（`try_files ... /index.html`） | SPA |
| `^~ /api/profile/internal/`、`~ ^/api/.*/internal(/｜$)` | `return 404` | 内部 API を外部に出さない（表の都合で全角の `｜` と表記。実際の `nginx.conf` では半角のパイプ記号） |
| `/api/auth`, `/api/profile`, `/api/my`, `/api/games`, `/api/static-sites`, `/api/teams` | 各サービス | プレフィックス一致 |
| `/api/videos/upload` | video-upload-api | タイムアウト 300 秒、`client_max_body_size 1100m`、`proxy_request_buffering off` |
| `/api/games/upload` / `/api/static-sites/upload` | game-upload-api / static-site-upload-api | `client_max_body_size` 1100m / 550m、`proxy_request_buffering off` |
| `~ ^/api/videos/delete/(?<video_id>[a-zA-Z0-9]+)$` | video-upload-api | `/api/videos/$video_id` に rewrite（DELETE 専用の URL を用意して `if` を避けている） |
| `~ ^/api/videos/[a-zA-Z0-9]+/thumbnail$` | video-upload-api | サムネイル削除。これが無いと下の `/api/videos` に吸われて video-worker で 404 になる |
| `~ '^/api/videos/([a-z0-9]{10})/stream/(.*)$'` | MinIO（`/videos/$1/$2` に rewrite） | HLS |
| `/api/videos` | video-worker | 上の正規表現に当たらなかった残り全部 |
| `~ ^/videos/thumbnails/(.+)$` ほか `/games/thumbnails/`, `/static-sites/thumbnails/`, `/user-profiles/` | MinIO | thumbnails は `Cache-Control: no-cache, no-store, must-revalidate`（`/user-profiles/` は付けない） |
| `~ '^/games/([a-z0-9]{10})/(.+)$'` | `return 404` | **ゲーム本体はメインオリジンで配信しない**（`thumbnails` も 10 文字なので、この location より前に thumbnails の location を置くこと） |

正規表現の location は**書いた順**に評価され、最初に一致したものが使われる。プレフィックス location より正規表現が優先される。

server の `client_max_body_size` は 12m（画像 10 MiB 用）。大きなアップロードは上の専用 location でだけ許可する。

### 10.3. `proxy_pass` の規約（変更禁止）

```nginx
location /api/auth {
    proxy_pass http://auth_service;   # 末尾にスラッシュを付けない
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

- 末尾スラッシュなし → URI がそのまま渡る（`/api/auth/login` → `http://auth_service/api/auth/login`）。各 Go サービスは `/api/...` を含めてルーティングしている。
- 末尾スラッシュを付けると `location` 部分が削られ、Go 側のルーティングが壊れる。

### 10.4. ユーザーがアップロードしたファイルのヘッダー

サムネイル・プロフィール画像・HLS の location には次を付ける。

```nginx
add_header Content-Security-Policy "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; sandbox" always;
add_header X-Content-Type-Options "nosniff" always;
```

- location 内で `add_header` を 1 つでも書くと、server レベルの `add_header`（メインの CSP）は**継承されない**。ヘッダーを追加するときは必要なものをすべてその location に書く。
- メインの CSP は `script-src 'self' 'unsafe-inline' 'unsafe-eval' https://cdn.jsdelivr.net`、`frame-src 'self' http://*.localhost http://*.localhost:3001`。

### 10.5. サブドメイン（ゲーム・静的サイト）

```nginx
rewrite ^(.*)/$ $1/index.html;                 # ディレクトリには index.html を補う（break を付けない）
rewrite ^/(.*)$ /games/$subdomain/$1 break;     # まず games バケット
error_page 404 = @fallback_static_site;        # 無ければ static-sites バケット
```

- MinIO はディレクトリのインデックスを返さないため、`index.html` を補っている。
- 1 行目に `break` を付けると 2 行目が実行されず、`/index.html` がバケット名として MinIO に渡る（過去にあった不具合）。

---

## 11. 認証・認可

### 11.1. JWT

- 発行: auth-service の `generateJWT`。HS256、署名鍵は `JWT_SECRET`（全サービス共通）、有効期限 7 日。
- クレーム: `sub` / `userID` / `user_id` / `id`（すべて同じユーザー ID）、`username`、`isAdmin`、`exp`、`jti`（UUID）。
- 保存場所: フロントエンドの `localStorage['token']`（Cookie ではない）。
- 検証: 各サービスの `authMiddleware`。auth / profile / mypage / team / video-* / game-upload-api / static-site-upload-api はすべて HS256 に固定し、`jti` の無いトークンは拒否する。

### 11.2. ログアウト（ブロックリスト）

- `POST /api/auth/logout` が `blocklist:<jti>` を Redis に保存（TTL = 残りの有効期限）。
- auth-service の `authMiddleware` は、**署名を検証した後に** `blocklist:<jti>` を確認する（Redis エラー時は 500 で拒否）。
- JWT を検証する全サービス（auth / profile / mypage / team / video-upload-api / video-worker / game-upload-api / static-site-upload-api）が同じ確認をする。Redis エラー時は拒否（fail closed）。認証が任意の公開 GET では、無効・失効済みのトークンは匿名として扱う。
- auth-service は加えて、リクエストごとに auth-db の `status` を確認する。

### 11.3. 編集・削除の認可

- 各ハンドラが、コンテンツの投稿者（videos: `uploader_id`、games / static_sites: `user_id`）と JWT の `userID` を比較し、一致しないかつ `isAdmin` でなければ 403。ただし game-upload-api の `PUT /:id`・`PUT /adjust/:id`・`DELETE /:id/thumbnail` は投稿者本人のみで、管理者でも 403（`DELETE /:id` だけ管理者も可）。
- `PUT /api/videos/:id`（video-worker）は authMiddleware と投稿者チェックあり。

### 11.4. チームの認可

- `requireTeamRole(c, "<role>")` が、呼び出し元のロールのランクを比較する。
- ロールの付与・削除は「自分より弱いロールだけ」。例外として owner は owner を付与できる（オーナー移譲のため。厳密にするなら `canGrantRole` の該当行を消す）。最後の owner は退出・削除できない（409）。
- 非公開チームの投稿は、メンバーがログインしている場合だけ閲覧できる。

---

## 12. セキュリティモデル

詳細な設計意図は `SECURITY_EXPLANATION.md`。ここでは**実装の事実**を記す。

### 12.1. スキャンの判定（`backend/security/sfsp/internal/`）

| 段階 | 実装 |
|---|---|
| zip の展開 | `worker.unzip`。Zip Slip・symlink・デバイス・重複エントリを拒否。上限: `SFSP_MAX_ZIP_ENTRIES` 20000、`SFSP_MAX_EXTRACTED_BYTES` 4 GiB、`SFSP_MAX_ZIP_ENTRY_BYTES` 2 GiB-1、`SFSP_MAX_ZIP_RATIO` 1000 倍、`SFSP_MAX_ZIP_DEPTH` 32。違反は `suspicious`、I/O エラーは `failed`、壊れた zip は `invalid` |
| ルートの検出 | Unity（`index.html` + `Build/`）→ 静的サイト（`index.html`）の順。複数見つかったり、見つからなければ `invalid` |
| スキャン対象 | **展開したディレクトリ全体**（検出したルートだけではない）。zip 以外はファイルそのもの |
| ClamAV | `clamscan -r --no-summary --infected --alert-exceeds-max=yes --max-filesize --max-scansize --max-files --max-recursion --max-dir-recursion --max-scantime --pcre-max-filesize <path>`（値は `SFSP_CLAMAV_*`）。終了コード 0 = clean、1 / 2 で実際の検出あり = malicious、`Heuristics.Limits.Exceeded.*` = suspicious、それ以外 = error。**1 ファイルの検査上限は 2 GiB-1**（ClamAV 1.5 の制約） |
| YARA | `yara -r -w -m <rules> <path>`。終了コード 0 で出力行あり = suspicious、出力なし = clean、0 以外 = error |
| 総合判定 | `determineOverallStatus`: ClamAV malicious → `malicious`、ClamAV または YARA が suspicious → `suspicious`、どちらかが error → `failed`、**両方が clean のときだけ** `clean` |
| 結果 | `clean` → `clean-files`、`malicious` / `suspicious` → `quarantine`。`raw-files` は「状態の記録 → イベント発行」の後に削除（途中で落ちても再処理できる） |

`RunInSandbox` は stdout と stderr を別々に返し、終了コードが 0 以外なら `*sandbox.ExitError{Code}` を返す（`sandbox.ExitCode(err)` で取り出す）。**判定は必ず終了コードで行い、「OK」などの文字列で判定しない。**

### 12.2. サンドボックス（`internal/sandbox/docker.go`）

| 設定 | 値 |
|---|---|
| 起動方法 | スキャナーのコマンドを `Entrypoint` として直接実行（イメージの `/init` を通さない） |
| ネットワーク | `NetworkMode: none` |
| 権限 | `CapDrop: ALL`、`SecurityOpt: no-new-privileges` |
| ルートファイルシステム | `ReadonlyRootfs: true`。書き込めるのは `/tmp` の tmpfs（`rw,noexec,nosuid,nodev,size=512m`）だけ。上限を超えるとスキャンはエラー終了し `failed`（clean にはならない） |
| リソース | メモリ 1.5 GB、CPU 1、PID 256 |
| タイムアウト | 30 分（`sandboxTimeout`） |
| マウント | `sfsp_temp_scan` ボリュームを `/scan_target` に読み取り専用でマウント |

- `CapDrop: ALL` の下では clamav/clamav イメージの `/init`（chown を行う）が失敗する。Entrypoint を元に戻すとスキャン前に終了コード 1 で落ち、全ファイルが判定不能になる。
- ウイルス定義は `sfsp-clamav-client` のビルド時に `freshclam` で更新する。サンドボックスはネットワークが無いので実行時には更新できない。定義を新しくするには `docker compose build --pull sfsp-clamav-client`。
- sfsp-api と sfsp-worker は非 root（uid/gid 10001）で動く（`docker/api/Dockerfile`、`docker/worker/Dockerfile` の `USER 10001:10001`）。スキャン用ディレクトリ `/tmp/sfsp-scan` は 0755・10001 所有で、サンドボックスのスキャナー（`CapDrop: ALL`）が読めるようにしてある。既存の `sfsp_temp_scan` ボリュームは `sfsp-scan-volume-init` が毎回 chown する。
- sfsp-worker は Docker ソケットも `.env` もマウントしない。`DOCKER_HOST=tcp://sfsp-docker-proxy:2375` で `sfsp-docker-proxy` 経由でサンドボックスを起動し、設定は compose の environment で明示的に渡す。
- 使う Docker API: `_ping`、`GET /images/{name}/json`、`POST /containers/create`、`start`、`wait`、`logs`、`DELETE /containers/{name}`。イメージの pull はしない（ローカルに無ければ `failed`）。
- プロキシはリクエスト本文（Privileged・Binds 等）を検査しないので、完全な隔離ではない（[15 章](#15-既知の問題未対応)）。

### 12.3. ユーザーのコンテンツの配信

| 対象 | 対策 |
|---|---|
| ゲーム・静的サイト | `<id>.localhost` のサブドメインでのみ配信（メインオリジンの `/games/<id>/` は 404） |
| サムネイル・プロフィール画像 | ワーカーが `http.DetectContentType` で `image/jpeg` / `png` / `gif` / `webp` のときだけ保存。拡張子も判定した形式から付ける（ユーザーのファイル名を使わない） |
| 上記と HLS の配信 | `CSP: sandbox` と `nosniff`（[10.4](#104-ユーザーがアップロードしたファイルのヘッダー)） |

### 12.4. JWT を localStorage に置くことのトレードオフ

- 利点: Cookie を自動送信しないので CSRF が原理的に起きない。
- 欠点: アプリのオリジン（`localhost:3001`）で XSS が 1 つでも成立すると、トークンを盗まれる。
- そのため、**ユーザーがアップロードした HTML / JS をメインオリジンで実行させないこと**が最重要。新しい配信経路を追加するときは 12.3 の対策を必ず適用する。
- 静的サイトは `sandbox` 付きの iframe で表示し、カメラ機能などのため `allow-same-origin` を許可している（サブドメイン分離で補っている）。

### 12.5. その他

- SQL はすべてプレースホルダ（`$1`）を使う。
- React の自動エスケープに頼り、`dangerouslySetInnerHTML` は使わない。
- 秘密情報は `.env`（`.gitignore` 済み）。`.env.example` の値は公開済みなので本番では必ず変更する。

---

## 13. ビルドと依存関係の制約

1. **`replace` ディレクティブ**: shared を使う Go サービスの `go.mod` には `replace github.com/atmosidea/shared => ../../shared` がある。新しいサービスを追加するときも必ず書く。無いと `exit status 128`（GitHub への認証エラー）でビルドが失敗する。
2. **ビルドコンテキストはプロジェクトルート**: compose の `context: .`、Dockerfile は `COPY . .` → `WORKDIR /app/backend/<サービス>` の順。これで `../../shared` が解決される。この組み合わせを変えない。
3. **`go mod tidy` の扱い**: 多くの Dockerfile は `go mod download` だけを行う。例外として、`profile-worker`・`game-upload-api`・`monitoring-service/backend` は Dockerfile 内で `go mod tidy` を実行し、SFSP の Dockerfile は `go.sum` が無いときだけ実行する。依存を追加したら、ローカルの `go.mod` / `go.sum` も更新すること（`video-worker` は `-mod=readonly` 相当でビルドされるため、`go.sum` が欠けると失敗する）。
4. **最終イメージ**: `alpine:latest` に `ca-certificates` と `tzdata`。多くの Dockerfile がビルドステージで `.env` をイメージにコピーするため、**`.env` が無いとビルドが失敗する。**
5. **ローカルに Go が無い場合**: `docker run --rm -v "<リポジトリ>:/src:ro" -v atomosidea-gomodcache:/go/pkg/mod golang:1.25-alpine sh -c 'cd /src/<モジュール> && go build ./... && go vet ./...'` でビルドと vet を確認できる。
6. **改行コード**: リポジトリのソースは LF。Windows の Python などで書き換えると CRLF になることがあるので注意する。

---

## 14. よくある誤解（旧ドキュメントとの差分）

古いドキュメント（旧 EditAI.md・旧 README・AI_PROJECT_GUIDE.md・SECURITY_EXPLANATION.md）に書かれていたが、現在の実装とは異なる内容。

| 旧ドキュメントの記述 | 現在の実装 |
|---|---|
| PostgreSQL は 4 基 | **5 基**（`team-db` がある） |
| MinIO は 6 基（profile-storage / game-storage / static-site-storage / video-storage / sfsp-raw-minio / sfsp-clean-minio）。物理ディレクトリはサービスごと（`profile_storage_data` 等）、video-storage だけコンソールが 9003 | **MinIO は 1 基**（`minio:9000`）。データは `./backend/storage_data`。各サービスはバケットとユーザーで分離 |
| `video-storage` に `thumbnails` バケットがある | サムネイルは `videos` バケットの `thumbnails/` プレフィックス |
| `video-worker` が `sfsp:completed:stream` を受けて HLS 変換する | **HLS 変換は `video-upload-api` 内の goroutine**。video-worker は参照・更新 API と `video-thumbnail` キューを担当 |
| HLS は Nginx の `alias /storage/videos/...` でローカルファイルから配信 | Nginx が **MinIO の `videos/<id>/...` にプロキシ**。`upload_workdir` は変換時の一時領域にすぎない |
| 動画の削除は `if ($request_method = DELETE)` で振り分け | `if` は使っていない。`/api/videos/delete/:id` を rewrite して video-upload-api に転送 |
| `auth-service` / `profile-service` は `backend/auth/auth-worker/auth-service/` と `.../profile-service/` の別ディレクトリ | auth-service は `backend/auth/auth-worker/`、profile-service は `backend/profile-service/profile-worker/`、mypage-service は `backend/profile-service/mypage-worker/` |
| sfsp-api は SHA256 で重複を排除する | SHA256 は保存・索引付けするだけで、重複排除はしていない（`files.sha256` に UNIQUE も無い） |
| サンドボックスは `ReadonlyRootfs: true`、`tmpfs`、メモリ 2 GB | [12.2](#122-サンドボックスinternalsandboxdockergo) の設定（ネットワーク無し・CapDrop ALL 等）。`deploy.resources.limits.memory: 2g` は sfsp-worker コンテナ自体の制限 |
| `sfsp-isolated-net` は SFSP 専用で外部から遮断 | `internal: true` の bridge（外向き通信なし）だが SFSP 専用ではなく、アップロード API / ワーカー、Redis、MinIO、profile-service も参加している。サンドボックスのコンテナだけがネットワーク無し |
| `Dockerfile` 内の `RUN go mod tidy` は全削除済み | 一部の Dockerfile に残っている（[13 章](#13-ビルドと依存関係の制約)） |
| `minio-init` は各 MinIO の `service_started` を待つ | 単一 MinIO の `service_healthy` を待つ |
| フロントエンドに `UploadPage` / `UploadGamePage` / `UploadStaticSitePage` がある | `UnifiedUploadPage`（`/upload?type=...`）に統合。`TeamListPage` / `TeamDetailPage` が追加 |
| sfsp-api の `/health` は `/api/v1/health` | `/health`（`/api/v1` の外） |
| `/api/auth/user/:userId` は JWT 必須 | 認証なし |
| 管理者登録はコードだけで行い、既定値（`admin@internal.local` / 既定パスワード）で作成 | email・password・adminCode が必須。既定パスワードによる補完は廃止 |
| 管理用スクリプトに `test.bat`、`update_run.log` がある | 存在しない。`cleanup_auth_db.bat`・`cleanup_profiles.bat`・`cleanup_storage.bat`・`cleanup_sfsp_minio_data.bat` がある |
| profile-service のスキャン処理は `profile-worker` という別サービス | profile-service（ディレクトリ名は `profile-worker`）自身の goroutine が処理する。独立したコンテナは無い |
| （2026-10-26 以前の EditAI.md）Redis にパスワードは無い、ネットワークは `internal: true` でない、sfsp-worker が docker.sock と `.env` をマウント | Redis は `requirepass`。`backend-db-network` / `sfsp-isolated-net` などは `internal: true`。sfsp-worker は `sfsp-docker-proxy` 経由 |
| （同上）完了イベントは BRPOP で取り出す | `DequeueReliable`（BRPOPLPUSH）+ `Ack` + 起動時の `RecoverProcessing` |
| （同上）Google ログイン後は `/login/success?token=` | `/login/success?code=` + `POST /api/auth/oauth/exchange` |
| （同上）初期管理者 `admin@internal.local` が投入される | 投入しない |
| （同上）ブロックリストを見るのは auth-service だけ | JWT を検証する全サービスが確認する |
| （同上）`POST /api/profile/internal/create` は認証なし | `X-Internal-Key` 必須。正式なパスは `/internal/create` |

---

## 15. 既知の問題（未対応）

コードレビューで見つかった問題の一覧。15.1〜15.3 の行は 2026-10-26 の対応で解消済み（取り消し線 + 「対応済み」。履歴として残す）で、未対応のものは [15.4](#154-残っている問題2026-10-26-の対応後に判明) に残っている。**関連する箇所を変更するときは、この表を確認し、直したら行を削除せずに「対応済み」と追記する。**

### 15.1. セキュリティ

| 重大度 | 問題 | 場所 |
|---|---|---|
| High | ~~`POST /api/profile/internal/create` に認証が無く、Nginx 経由で外部から呼べる（任意の UUID でプロフィールを作れる）~~ **対応済み（2026-10-26）**: `X-Internal-Key` 必須、正式パスは `/internal/create`、Nginx で `/api/profile/internal/` と `/api/*/internal` を 404 | `profile-worker/main.go`、`nginx.conf` の `/api/profile` |
| High | ~~admin が owner を削除できる（対象のロールを確認していない）。owner が自分を削除するとチームに owner がいなくなる~~ **対応済み（2026-10-26）**: 自分より弱いロールだけ削除可、最後の owner は 409 | `team-worker/main.go` `removeMemberHandler` |
| High | ~~`POST /:token/members` が `team_id` に URL のトークンを渡しており常に 500。`role` を検証していないため、直すだけだと admin が owner を付与できる~~ **対応済み（2026-10-26）**: トークンから ID を引く。付与は自分より弱いロールのみ（owner→owner は移譲用の例外） | `team-worker/main.go` `addMemberHandler` |
| High | ~~`/:token/permission` が非公開チームでは JWT を見る前に `can_view:false` を返すため、メンバーでもチーム投稿できない~~ **対応済み（2026-10-26）**: JWT を先に評価し、メンバーは `can_view` / `can_post` を得る | `team-worker/main.go` `teamPermissionHandler` |
| High | ~~動画・ゲーム・サイトの詳細 API が `team_id` と `status` を確認せず、チーム限定・隔離済みのコンテンツも ID が分かれば見える。一覧の `?team=` もメンバー確認なし~~ **対応済み（2026-10-26）**: 詳細は投稿者・管理者以外 `public` のみ、`team_id` があれば `can_view` を確認。一覧の `?team=` も `can_view` 必須。**ただし Nginx から MinIO への直接配信は未対応（下の「残っている問題」）** | 各 `GET /:id`、一覧ハンドラー |
| High | ~~動画の公開一覧が `status = 'public'` で絞り込まれていない~~ **対応済み（2026-10-26）**: `status='public'` かつ（`?team=` 無しなら）`team_id IS NULL` | `video-worker/main.go` `listVideosHandler` |
| High | ~~Redis にパスワードが無く、同じネットワークのコンテナが完了イベントを偽造できる~~ **対応済み（2026-10-26）**: `requirepass`（`REDIS_PASSWORD`）を設定 | `docker-compose.yml` |
| High | ~~sfsp-worker が Docker ソケットと `.env` 全体をマウントしている~~ **対応済み（2026-10-26）**: `.env` マウントを廃止し、`sfsp-docker-proxy` 経由に変更（本文検査はしない点は残る） | `docker-compose.yml` |
| High | ~~一部のネットワークが `internal: true` でなく、sfsp-db を全サービスから参照できる~~ **対応済み（2026-10-26）**: `backend-db-network` / `sfsp-isolated-net` を `internal: true`、sfsp-db は専用の `sfsp-db-network` | `docker-compose.yml` |
| Medium | ~~ログアウトしたトークンは auth-service 以外では有効期限まで使える~~ **対応済み（2026-10-26）**: JWT を検証する全サービスがブロックリストを確認（fail closed） | 各サービスの `authMiddleware` |
| Medium | ~~OAuth の state が「パラメータも Cookie も空」だと一致扱いになる。Cookie のドメインが `localhost` 固定、使用後に削除しない~~ **対応済み（2026-10-26）**: 空を拒否・定数時間比較・host-only Cookie・使用後に削除 | `auth-worker/main.go` `googleCallbackHandler` |
| Medium | ~~Google の userinfo の HTTP ステータスを確認せず、`ID[:8]` で panic しうる~~ **対応済み（2026-10-26）**: ステータス・必須項目・`verified_email` を確認、切り出し前に長さ確認 | `auth-worker/main.go` `getGoogleUserData` |
| Medium | ~~JWT をリダイレクト URL のクエリに載せている（ログ・履歴に残る）~~ **対応済み（2026-10-26）**: 60 秒・1 回限りのコード + `POST /api/auth/oauth/exchange` | `auth-worker/main.go`（`/login/success?token=`） |
| Medium | ~~ログイン時に `status` を確認しない（`deleted_data` でもログインできる）~~ **対応済み（2026-10-26）**: パスワード・Google ログインと `authMiddleware` で拒否 | `auth-worker/main.go` `loginHandler` |
| Medium | ~~初期管理者 `admin@internal.local` のパスワードが `init.sql` に書かれており公開済み~~ **対応済み（2026-10-26）**: `init.sql` から削除（既存ボリュームは 5.1 の SQL で削除） | `backend/auth/auth-datebase/auth-db/init.sql` |
| Medium | ~~sfsp-api に認証が無い~~ **対応済み（2026-10-26）**: `X-SFSP-API-Key` 必須、キー未設定なら起動しない | `cmd/sfsp-api/main.go` |
| Medium | ~~DB ロールのパスワード（`team_service_strong_password`、`sfsp_worker_password` など）が SQL に直書き~~ **対応済み（2026-10-26）**: SQL から削除し、`db-role-init` が `.env` の値で `ALTER ROLE` | 各 `init.sql`、`docker-compose.yml` の既定値 |
| Low | ~~`APP_DATABASE_URL` を含むエラーログでパスワードが出る~~ **対応済み（2026-10-26）**: `RedactURL` / `RedactString` で伏せてから出力 | `cmd/sfsp-worker/main.go` |
| Low | ~~監視ダッシュボードにログイン認証が無い（ループバック限定で運用）~~ **対応済み（2026-10-26）**: mon-backend と mon-nginx で HTTP Basic 認証 | `monitoring-service/` |

### 15.2. 信頼性・データ整合性

| 問題 | 場所 |
|---|---|
| ~~BRPOP で取り出した後に処理が失敗・再起動するとイベントが失われ、`scanning` / `processing` / `running` のまま残る（再試行・回収の仕組みが無い）~~ **対応済み（2026-10-26）**: 全ワーカーと sfsp-worker を `DequeueReliable` / `Ack` / `RecoverProcessing` に変更 | 全ワーカー、sfsp-worker |
| ~~サムネイルのイベントが DB の INSERT より先に届くと捨てられる（ゲーム・静的サイト）~~ **対応済み（2026-10-26）**: 行を先に INSERT し、ワーカーは見つからなければ待って再検索 | `game-upload-api`、`static-site-upload-api` |
| ~~一時ファイル名にユーザーのファイル名を使うため、同名ファイルの同時アップロードで内容が混ざる~~ **対応済み（2026-10-26）**: ジョブごとの一時ディレクトリ / `os.CreateTemp` | `video-upload-api`（`UPLOAD_DIR/<filename>`）、`static-site-worker`（`os.TempDir()/<filename>`） |
| ~~アップロードサイズの上限が無く、zip を丸ごとメモリに読む。zip bomb への対策も無い~~ **対応済み（2026-10-26）**: 各 API に `MaxBytesReader`（413）とストリーミング送信、各ワーカーと sfsp-worker の展開に上限 | 各アップロード API、`game-worker` / `sfsp-worker` の展開処理 |
| ~~ffmpeg の同時実行数に上限が無く、タイムアウトも無い~~ **対応済み（2026-10-26）**: `FFMPEG_MAX_CONCURRENCY` / `FFMPEG_TIMEOUT` | `video-upload-api` |
| ~~動画を処理中に削除すると、変換結果が MinIO に残る~~ **対応済み（2026-10-26）**: 削除時にジョブをキャンセルし、ジョブ側でも後始末 | `video-upload-api` |
| ~~静的サイトのサムネイルを差し替えても古いオブジェクトが消えない~~ **対応済み（2026-10-26）**: 確定後に古いオブジェクトを削除 | `static-site-worker` |
| ~~`profile-worker` のループ内で `defer` を使っており、ファイルディスクリプタと一時ファイルが溜まる~~ **対応済み（2026-10-26）**: 1 件ごとの関数に分離 | `profile-worker/main.go` の完了イベント処理 |
| ~~アカウント削除が profile-db の行と team-db のメンバー情報を削除しない。動画はローカルパスで削除しようとしており MinIO の HLS が残る~~ **対応済み（2026-10-26）**: profile-service / team-service の内部 API を呼び、動画は MinIO の `videos/<id>/` を削除 | `auth-worker/main.go` `deleteAccountHandler` |
| ~~チーム更新で `description` を省略すると NULL で上書きされる。一度却下・取り消しされたユーザーは `UNIQUE(team_id, user_id)` により再申請できない~~ **対応済み（2026-10-26）**: 送られたフィールドだけ更新、再申請は UPSERT で `pending` に戻す | `team-worker/main.go` |
| ~~team-service が `app-db.users` を参照するが、そのテーブルは無い（`author_name` が常に空）~~ **対応済み（2026-10-26）**: profile-service から名前を取得 | `team-worker/main.go` |
| ~~ClamAV の既定のファイルサイズ上限を超えるファイル（大きな動画など）は検査されない~~ **対応済み（2026-10-26）**: 上限を明示し `--alert-exceeds-max` で超過は `suspicious`（1 ファイル 2 GiB-1 が ClamAV の限界） | `scanner/clamav.go` |
| ~~`video-upload-api` の予備の `CREATE TABLE` に `thumbnail_sfsp_job_id` / `updated_at` が無い~~ **対応済み（2026-10-26）**: `init.sql` と同じ定義 + `ADD COLUMN IF NOT EXISTS` | `video-upload-api/main.go` |

### 15.3. 機能の不具合

| 問題 | 場所 |
|---|---|
| ~~**動画のサムネイル削除（`DELETE /api/videos/:id/thumbnail`）が常に 404**。実装は video-upload-api にあるが、Nginx の `/api/videos` が video-worker に転送する~~ **対応済み（2026-10-26）**: `nginx.conf` に `~ ^/api/videos/[a-zA-Z0-9]+/thumbnail$` → video-upload-api の location を追加 | `frontend/nginx.conf`、`EditVideoPage.tsx` |
| ~~`create_go_work.bat` が古いパス（`backend/auth/auth-worker/auth-service` など）を参照している~~ **対応済み（2026-10-26）**: 実在する 12 モジュールに修正 | `create_go_work.bat` |

### 15.4. 残っている問題（2026-10-26 の対応後に判明）

| 重大度 | 問題 | 場所 |
|---|---|---|
| High | Nginx が MinIO に直接プロキシする経路（HLS `/api/videos/<id>/stream/*`、`/videos\|games\|static-sites/thumbnails/*`、サブドメイン `<id>.localhost`）に認可が無い。チーム限定・処理中のコンテンツも ID が分かれば取得できる（隔離済みファイルは公開バケットに置かれないので対象外）。`<img>`・hls.js・サブドメインには JWT が届かないため、署名付き URL か Cookie など別の仕組みが要る | `frontend/nginx.conf`、各バケットの匿名読み取りポリシー（game-worker の `ensureBucket` を含む） |
| Medium | `sfsp-docker-proxy` はリクエスト本文を検査しないため、sfsp-worker が乗っ取られると Privileged コンテナやホストのマウントを作れる | `docker-compose.yml` |
| Medium | 多くの Dockerfile が `COPY --from=builder /app/.env ./.env` で `.env` をイメージに焼き込み、アプリ側サービスは `env_file: .env` で全サービスの秘密情報を受け取っている | 各 `Dockerfile`、`docker-compose.yml` |
| Medium | `POSTGRES_PASSWORD` と MinIO の各キーは `.env.example` で公開済みの値のまま。変更するにはボリュームの作り直しが必要 | `.env` |
| Low | Redis に job ID が無いまま `running` で止まった過去のスキャンジョブは回収されない | sfsp-worker |
| Low | `docker compose` のプロジェクト名がフォルダ名から決まるため、フォルダ名が違うと別プロジェクトとして新しい空のボリュームが作られ、既存の DB が見えなくなる（稼働中のスタックは `atomosidea-claude`、このフォルダ `Atomosidea-work(claude)` から導出される名前は `atomosidea-workclaude`）。`.env` の `COMPOSE_PROJECT_NAME` か compose の `name:` で固定する | `docker-compose.yml`、各 `.bat` |
| Low | 処理エラー時の status がサービスで違う（動画 `error`、ゲーム `failed`、静的サイト `error` / `failed`） | 各ワーカー |
| Low | 削除済みの Google ユーザーは同じ Google アカウントで再登録できない。ログイン可能な status に `pending` も含めている | `auth-worker/main.go` |

---

## 16. トラブルシューティング

| 症状 | 原因と対処 |
|---|---|
| サービスが起動しない | `docker compose logs <サービス>`。`.env` の不足、依存サービスの起動失敗が多い |
| ビルドで `COPY .env` が失敗 | `.env` が無い。`cp .env.example .env` |
| `exit status 128` | `go.mod` の `replace github.com/atmosidea/shared => ../../shared` が無い |
| `replacement directory ../shared does not exist` | ビルドコンテキストがルートでない。`COPY . .` と `WORKDIR` を確認 |
| `missing go.sum entry` / `updates to go.mod needed` | 依存を追加した後に `go.sum` を更新していない |
| MinIO の `Access Denied` / `InvalidAccessKeyId` | `.env` のキーと `minio-init` のユーザーが一致しているか。`docker logs atmosidea-minio-init` を確認。一時的なエラーは video の HLS アップロードでは最大 5 回リトライされる |
| アップロードが `scanning` のまま | sfsp-worker のログで `Finished processing job: <id>` まで進んだか。完了イベントのキュー名と `target_service` が一致しているか |
| 正常なファイルが `malicious` / `failed` | `sfsp-db.scan_results.raw_output` を確認。サンドボックスの Entrypoint を変更していないか（[12.2](#122-サンドボックスinternalsandboxdockergo)） |
| ClamAV の「virus database is older than 7 days」警告 | `docker compose build --pull sfsp-clamav-client` |
| ログイン直後にログアウトされる | `/api/profile/me` の応答。401 以外ではログアウトしない実装になっているか |
| サブドメインで `AccessDenied` | `nginx.conf` のサブドメイン用 rewrite に `break` が付いていないか（[10.5](#105-サブドメインゲーム静的サイト)） |
| Nginx の構文エラー | `server_name` の正規表現を引用符で囲んでいるか。`docker run --rm --add-host <各upstream>:127.0.0.1 -v .../nginx.conf:/etc/nginx/nginx.conf:ro nginx:alpine nginx -t` で確認できる |
| HLS が再生できない | URL が `/api/videos/<id>/stream/playlist.m3u8` か。MinIO の `videos/<id>/` にファイルがあるか |
| `upload_workdir` が無い | 問題なし。バインドマウント時に自動作成される |
| `network ... needs to be recreated` | ネットワーク定義が変わった。監視スタックを止めて `docker compose down` → `up -d --build` |
| Redis の `NOAUTH` / sfsp-api が起動しない | `.env` の `REDIS_PASSWORD` / `SFSP_API_KEY` が空でないか。`.env.example` から作り直した場合は値を設定する |
| サービス間呼び出しが 401 / 403 | `X-SFSP-API-Key`（sfsp-api）や `X-Internal-Key`（内部 API）の値が `.env` と一致しているか |
| `db-role-init` がエラーで止まる | 古いボリュームに `sfsp_worker` などのロールが無い。init.sql の該当部分を手動で流すかボリュームを作り直す |

---

## 17. 変更時のチェックリスト

1. **正のソース**は `docker-compose.yml` と `backend/` の実ファイル。ドキュメントと違えば実装を優先し、ドキュメントを直す。
2. **パス・テーブル・カラム・キュー名・環境変数名**を変えたら、このドキュメントの該当箇所を同じ変更で更新する。
3. **Go サービス**: `replace` ディレクティブ、ルートのビルドコンテキスト、`COPY . .` + `WORKDIR` を守る。依存を追加したら `go.mod` / `go.sum` も更新する。`go build ./...` と `go vet ./...` を通す。
4. **キュー・イベント**: `backend/shared/queue` と `backend/security/sfsp/internal/queue`（・`model`）の両方を直す。`target_service` を追加したら sfsp-api の許可リストも直す。完了イベントは `DequeueReliable` で取り出し、終端状態を記録してから `Ack` する（BRPOP を使わない）。
5. **Nginx**: `proxy_pass` の末尾スラッシュ禁止。location の評価順。`/api/videos/...` に新しいエンドポイントを video-upload-api 側で追加したら、専用の location も追加する（追加しないと video-worker に転送される）。location 内の `add_header` は server のものを継承しない。`nginx -t` → `docker compose restart frontend`。
6. **ユーザーのファイルを配信する経路を追加・変更**: メインオリジンで HTML / JS を実行させない（サブドメインで配信するか、`CSP: sandbox` + `nosniff` を付ける）。サムネイル等は画像形式のみ保存する。
7. **スキャン**: 判定は終了コードで行う。スキャナーのエラーは公開しない側（`failed`）に倒す。zip は展開した全体をスキャンする。サンドボックスの制限を緩めない。
8. **認可**: 編集・削除のエンドポイントには authMiddleware と投稿者チェックを付ける。チームはロールのランクで判定し、自分より強いロールの付与・削除を許さない。JWT の検証では HS256 固定と `blocklist:<jti>` の確認を必ず行う。サービス間の内部 API は `/api` の外に置き、`X-Internal-Key` で守る。sfsp-api の呼び出しには `X-SFSP-API-Key` を付ける。
9. **MinIO のユーザー・ポリシー**: `minio-init`（environment / entrypoint / volumes）と `backend/minio-init/*.json` と `.env.example` を対で更新する。
10. **DB スキーマ**: 該当 DB の `init.sql` を更新する（既存ボリュームには反映されないので、必要ならマイグレーション SQL を追加する）。
11. **JWT・localStorage**: 12.4 のトレードオフを理解したうえで変更する。
12. **大規模な削除**（ファイル・ドキュメントの節・データ）は必ずユーザーの許可を取る。

---

## 18. セットアップと管理用スクリプト

### 18.1. セットアップ

1. `cp .env.example .env` を実行し、パスワード・`JWT_SECRET`・`ADMIN_REGISTRATION_CODE`・MinIO のキーを変更する（`.env.example` の値は公開済み）。Google ログインを使う場合は `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` を設定する。
2. `setup.bat`（初回）または `docker compose up -d --build`。
3. `http://localhost:3001` を開く。
4. 「新規登録」→「管理者として登録」で自分の管理者を作る（初期管理者 `admin@internal.local` は投入されない。古いボリュームに残っている場合は [5.1](#51-auth-db) の SQL で削除する）。
5. 任意: `monitoring-service/scripts/start.bat`（または `docker compose --env-file ../.env -f docker-compose.monitoring.yml up -d --build`）→ `http://127.0.0.1:8090`。ログインは `.env` の `MONITOR_USERNAME` / `MONITOR_PASSWORD`。

### 18.2. スクリプト

| スクリプト | 内容 |
|---|---|
| `setup.bat` | 初回。`.env` を対話的に作り、ビルドして起動 |
| `start.bat` | `.env` の有無を確認して `docker compose up -d`。公開されているのは `http://localhost:3001` だけ（MinIO コンソールは非公開）。監視ダッシュボードの起動手順も表示 |
| `stop.bat` | `docker compose down`（DB と MinIO のデータは残る） |
| `update.bat` | 全停止（ネットワーク定義の変更に備える）→ キャッシュなしで再ビルド → 起動 |
| `clean.bat` | `docker compose down -v --remove-orphans --rmi local` とビルドキャッシュの削除。DB のボリュームは消えるが、MinIO のデータ（`backend/storage_data/`）は消えない |
| `cleanup_games.bat` | `app_db.public.games` を TRUNCATE し、`games` バケットを空にする（稼働中の `atmosidea-app-db` と `atmosidea-minio` に対して実行） |
| `cleanup_static_sites.bat` | `app_db.public.static_sites` を TRUNCATE し、`static-sites` バケットを空にする |
| `cleanup_videos.bat` | `videos` バケットと `upload_workdir` を空にする（DB レコードは残る） |
| `cleanup_profiles.bat` | `user-profiles` バケットを空にする（profile-db は触らない） |
| `cleanup_storage.bat` | `videos` / `games` / `user-profiles` / `static-sites` バケットと `upload_workdir` を空にする（DB と SFSP 系バケットは触らない） |
| `cleanup_sfsp_minio_data.bat` | `raw-files` / `clean-files` / `quarantine` バケットを空にする（スキャン実行中は使わない） |
| `cleanup_auth_db.bat` | auth-db のボリューム（アカウント情報）を削除。ボリューム名は実行時に導出する |
| `create_go_work.bat` | `go.work` の作成（実在する 12 モジュールのパスに修正済み。[15.3](#153-機能の不具合)） |

### 18.3. 関連ドキュメント

- `README.md`: 人間向けの概要とクイックスタート
- `AI_PROJECT_GUIDE.md`: AI 向けの入門（考え方・トラップ）
- `SECURITY_EXPLANATION.md`: セキュリティ設計の説明（一部の記述は [14 章](#14-よくある誤解旧ドキュメントとの差分) のとおり古い）
- `Docs/api_list.md`、`Docs/Nginx.md`、`Docs/MinIO_DB.md`: 補足資料
- `backend/security/sfsp/docs/`: SFSP の ADR・Runbook・脅威モデル・インシデント対応
