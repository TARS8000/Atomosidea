# Atomosidea APIリスト

本ドキュメントは、Atomosideaが提供するAPIエンドポイントの一覧です。

すべてのAPIはフロントエンドのNginx（APIゲートウェイ）経由でアクセスします。ベースパスの `/api` 以降を各サービスが受け付けます。認証が必要なエンドポイントは、リクエストヘッダーに `Authorization: Bearer <jwt_token>` を付与する必要があります。

## 目次

- [認証について](#認証について)
- [Auth Service](#auth-service)
- [Profile Service](#profile-service)
- [Video Upload API & Stream Service](#video-upload-api--stream-service)
- [Game Upload API](#game-upload-api)
- [Static Site Upload API](#static-site-upload-api)
- [MyPage Service](#mypage-service)
- [Security File Scan Platform (SFSP)](#security-file-scan-platform-sfsp)
- [Monitoring Service](#monitoring-service)

---

## 認証について

- 認証方式は JWT（JSON Web Token）です。
- ログイン成功時に発行されたトークンを `Authorization` ヘッダーに付与します。
- 書式: `Authorization: Bearer <jwt_token>`
- 管理者専用アカウントはローカル認証（メール・パスワード）で作成できます。一般ユーザーは Google OAuth2 認証を使用します。

---

## Auth Service

- **ベースパス:** `/api/auth`
- **担当サービス:** `auth-service`
- **責務:** ユーザー認証、セッション管理、アカウントライフサイクル

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/register` | 不要 | 管理者アカウントの登録。初回起動時などに使用します。`ADMIN_REGISTRATION_CODE` が必要です。 | `{"email": "...", "password": "...", "adminCode": "..."}` | `{"message": "Admin user created successfully", "userID": "..."}` |
| `POST` | `/login` | 不要 | ローカルログイン。メールとパスワードで認証し、JWTを返却します。 | `{"email": "...", "password": "..."}` | `{"token": "jwt_token_string"}` |
| `GET` | `/google/login` | 不要 | Google OAuth2認証の開始。ユーザーをGoogleの認証ページにリダイレクトします。 | （なし） | （リダイレクト） |
| `GET` | `/google/callback` | 不要 | Google OAuth2認証のコールバック。Googleからの応答を処理し、ユーザーを登録またはログインさせてJWTを付与してフロントエンドにリダイレクトします。 | （クエリパラメータ） | （リダイレクト） |
| `POST` | `/logout` | JWT | ログアウト。現在のセッションで使用しているJWTをRedisのブロックリストに追加し、無効化します。 | （なし） | `{"message": "Successfully logged out"}` |
| `DELETE` | `/me` | JWT | アカウント削除。認証ユーザーのアカウントと、関連するすべてのコンテンツ（プロフィール画像、動画、ゲーム、静的サイト）を完全に削除します。 | （なし） | `{"message": "アカウントデータが正常に削除されました。"}` |
| `GET` | `/user/:userId` | JWT | ユーザーの認証プロバイダ取得。指定したユーザーIDが `local` か `google` かを返します。 | （なし） | `{"provider": "google"}` |

---

## Profile Service

- **ベースパス:** `/api/profile`
- **担当サービス:** `profile-service`
- **責務:** ユーザープロフィールの操作

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `GET` | `/me` | JWT | 自分のプロフィール取得。認証ユーザー自身の完全なプロフィール情報を取得します。 | （なし） | `{"id": "...", "username": "...", "bio": "...", ...}` |
| `GET` | `/:userId` | 不要 | 特定ユーザーのプロフィール取得。指定したユーザーIDの公開プロフィール情報を取得します。 | （なし） | `{"id": "...", "username": "...", "bio": "...", ...}` |
| `GET` | `/status` | JWT | 自分のステータス取得。認証ユーザーのアカウントステータスを取得します。 | （なし） | `{"status": "active"}` |
| `PUT` | `` | JWT | プロフィール更新。認証ユーザーのユーザー名と自己紹介を更新します。 | `{"username": "New Name", "bio": "New Bio"}` | `{"message": "Profile updated successfully"}` |
| `PUT` | `/icon` | JWT | アイコン更新。認証ユーザーのプロフィールアイコンをSFSPでスキャン後に更新します。 | `multipart/form-data`（key: `icon`） | `{"message": "Icon upload accepted for scanning", "job_id": "...", "status": "scanning"}` |
| `PUT` | `/background` | JWT | 背景画像更新。認証ユーザーの背景画像をSFSPでスキャン後に更新します。 | `multipart/form-data`（key: `background`） | `{"message": "Background upload accepted for scanning", "job_id": "...", "status": "scanning"}` |
| `POST` | `/internal/create` | 内部 | 内部用プロフィール作成。`auth-service`からのリクエストで、新規ユーザーの初期プロフィールレコードを作成します。 | `{"user_id": "...", "username": "..."}` | `{"message": "Profile initialized successfully"}` |

---

## Video Upload API & Stream Service

- **ベースパス:** `/api/videos`
- **担当サービス:** `video-upload-api`（アップロード・削除）、`video-worker`（一覧・詳細・ストリーミング）
- **責務:** 動画のアップロード受付、メタデータ管理、ストリーミング配信

| メソッド | エンドポイント | サービス | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|---|
| `POST` | `/upload` | `video-upload-api` | JWT | 動画アップロード。動画ファイルとメタデータを受け取り、スキャンと変換プロセスを開始します。 | `multipart/form-data`（keys: `video`、`title`、`description`） | `{"message": "Video upload initiated for scanning", "videoID": "..."}` |
| `DELETE` | `/delete/:id` | `video-upload-api` | JWT | 動画削除。指定した動画と関連ファイル（HLS、サムネイル）を削除します。 | （なし） | `{"message": "Video deleted successfully"}` |
| `GET` | `` | `video-worker` | 不要 | 動画リスト取得。公開済みの動画リストを検索クエリ付きで取得します。 | （クエリ: `q=search_term`） | `[{"id": "...", "title": "...", ...}]` |
| `GET` | `/:id` | `video-worker` | 不要 | 動画詳細取得。指定した動画のメタデータを取得します。 | （なし） | `{"id": "...", "title": "...", ...}` |
| `PUT` | `/:id` | `video-worker` | JWT | 動画メタデータ更新。指定した動画のタイトルと説明を更新します。 | `{"title": "...", "description": "..."}` | `{"message": "Video updated successfully"}` |
| `GET` | `/:id/stream/playlist.m3u8` | `video-worker` | 不要 | 動画ストリーミング。HLSのマスタープレイリストを取得します。 | （なし） | （HSLマニフェスト） |

---

## Game Upload API

- **ベースパス:** `/api/games`
- **担当サービス:** `game-upload-api`
- **責務:** ゲームコンテンツのアップロードと管理

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/upload` | JWT | ゲームアップロード。ゲームのzipファイルとメタデータを受け取り、スキャンと展開プロセスを開始します。 | `multipart/form-data`（keys: `game`、`thumbnail`、`title`、`description`） | `{"message": "Game upload accepted", "gameId": "..."}` |
| `GET` | `` | 不要 | ゲームリスト取得。公開済みのゲームリストを検索クエリ付きで取得します。 | （クエリ: `q=search_term`） | `[{"id": "...", "title": "...", ...}]` |
| `GET` | `/:id` | 不要 | ゲーム詳細取得。指定したゲームのメタデータを取得します。 | （なし） | `{"id": "...", "title": "...", ...}` |
| `PUT` | `/:id` | JWT | ゲームメタデータ更新。指定したゲームのタイトル、説明、サムネイルを更新します。 | `multipart/form-data`（keys: `title`、`description`、`thumbnail`） | `{"message": "Game details updated successfully"}` |
| `PUT` | `/adjust/:id` | JWT | ゲーム表示調整。ゲームの表示スケールとオフセットを更新します。 | `{"scale": 1.0, "offset_x": 0, "offset_y": 0}` | `{"message": "Adjustments saved successfully"}` |
| `DELETE` | `/:id` | JWT | ゲーム削除。指定したゲームと関連するMinIO上のファイルをすべて削除します。 | （なし） | `{"message": "Game deleted successfully"}` |

---

## Static Site Upload API

- **ベースパス:** `/api/static-sites`
- **担当サービス:** `static-site-upload-api`
- **責務:** 静的サイトコンテンツのアップロードと管理

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/upload` | JWT | static-siteアップロード。サイトのzipファイルとメタデータを受け取り、スキャンと展開プロセスを開始します。 | `multipart/form-data`（keys: `file`、`thumbnail`、`title`、`description`） | `{"message": "Static site upload initiated for scanning", "siteId": "..."}` |
| `GET` | `` | 不要 | static-siteリスト取得。公開済みのサイトリストを検索クエリ付きで取得します。 | （クエリ: `q=search_term`） | `[{"id": "...", "title": "...", ...}]` |
| `GET` | `/:id` | 不要 | static-site詳細取得。指定したサイトのメタデータを取得します。 | （なし） | `{"id": "...", "title": "...", ...}` |
| `PUT` | `/:id` | JWT | static-siteメタデータ更新。指定したサイトのタイトルと説明を更新します。 | `{"title": "...", "description": "..."}` | `{"message": "Static site updated successfully"}` |
| `DELETE` | `/:id` | JWT | static-site削除。指定したサイトと関連するMinIO上のファイルをすべて削除します。 | （なし） | `{"message": "Static site deleted successfully"}` |

---

## MyPage Service

- **ベースパス:** `/api/my`
- **担当サービス:** `mypage-service`
- **責務:** 認証ユーザーのコンテンツ集約

| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `GET` | `/videos` | JWT | 自分の動画リスト取得。認証ユーザーがアップロードした動画のリストを返します。 |
| `GET` | `/games` | JWT | 自分のゲームリスト取得。認証ユーザーがアップロードしたゲームのリストを返します。 |
| `GET` | `/static-sites` | JWT | 自分のstatic-siteリスト取得。認証ユーザーがアップロードしたstaticサイトのリストを返します。 |

---

## Security File Scan Platform (SFSP)

- **ベースパス:** `/api/v1`
- **担当サービス:** `sfsp-api`
- **責務:** ファイルのセキュリティスキャン受付と結果提供（主に内部サービス向け）

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/files` | 内部 | ファイルスキャン依頼。各アップロードサービスからファイルを受け付け、スキャンジョブを作成・キューイングします。 | `multipart/form-data`（keys: `file`、`target_service`） | `{"fileID": "...", "jobID": "...", "status": "queued"}` |
| `GET` | `/jobs/:id` | 内部 | ジョブステータス確認。指定したジョブIDの現在の状態（`queued`、`running`、`completed` 等）を返します。 | （なし） | `{"id": "...", "status": "running", ...}` |
| `GET` | `/results/:id` | 内部 | スキャン結果取得。完了したジョブIDのスキャン結果（ClamAV、YARA 等）の詳細を返します。 | （なし） | `[{"scanner": "clamav", "result": "clean", ...}]` |
| `GET` | `/health` | 不要 | ヘルスチェック。サービスの稼働状況を確認します。 | （なし） | `{"status": "ok"}` |

---

## Monitoring Service

- **ベースパス:** `/api`（監視サービス内部）
- **担当サービス:** `monitoring-service`
- **アクセス:** `http://localhost:8090`
- **責務:** 開発者向けにシステム全体の状態を提供

| メソッド | エンドポイント | 説明 |
|---|---|---|
| `GET` | `/containers` | 稼働中の全コンテナのリスト（ID、名前、状態 等）を取得します。 |
| `GET` | `/containers/stats` | 全コンテナのCPU・メモリ使用率などの統計情報を取得します。 |
| `POST` | `/containers/restart/{name}` | 指定した名前のコンテナを再起動します。 |
| `GET` | `/connections/count` | `atmosidea-frontend`のログを解析し、直近5分間のユニークIPアドレス数をカウントしてアクティブユーザー数を推定します。 |
| `GET` | `/minio/list/{containerName}` | 指定したMinIOコンテナ内のバケットやオブジェクトを一覧表示します。（クエリ: `bucket`、`prefix`） |
| `POST` | `/minio/upload/{containerName}` | 指定したMinIOコンテナのバケットにファイルをアップロードします。（クエリ: `bucket`、`prefix`） |
| `DELETE` | `/minio/delete/{containerName}` | 指定したMinIOコンテナのバケットからオブジェクトを削除します。（クエリ: `bucket`、`key`） |
| `GET` | `/ws/logs` | WebSocket接続を確立し、指定したコンテナのログをリアルタイムにストリーミングします。（クエリ: `container`） |

---

## 補足

- 各エンドポイントの詳細（内部処理ロジックや関連ファイル）は、プロジェクトの `README.md` の「APIエンドポイント仕様」セクションに記載されています。
- 動画ストリーミングはHLS形式を使用します。フロントエンドは `/api/videos/:id/stream/playlist.m3u8` 経由でNginxを通じ、ローカルのHLSファイルにアクセスします。