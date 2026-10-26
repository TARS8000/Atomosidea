# Atomosidea APIリスト

本ドキュメントは、Atomosideaが提供するAPIエンドポイントの一覧です。

すべてのAPIはフロントエンドのNginx（APIゲートウェイ）経由でアクセスします。Nginx はリクエストURIを書き換えずにそのまま各サービスへ転送し（動画の削除とHLS配信のみ例外）、各サービスが `/api/...` を含むパスでルーティングします。認証が必要なエンドポイントは、リクエストヘッダーに `Authorization: Bearer <jwt_token>` を付与する必要があります。サービス間の内部API（`/internal/...`）は Nginx が 404 を返すため、外部からは呼び出せません。

表の「認証」列の表記: `不要` = 認証なし、`任意` = JWT があれば本人として扱う（無くても呼び出せる）、`JWT` = JWT 必須、`内部` = `X-Internal-Key` ヘッダー（`INTERNAL_API_KEY` と一致する値）が必要。

## 目次

- [認証について](#認証について)
- [Auth Service](#auth-service)
- [Profile Service](#profile-service)
- [Video Upload API & Stream Service](#video-upload-api--stream-service)
- [Game Upload API](#game-upload-api)
- [Static Site Upload API](#static-site-upload-api)
- [MyPage Service](#mypage-service)
- [Team Service](#team-service)
- [Security File Scan Platform (SFSP)](#security-file-scan-platform-sfsp)
- [Monitoring Service](#monitoring-service)

---

## 認証について

- 認証方式は JWT（JSON Web Token）です。
- ログイン成功時に発行されたトークンを `Authorization` ヘッダーに付与します。
- 書式: `Authorization: Bearer <jwt_token>`
- 管理者専用アカウントはローカル認証（メール・パスワード）で作成できます。一般ユーザーは Google OAuth2 認証を使用します。
- ログアウトまたはアカウント削除で無効化されたトークン（Redis のブロックリスト）は、JWT を検証するすべてのサービスで拒否されます。
- Google ログイン後は JWT を URL に載せず、60 秒・1 回限りのコードを `POST /api/auth/oauth/exchange` でトークンに交換します。

---

## Auth Service

- **ベースパス:** `/api/auth`
- **担当サービス:** `auth-service`
- **責務:** ユーザー認証、セッション管理、アカウントライフサイクル

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/register` | 管理者コード | 管理者アカウントの登録。初回起動時などに使用します。`adminCode` が `ADMIN_REGISTRATION_CODE` と一致する必要があります。メール・パスワード（8文字以上）は必須です。201 / 400 / 403（コード不一致）/ 409（登録済み）。 | `{"email": "...", "password": "...", "adminCode": "..."}` | `{"message": "Admin user created successfully", "userID": "..."}` |
| `POST` | `/login` | 不要 | ローカルログイン。メールとパスワードで認証し、JWTを返却します。認証失敗は 401、無効化されたアカウントは 403。 | `{"email": "...", "password": "..."}` | `{"token": "jwt_token_string"}` |
| `GET` | `/google/login` | 不要 | Google OAuth2認証の開始。ユーザーをGoogleの認証ページにリダイレクトします。`GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` が設定されているときだけ登録されるルートです。 | （なし） | （リダイレクト） |
| `GET` | `/google/callback` | 不要 | Google OAuth2認証のコールバック。Googleからの応答を処理し、ユーザーを登録またはログインさせたうえで、`/login/success?code=...` にリダイレクトします（コードは 60 秒・1 回限りで、JWT 自体は URL に載せません）。失敗時は `/login?error=...` にリダイレクトします。 | （クエリパラメータ） | （リダイレクト） |
| `POST` | `/oauth/exchange` | 不要 | OAuth ワンタイムコードの交換。コールバックで受け取ったコードをJWTに交換します。200 / 400（`code` 無し）/ 401（無効または期限切れ）。 | `{"code": "..."}` | `{"token": "jwt_token_string"}` |
| `POST` | `/logout` | JWT | ログアウト。現在のセッションで使用しているJWTをRedisのブロックリストに追加し、無効化します。 | （なし） | `{"message": "Successfully logged out"}` |
| `DELETE` | `/me` | JWT | アカウント削除。認証ユーザーのプロフィール（profile-service）、チームのメンバー情報（team-service）、プロフィール画像、動画、ゲーム、静的サイトを削除します。ローカルアカウントは行を削除し、Googleアカウントは `status` を `deleted_data` にします。使用中のトークンも無効化されます。一部の後始末に失敗しても 200 を返し、失敗した項目を `cleanupErrors` に入れます（その場合の `message` は「アカウントは削除されましたが、一部のデータの削除に失敗しました。」）。auth-db の更新に失敗した場合は 500。 | （なし） | `{"message": "アカウントデータが正常に削除されました。", "cleanupErrors": []}` |
| `GET` | `/user/:userId` | 不要 | ユーザーの認証プロバイダ取得。指定したユーザーIDが `local` か `google` かを返します（UUID 形式でなければ 400、存在しなければ 404）。 | （なし） | `{"provider": "google"}` |

---

## Profile Service

- **ベースパス:** `/api/profile`
- **担当サービス:** `profile-service`
- **責務:** ユーザープロフィールの操作

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `GET` | `/me` | JWT | 自分のプロフィール取得。認証ユーザー自身の完全なプロフィール情報を取得します。プロフィール行が無い場合はその場で作成します。 | （なし） | `{"id": "...", "username": "...", "bio": "...", ...}` |
| `GET` | `/:userId` | 不要 | 特定ユーザーのプロフィール取得。指定したユーザーIDの公開プロフィール情報を取得します（UUID 形式でなければ 400、存在しなければ 404）。 | （なし） | `{"id": "...", "username": "...", "bio": "...", ...}` |
| `GET` | `/status` | JWT | 自分のステータス取得。認証ユーザーのアカウントステータスを取得します。 | （なし） | `{"status": "active"}` |
| `PUT` | `` | JWT | プロフィール更新。認証ユーザーのユーザー名と自己紹介を更新し、`status` を `active` にします。 | `{"username": "New Name", "bio": "New Bio"}` | `{"message": "Profile updated successfully"}` |
| `PUT` | `/icon` | JWT | アイコン更新。認証ユーザーのプロフィールアイコンをSFSPでスキャン後に更新します（202 で受け付け）。画像（JPEG・PNG・GIF・WebP）以外は 415、`MAX_IMAGE_UPLOAD_BYTES`（既定 10 MiB）超過は 413。 | `multipart/form-data`（key: `icon`） | `{"message": "Icon upload accepted for scanning", "job_id": "...", "status": "scanning"}` |
| `PUT` | `/background` | JWT | 背景画像更新。認証ユーザーの背景画像をSFSPでスキャン後に更新します（202 で受け付け）。画像以外は 415、サイズ超過は 413。 | `multipart/form-data`（key: `background`） | `{"message": "Background upload accepted for scanning", "job_id": "...", "status": "scanning"}` |
| `POST` | `/internal/create` | 内部 | 旧パス（移行用）。下記の内部用プロフィール作成と同じ処理ですが、Nginx が `/api/profile/internal/` を 404 にするため外部からは届きません。 | `{"user_id": "...", "username": "..."}` | `{"message": "Profile initialized successfully"}` |

### 内部専用エンドポイント（profile-service）

`/api/profile` の外にある、サービス間専用のパスです。Nginx は転送しません。`X-Internal-Key` が `INTERNAL_API_KEY` と一致しない場合、または `INTERNAL_API_KEY` が未設定の場合は常に 403 です。

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/internal/create` | 内部 | 内部用プロフィール作成。`auth-service` からのリクエストで、新規ユーザーの初期プロフィールレコードを作成します。201。 | `{"user_id": "...", "username": "..."}` | `{"message": "Profile initialized successfully"}` |
| `DELETE` | `/internal/users/:userId` | 内部 | プロフィールと画像の削除。アカウント削除時に `auth-service` が呼び出します。冪等で、成功時は 204。 | （なし） | （204 No Content） |

---

## Video Upload API & Stream Service

- **ベースパス:** `/api/videos`
- **担当サービス:** `video-upload-api`（アップロード・削除・サムネイル削除）、`video-worker`（一覧・詳細・メタデータ更新）。HLSのストリーミング配信はNginxがMinIOから直接行います。
- **責務:** 動画のアップロード受付、メタデータ管理、ストリーミング配信

| メソッド | エンドポイント | サービス | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|---|
| `POST` | `/upload` | `video-upload-api` | JWT | 動画アップロード。動画ファイルとメタデータを受け取り、スキャンと変換プロセスを開始します（202）。動画以外は 415、`MAX_VIDEO_UPLOAD_BYTES`（既定 1 GiB）超過は 413。`team_id` を指定するとチームへの投稿になり、投稿権限が無ければ 403。 | `multipart/form-data`（keys: `video`、`title`、`description`、任意で `team_id`） | `{"message": "Video upload accepted", "videoID": "...", "status": "..."}` |
| `DELETE` | `/delete/:id` | `video-upload-api` | JWT | 動画削除。指定した動画と関連ファイル（HLS、サムネイル）を削除します。投稿者または管理者のみ（他は 403）。Nginx が URL を `/api/videos/:id` に書き換えて `video-upload-api` の `DELETE` に転送します。 | （なし） | `{"message": "Video deleted successfully"}` |
| `DELETE` | `/:id/thumbnail` | `video-upload-api` | JWT | 動画サムネイル削除。サムネイルをMinIOから削除し、`thumbnail_path` を空にします。投稿者または管理者のみ。Nginx の専用 location で `video-upload-api` に振り分けます。 | （なし） | `{"message": "Thumbnail deleted successfully"}` |
| `GET` | `` | `video-worker` | 不要 | 動画リスト取得。公開済み（`status='public'`）の動画リストを検索クエリ付きで取得します。`team` を指定しない場合は全体公開（`team_id` なし）の動画のみで、`team=<token>` を指定するとそのチーム限定の動画を返します（チームの閲覧権限 `can_view` が必要。無ければ 403）。 | （クエリ: `q=search_term`、`team=<token>`） | `[{"id": "...", "title": "...", ...}]` |
| `GET` | `/:id` | `video-worker` | 任意 | 動画詳細取得。指定した動画のメタデータを取得します。投稿者本人と管理者は状態にかかわらず閲覧できます。それ以外は `public` かつ（`team_id` があれば）`can_view` のときだけで、見せられない場合は存在を明かさないよう 404 を返します。 | （なし） | `{"id": "...", "title": "...", ...}` |
| `PUT` | `/:id` | `video-worker` | JWT | 動画メタデータ更新。指定した動画のタイトルと説明を更新します。投稿者または管理者のみ（他は 403）。`thumbnail` を付けた場合はSFSPでスキャンし、202 で受け付けます。画像以外は 415、サイズ超過は 413。 | `multipart/form-data`（keys: `title`、`description`、任意で `thumbnail`） | `{"message": "Video updated successfully"}`（サムネイル付きの場合は 202 `{"message": "Thumbnail upload accepted for scanning", "job_id": "...", "status": "scanning"}`） |
| `GET` | `/:id/stream/<path>` | Nginx（MinIO直配信） | 不要 | 動画ストリーミング。HLSのマスタープレイリスト（`playlist.m3u8`）やセグメントを取得します。NginxがリクエストをMinIOの `videos` バケットのオブジェクトキー `<id>/<path>` へ変換して直接配信します（`:id` は10文字の英小文字・数字）。 | （なし） | （HLSマニフェスト） |

---

## Game Upload API

- **ベースパス:** `/api/games`
- **担当サービス:** `game-upload-api`
- **責務:** ゲームコンテンツのアップロードと管理

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/upload` | JWT | ゲームアップロード。ゲームのzipファイルとメタデータを受け取り、スキャンと展開プロセスを開始します（202）。`title` は必須（無ければ 400）。Unity WebGL ビルドのzipでなければ 415、`MAX_GAME_UPLOAD_BYTES`（既定 1 GiB）超過は 413。サムネイルは画像のみ（415）で `MAX_IMAGE_UPLOAD_BYTES`（既定 10 MiB）まで。`team_id` を指定するとチームへの投稿になり、投稿権限が無ければ 403。 | `multipart/form-data`（keys: `game`、`thumbnail`、`title`、`description`、任意で `team_id`） | `{"message": "Game upload accepted", "gameId": "...", "status": "..."}` |
| `GET` | `` | 不要 | ゲームリスト取得。公開済み（`status='public'`）のゲームリストを検索クエリ付きで取得します。`team` を指定しない場合は全体公開（`team_id` なし）のゲームのみで、`team=<token>` を指定するとそのチーム限定のゲームを返します（`can_view` が必要。無ければ 403、team-service に問い合わせられなければ 502）。 | （クエリ: `q=search_term`、`team=<token>`） | `[{"id": "...", "title": "...", ...}]` |
| `GET` | `/:id` | 任意 | ゲーム詳細取得。指定したゲームのメタデータを取得します。投稿者本人と管理者は状態にかかわらず閲覧できます。それ以外は `public` のみ（それ以外は 404）で、`team_id` があるゲームは `can_view` が必要です（無ければ 403、team-service に問い合わせられなければ 502）。 | （なし） | `{"id": "...", "title": "...", ...}` |
| `PUT` | `/:id` | JWT | ゲームメタデータ更新。指定したゲームのタイトル、説明、サムネイルを更新します。投稿者本人のみ（他は 403）。`thumbnail` を付けた場合はSFSPでスキャンし、202 で受け付けます。 | `multipart/form-data`（keys: `title`、`description`、`thumbnail`） | `{"message": "Game details updated successfully"}`（サムネイル付きの場合は 202 `{"message": "Thumbnail upload accepted for scanning", "job_id": "...", "status": "scanning"}`） |
| `PUT` | `/adjust/:id` | JWT | ゲーム表示調整。ゲームの表示スケールとオフセットを更新します。投稿者本人のみ。 | `{"scale": 1.0, "offset_x": 0, "offset_y": 0}` | `{"message": "Adjustments saved successfully"}` |
| `DELETE` | `/:id` | JWT | ゲーム削除。指定したゲームと関連するMinIO上のファイルをすべて削除します。投稿者または管理者のみ。 | （なし） | `{"message": "Game deleted successfully"}` |
| `DELETE` | `/:id/thumbnail` | JWT | ゲームサムネイル削除。投稿者本人のみ。 | （なし） | `{"message": "Thumbnail deleted successfully"}` |

---

## Static Site Upload API

- **ベースパス:** `/api/static-sites`
- **担当サービス:** `static-site-upload-api`
- **責務:** 静的サイトコンテンツのアップロードと管理

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/upload` | JWT | static-siteアップロード。サイトのzipファイルとメタデータを受け取り、スキャンと展開プロセスを開始します（202）。`title` は必須（無ければ 400）。zip以外は 415、`MAX_SITE_UPLOAD_BYTES`（既定 500 MiB）超過は 413。サムネイルは画像のみ（415）で `MAX_IMAGE_UPLOAD_BYTES`（既定 10 MiB）まで。`team_id` を指定するとチームへの投稿になり、投稿権限が無ければ 403。 | `multipart/form-data`（keys: `file`、`thumbnail`、`title`、`description`、任意で `team_id`） | `{"message": "Static site upload initiated for scanning", "siteId": "..."}` |
| `GET` | `` | 不要 | static-siteリスト取得。公開済み（`status='public'`）のサイトリストを検索クエリ付きで取得します。`team` を指定しない場合は全体公開（`team_id` なし）のサイトのみで、`team=<token>` を指定するとそのチーム限定のサイトを返します（`can_view` が必要。無ければ 403）。 | （クエリ: `q=search_term`、`team=<token>`） | `[{"id": "...", "title": "...", ...}]` |
| `GET` | `/:id` | 任意 | static-site詳細取得。指定したサイトのメタデータを取得します。投稿者本人と管理者は状態にかかわらず閲覧できます。それ以外は `public` かつ（`team_id` があれば）`can_view` のときだけで、見せられない場合は 404 を返します。 | （なし） | `{"id": "...", "title": "...", ...}` |
| `PUT` | `/:id` | JWT | static-siteメタデータ更新。指定したサイトのタイトルと説明を更新します。投稿者または管理者のみ（他は 403）。`thumbnail` を付けた場合はSFSPでスキャンし、202 で受け付けます。 | `multipart/form-data`（keys: `title`、`description`、任意で `thumbnail`） | `{"message": "Static site updated successfully"}`（サムネイル付きの場合は 202 `{"message": "Thumbnail upload accepted for scanning", "job_id": "...", "status": "scanning"}`） |
| `DELETE` | `/:id` | JWT | static-site削除。指定したサイトと関連するMinIO上のファイルをすべて削除します。投稿者または管理者のみ。 | （なし） | `{"message": "Static site deleted successfully"}` |
| `DELETE` | `/:id/thumbnail` | JWT | static-siteサムネイル削除。投稿者または管理者のみ。 | （なし） | `{"message": "Thumbnail deleted successfully"}` |

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

## Team Service

- **ベースパス:** `/api/teams`
- **担当サービス:** `team-service`
- **責務:** チームの作成・メンバー管理と、トークンベースのクローズドコンテンツ共有（Discord招待リンク風）。チーム詳細はトークン(URL)を知っていれば認証不要で閲覧できます。投稿の閲覧は、公開チームなら認証不要、非公開チームはメンバーのみです。
- **ロール:** メンバーは`owner`(3)・`admin`(2)・`member`(1)の3段階。各エンドポイントは役割ランクでアクセス制御する。ロールを付与できるのは自分より弱いロールのみ（例外: `owner` は `owner` を付与でき、オーナー権限の移譲に使う）。自分自身のロールは変更できず、自分と同じか強いロールのメンバーの変更・削除もできない。

| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `GET` | `` | 不要 | 公開チーム一覧取得。`is_public=true` のチームのみを返します。 |
| `GET` | `/mine` | JWT | 自分が参加しているチーム一覧を取得します。 |
| `POST` | `` | JWT | チーム作成。24文字のbase36トークンを自動生成し、作成者を`owner`として登録して201で返します。`name`（1〜255文字、必須）・`description`・`is_public`・`auto_approve`・`allow_member_invite`を指定可能。`auto_approve`は公開チームでのみ有効。 |
| `GET` | `/:token` | 不要 | トークンでチーム詳細取得。URLを持つ誰でも閲覧可能。 |
| `GET` | `/:token/permission` | 任意 | 権限確認。呼び出し元がそのチームで閲覧・投稿可能かを`{is_public, can_view, can_post, role}`で返す。JWT を先に評価し、`can_view`は公開チームまたはメンバー、`can_post`は`member`以上のとき`true`（非メンバーの`role`は空文字）。各アップロードサービスがチーム投稿前に、動画・ゲーム・static-siteの詳細・一覧が閲覧前に呼び出す。 |
| `GET` | `/:token/members` | JWT (member以上) | メンバー一覧を取得します。 |
| `POST` | `/:token/members` | JWT (admin以上) | メンバー追加／ロール変更。`user_id`（UUID）と`role`（`owner`・`admin`・`member`。省略時`member`）を指定。付与できるのは自分より弱いロールのみ（`owner`は`owner`を付与可）。新規は201、既存メンバーのロール変更は200。自分自身は不可（400）。 |
| `DELETE` | `/:token/members/:userId` | JWT (自分の退出は可／他人の削除はadmin以上) | メンバー削除・退出。他人を削除できるのは、自分より弱いロールのメンバーだけ。最後の`owner`が退出しようとすると409（`code: last_owner`）。 |
| `PATCH` | `/:token` | JWT (owner) | チーム情報更新。`name`・`description`・`is_public`・`auto_approve`・`allow_member_invite`のうち、送ったフィールドだけを更新する（無ければ400）。 |
| `DELETE` | `/:token` | JWT (owner) | チーム削除（メンバー・関連情報も削除）。 |

##### チームへの加入

| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `POST` | `/:token/join` | JWT | 加入リクエスト。公開チームで`auto_approve`なら即メンバー化（`{"status": "approved"}`）、手動承認なら`pending`の申請を作成（`{"status": "pending", "request_id": ...}`）。却下・取消済みの申請は再申請で`pending`に戻る。既にメンバーなら`already_member`、保留中の申請があれば409。非公開チームは加入自体を拒否（403）。 |
| `GET` | `/:token/join-requests` | JWT (admin以上) | 加入リクエスト一覧。 |
| `PATCH` | `/:token/join-requests/:requestId` | JWT (admin以上) | 加入リクエスト承認・却下。クエリ`action=approve\|reject`。承認すると`member`として追加される。`{"status", "user_id"}`を返し、保留中の申請が無ければ404。 |
| `DELETE` | `/:token/join-requests` | JWT | 加入リクエスト取消。自分の`pending`を取消。 |

##### チームコンテンツ（投稿）

| メソッド | エンドポイント | 認証 | 説明 |
|---|---|---|---|
| `POST` | `/:token/content` | JWT (member以上) | 投稿作成。`title`（必須、255文字以内）・`body`を指定。201で返す。 |
| `GET` | `/:token/content` | チームが公開なら不要／非公開ならmember以上 | そのチームの投稿一覧を取得します。非公開チームで未認証なら401、メンバーでなければ403。 |
| `GET` | `/:token/content/:contentID` | 一覧と同じ | 単一投稿取得。 |

投稿は `team_posts` テーブルに格納され、`author_name` は profile-service から取得した表示名が付与されます。非公開チームのコンテンツは、メンバーがログインした場合のみ閲覧可能です。

> **補足:** `:token` は24文字のbase36トークン（crypto/rand由来、約124ビットのエントロピー）で、予測不可能性によりアクセスを制限します。チームコンテンツ（動画・ゲーム・static-site）の連携は既に実装済みで、アップロード時のフォームフィールド`team_id`（チームのトークン）と、一覧の`?team=<token>`で扱い、各コンテンツテーブルの`team_id`カラムで所属チームを特定します。
>
> **内部専用:** `DELETE /internal/users/:userId`（`/api/teams`の外。Nginxは転送しない）は、アカウント削除時に`auth-service`が`X-Internal-Key`付きで呼び出し、そのユーザーのメンバー情報・加入申請・投稿を削除します（唯一の`owner`だったチームは他のメンバーへ`owner`を移譲し、メンバーがいなければチームごと削除）。冪等で、成功時は204。

---

## Security File Scan Platform (SFSP)

- **ベースパス:** `/api/v1`
- **担当サービス:** `sfsp-api`
- **責務:** ファイルのセキュリティスキャン受付と結果提供（内部専用。Nginx からは到達できません）
- **認証:** `/api/v1/*` はヘッダー `X-SFSP-API-Key`（`SFSP_API_KEY` と一致する値）が必須で、違えば 401 です。`SFSP_API_KEY` が空だと sfsp-api は起動しません。`/health` は認証不要で、`/api/v1` の外にあります。

| メソッド | エンドポイント | 認証 | 説明 | リクエストボディ | レスポンス例 |
|---|---|---|---|---|---|
| `POST` | `/files` | APIキー | ファイルスキャン依頼。各アップロードサービスからファイルを受け付け、スキャンジョブを作成・キューイングします（202）。`target_service` は `static-site`・`game`・`stream`・`profile`・`thumbnail`・`video-thumbnail`・`game-thumbnail`・`site-thumbnail` のいずれかで、それ以外は 400。1ファイルの上限は `SFSP_MAX_UPLOAD_BYTES`（既定 2 GiB-1）で、超過は 413。 | `multipart/form-data`（keys: `file`、`target_service`） | `{"file_id": "...", "job_id": "...", "status": "queued"}` |
| `GET` | `/jobs/:id` | APIキー | ジョブステータス確認。指定したジョブIDの現在の状態（`queued`、`running`、`completed` 等）を返します（ID 形式不正は 400、存在しなければ 404）。 | （なし） | `{"id": "...", "status": "running", ...}` |
| `GET` | `/results/:id` | APIキー | スキャン結果取得。完了したジョブIDのスキャン結果（ClamAV、YARA 等）の詳細を返します。結果がまだ無い場合は 200 で `{"message": ..., "job_status": ...}` を返します。 | （なし） | `[{"scanner": "clamav", "result": "clean", ...}]` |
| `GET` | `/health` | 不要 | ヘルスチェック。サービスの稼働状況を確認します（パスは `/api/v1` の外の `/health`）。 | （なし） | `{"status": "ok"}` |

---

## Monitoring Service

- **ベースパス:** `/api`（WebSocket のみ `/ws`）
- **担当サービス:** `monitoring-service`
- **アクセス:** `http://127.0.0.1:8090`（ホストのループバックのみに公開。HTTP Basic 認証が必要で、`MONITOR_USERNAME` / `MONITOR_PASSWORD` が未設定の場合は 503）
- **責務:** 開発者向けにシステム全体の状態を提供

| メソッド | エンドポイント | 説明 |
|---|---|---|
| `GET` | `/containers` | 稼働中の全コンテナのリスト（ID、名前、状態 等）を取得します。 |
| `GET` | `/containers/stats` | 全コンテナのCPU・メモリ使用率などの統計情報を取得します。 |
| `POST` | `/containers/restart/{name}` | 指定した名前のコンテナを再起動します。 |
| `GET` | `/topology` | 構成図用のデータ（コンテナの接続関係）を取得します。 |
| `GET` | `/volumes` | Docker ボリュームの一覧を取得します。 |
| `GET` | `/connections/count` | `atmosidea-frontend`のログを解析し、直近5分間のユニークIPアドレス数をカウントしてアクティブユーザー数を推定します。 |
| `GET` | `/minio/list/{containerName}` | 指定したMinIOコンテナ内のバケットやオブジェクトを一覧表示します。（クエリ: `bucket`、`prefix`） |
| `POST` | `/minio/upload/{containerName}` | 指定したMinIOコンテナのバケットにファイルをアップロードします。（クエリ: `bucket`、`prefix`） |
| `DELETE` | `/minio/delete/{containerName}` | 指定したMinIOコンテナのバケットからオブジェクトを削除します。（クエリ: `bucket`、`key`） |
| `GET` | `/ws/logs`、`/ws/logs/{id}` | `/api` の外のパス。WebSocket接続を確立し、指定したコンテナのログをリアルタイムにストリーミングします。（クエリ: `container`。`/ws/logs/{id}` ではコンテナIDをパスで指定） |

---

## 補足

- 各エンドポイントの詳細（内部処理ロジックや関連ファイル）は、プロジェクトの `README.md` の「API 一覧」セクションに記載されています。
- 動画ストリーミングはHLS形式を使用します。フロントエンドは `/api/videos/:id/stream/...` 経由でNginxを通じ、MinIO（video-storage）のHLSファイルにアクセスします。NginxがURLをMinIOのオブジェクトキーへ変換して直接配信します。