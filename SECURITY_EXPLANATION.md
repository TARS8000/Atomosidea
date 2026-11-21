# Atomosidea セキュリティ設計説明書

## 1. 概要

このドキュメントは、Atomosideaプロジェクト全体のセキュリティアーキテクチャと、その設計思想について説明します。本プロジェクトは、ユーザーがコンテンツを安全にアップロード・共有できるプラットフォームを提供するため、多層的なセキュリティ対策を講じています。

主要なセキュリティ原則は以下の通りです。

*   **多層防御 (Defense in Depth):** 単一のセキュリティ対策に依存せず、認証、認可、入力検証、インフラ保護など、複数の防御層を設けます。
*   **最小権限 (Least Privilege):** 各コンポーネント（サービス、ユーザー）は、その機能に必要な最小限の権限のみを持つべきです。
*   **セキュア・バイ・デフォルト (Secure by Default):** 設定を何もしなければ、最も安全な状態がデフォルトとなるように設計します。
*   **信頼しない (Zero Trust):** サービス間通信であっても、常にリクエストを検証し、明示的に信頼されたリクエストのみを受け入れます。

---

## 2. 主要セキュリティワークフロー

システムの安全性を確保するための主要な処理の流れを以下に詳述します。

### 2.1. ワークフロー1: ユーザー認証とAPIアクセス

このワークフローは、ユーザーがどのように認証され、保護されたAPIリソースにアクセスするのかを示します。

1.  **[ユーザー → `frontend`]**
    *   ユーザーがブラウザでログインページを開き、メールアドレスとパスワードを入力して「ログイン」ボタンをクリックします。

2.  **[`frontend` → `auth-service`]**
    *   `frontend`は、入力された認証情報をペイロードに含め、`auth-service`の `/api/auth/login` エンドポイントにPOSTリクエストを送信します。

3.  **[`auth-service`]**
    *   リクエストを受け取ると、`auth-db`（PostgreSQL）に保存されているユーザー情報を検索します。
    *   提供されたパスワードと、データベースに保存されているパスワードハッシュを `bcrypt` を用いて比較・検証します。
    *   ユーザーの `status` が `active` または `pending` であることも確認します（`deleted_data` などのユーザーはログインできません）。

4.  **[`auth-service` → `frontend`]**
    *   認証に成功した場合、`auth-service`はユーザーID・トークンID（`jti`）・有効期限（7日）を含むペイロードを持つJWT（JSON Web Token）を生成します。
    *   このJWTに、`.env` ファイルで管理されている `JWT_SECRET` を使って署名し、レスポンスとして `frontend` に返します。

5.  **[`frontend`]**
    *   受け取ったJWTをブラウザの `localStorage`（キー `token`）に保存します。Cookieは使わないため、CSRFは原理的に起きません。一方、XSSが1つでも成立するとトークンを盗まれるため、ユーザーがアップロードしたHTML/JSをメインオリジンで実行させない設計にしています（3.2参照）。

6.  **[`frontend` → `mypage-service` (保護されたAPI)]**
    *   ユーザーがマイページにアクセスすると、`frontend`は `mypage-service` の `/api/my/videos` エンドポイントにGETリクエストを送信します。
    *   この際、HTTPリクエストヘッダーに `Authorization: Bearer <JWT>` を付与します。

7.  **[`mypage-service`]**
    *   リクエストを受け取ると、まずAPIミドルウェアが `Authorization` ヘッダーのJWTを検証します。
    *   `mypage-service`も同じ `JWT_SECRET` を保持しており、これを使ってトークンの署名を検証します（アルゴリズムはHS256に固定し、`jti` が無いトークンは拒否します）。
    *   署名の検証後、Redisのブロックリスト（`blocklist:<jti>`）を確認し、ログアウト済みのトークンを拒否します。Redisでエラーが起きた場合も拒否します（fail closed）。この確認は、JWTを検証するすべてのサービスが行います。
    *   署名が有効で、かつ有効期限内であれば、ペイロードからユーザーIDを抽出し、そのユーザーに関連するデータ（この場合は動画一覧）を `app-db` から取得して返します。
    *   署名が無効、トークンが期限切れ、または失効済み（ログアウト済み）の場合、`401 Unauthorized` エラーを即座に返します。

**補足: Googleログインの場合**

JWTをURLに載せません。`auth-service` は、Cookieに保存した `state` とURLの `state` を定数時間で照合したうえでGoogleのユーザー情報を確認し、60秒・1回限りの `oauth_code:<code>` をRedisに保存して `/login/success?code=...` にリダイレクトします。`frontend` はURLからコードを消し、`POST /api/auth/oauth/exchange` でJWTを受け取ります。

### 2.2. ワークフロー2: 安全なファイルアップロードとスキャン

これは本システムで最も重要なセキュリティワークフローです。ユーザーがアップロードしたファイルが、いかにして安全に処理され、公開されるかを示します。

1.  **[ユーザー → `frontend` → `game-upload-api`]**
    *   認証済みユーザーが、ゲームのZIPファイル（例: `my_game.zip`）をアップロードします。
    *   `frontend`は、JWTと共にこのファイルを `game-upload-api` の `/api/games/upload` にPOSTします。サイズには上限があり（`MAX_GAME_UPLOAD_BYTES`、既定1GiB）、超えると `413` を返します。

2.  **[`game-upload-api`]**
    *   JWTを検証し、リクエストが正当なユーザーからのものであることを確認します。
    *   チーム限定で投稿する場合は、`team-service` の `/api/teams/:token/permission` で投稿権限（`can_post`）を確認します。
    *   `app-db`の`games`テーブルに、`status='scanning'` としてコンテンツの仮レコードを**先に**作成します（`sfsp_job_id` は応答を受けてから更新し、送信に失敗したらレコードを削除します）。
    *   受け取ったファイルを、スキャン依頼のメタデータ（`target_service='game'`）と共に、`sfsp-api` の `/api/v1/files` エンドポイントにストリーミング転送します。このとき `X-SFSP-API-Key` ヘッダーを付けます。サムネイルは別に `target_service='game-thumbnail'` で送ります。

3.  **[`sfsp-api`]**
    *   まず `X-SFSP-API-Key` ヘッダーを `SFSP_API_KEY` と定数時間で比較します。一致しなければ本文を読まずに `401` を返します（キーが未設定の場合、`sfsp-api` は起動しません）。
    *   `target_service` が許可リスト（`game`, `static-site`, `stream`, `profile`, `video-thumbnail`, `game-thumbnail`, `site-thumbnail`）にあることを確認します（それ以外は `400`。旧い別名の `thumbnail` も今は `400`）。
    *   ファイルをメモリや一時ファイルに溜めず、MinIOの `raw-files` バケット（未スキャン用）へストリーミングで保存しながら、SHA256ハッシュを計算します。`SFSP_MAX_UPLOAD_BYTES`（既定2GiB-1）を超えると `413` を返します。
    *   `sfsp-db`の`files`テーブルにファイルメタデータ（SHA256、ファイル名等）を、`scan_jobs`テーブルに新しいジョブを `status='queued'` で記録します。SHA256は記録するだけで、同じハッシュのファイルがあっても重複排除はしません（毎回スキャンします）。
    *   Redisの `sfsp:scan_queue` リストに、このジョブのIDをプッシュします。
    *   `game-upload-api` に `202 Accepted` を返します。

4.  **[`sfsp-worker`]**
    *   Redisの `sfsp:scan_queue` を常時監視しており、新しいジョブIDを `BRPOPLPUSH`（信頼性キュー）で取得します。取り出したIDは `sfsp:scan_queue:processing` に退避され、処理が終わってから `Ack` で消えます。途中で落ちても、起動時に元のキューへ戻されます。
    *   `scan_jobs`テーブルのステータスを `running` に更新します。
    *   MinIOの`raw-files`バケットから対象ファイルをダウンロードします。
    *   対象がZIPの場合は、まず上限付きで展開します。Zip Slip・symlink・デバイスファイル・重複エントリを拒否し、エントリ数・展開サイズ・圧縮率・ディレクトリの深さの上限を超えるものは `suspicious` と判定します。展開したディレクトリ**全体**を以降のスキャン対象とします。
    *   **サンドボックス内でスキャンを実行します。**
        *   a. **ClamAVスキャン:** `sfsp-clamav-client` Dockerイメージを、ファイルが読み取り専用でマウントされたサンドボックス内で起動し、ウイルススキャンを実行します。
        *   b. **YARAスキャン:** `sfsp-yara-client` Dockerイメージを同様のサンドボックス内で起動し、定義済みルールセット（`backend/security/sfsp/yara-rules/`。ビルド時に `sfsp-yara-client` イメージの `/etc/yara-rules` へ焼き込まれ、`sfsp-worker` 自身はルールを持ちません）に基づいて不正なパターンを検出します。
    *   スキャン結果を `sfsp-db` の `scan_results` テーブルに記録します。

5.  **[`sfsp-worker` (後処理)]**
    *   すべてのスキャン結果を評価し、最終的なステータス（`clean`, `malicious`, `suspicious`, `failed`, `invalid`）を決定します。両方のスキャナーが `clean` のときだけ `clean` になります。
    *   **[ステータスが `clean` の場合]**
        *   ファイルを `clean-files` バケット（スキャン済み用）に配置します。
    *   **[ステータスが `malicious` または `suspicious` の場合]**
        *   ファイルを `quarantine` バケット（隔離用）に配置します。
    *   `raw-files` のオブジェクトは、状態の記録とイベント発行の後に削除します（途中で落ちても再処理できるようにするためです）。
    *   `scan_jobs`テーブルのステータスを最終結果で更新します。
    *   スキャン完了イベント（`ScanCompletionEvent`）をJSON形式で作成し、`target_service` ごとのRedisキュー（ゲームなら `sfsp:completed:game`）にプッシュします。このイベントには、ジョブID、ファイルID、最終ステータス、処理対象サービス（`target_service='game'`）、SHA256、元のファイル名などが含まれます。

6.  **[`game-worker`]**
    *   Redisの `sfsp:completed:game` キューを常時監視しており、`BRPOPLPUSH`（信頼性キュー）で完了イベントを取得します。
    *   イベントの `job_id` を `app-db` の `games.sfsp_job_id` と照合して対象の行を特定し、`final_status` が `clean` であることを確認します。`clean` 以外の場合は `quarantined` / `failed` / `invalid` を記録して公開しません。
    *   MinIOの `clean-files` バケットから安全なゲームZIPファイルをダウンロードします。
    *   ゲーム固有の処理（上限付きのZIP展開、`index.html`の修正など）を実行します。展開サイズ2GiB・エントリ数20000・1エントリ1GiBを超える場合や、`../`・絶対パス・symlinkを含む場合は `failed` になります。
    *   処理済みのゲームファイルを、公開用の `games` バケット（`games/<id>/`）にアップロードします。
    *   `app-db`の`games`テーブルのレコードを、ステータスを `public` に、`game_url`（`<id>.localhost` サブドメインのURL）と解像度に更新します。
    *   終端状態をDBに記録してから `Ack` します。DBに書けなかったときだけ `Ack` せず、再起動時に再処理されます。公開後は、`sfsp-worker` が `clean-files` に残った元ファイルを削除します。

7.  **[ユーザー → `frontend`]**
    *   ユーザーがマイページをリフレッシュすると、ゲームのステータスが「公開」に変わり、プレイ可能になります。
    *   ゲームは `<id>.localhost` のサブドメイン（アプリ本体と別オリジン）で配信されます。投稿者・管理者以外には `public` のものだけが見え、チーム限定の投稿はさらに `team-service` の `can_view` を満たす人だけが見られます。

### 2.3. ワークフロー3: アカウントの完全削除

ユーザーデータのプライバシーを保護するため、アカウント削除は関連する全データをシステムから削除する重要なワークフローです。

1.  **[ユーザー → `frontend` → `auth-service`]**
    *   認証済みユーザーが「アカウント削除」を実行すると、`frontend`は `auth-service` の `/api/auth/me` エンドポイントにDELETEリクエストを送信します（JWT認証付き）。

2.  **[`auth-service`]**
    *   JWTを検証し（ブロックリストの確認を含む）、ユーザーIDを特定します。
    *   **削除処理を開始します。** 途中で一部が失敗しても残りの処理は続け、失敗した項目はレスポンスの `cleanupErrors` で返します。
        *   a. `X-Internal-Key` ヘッダー付きの内部APIで、`profile-service` と `team-service` の `DELETE /internal/users/:userId` を呼びます。`profile-db` の行とアイコン・背景画像、`team-db` のメンバー情報・加入申請・本人のチーム投稿が削除されます。唯一のownerだったチームは残りのメンバーに移譲され、誰もいなければチームごと削除されます。内部APIは `/api` の外にあり、Nginxからは届きません。
        *   b. `auth-service` 自身が、そのユーザーの動画・ゲーム・静的サイトの行を `app-db` から削除し、MinIOの各バケット（`videos` / `games` / `static-sites` / `user-profiles`）から、そのユーザーに紐づくファイル（動画のHLSとサムネイル、ゲーム、サイト、プロフィール画像など）を物理的に削除します。
    *   最後に `auth-db` のユーザーレコードを処理します。ローカルアカウントは物理的に削除し、Googleアカウントは `status='deleted_data'` にして無効化します（このステータスのユーザーはログインできません）。使用中のトークンの `jti` はブロックリストに追加され、直ちに使えなくなります。

---

## 3. 個別コンポーネントのセキュリティ詳細

### 3.1. SFSPサンドボックス実行環境 (`sfsp-worker` -> `DockerSandbox`)

`sfsp-worker`は、スキャナを隔離されたDockerコンテナ内で実行することで、万が一マルウェアが実行されてもホストシステムや他のサービスに影響が及ばないようにしています。

*   **ネットワーク遮断 (`NetworkMode: none`)**: スキャナーのコンテナにはネットワークが無く、マルウェアが外部と通信したり、他のコンテナへ到達したりできません。これがサンドボックスの最も重要な設定です。
*   **権限の削減**: `CapDrop: ALL`（すべてのLinux capabilityを破棄）と `no-new-privileges` を指定し、コンテナ内での権限昇格を防ぎます。
*   **読み取り専用マウント (`:ro`)**: スキャン対象のファイルは、名前付きボリューム `sfsp_temp_scan` を `/scan_target` に読み取り専用でマウントして渡し、スキャナーが元のファイルを変更することを防ぎます。
*   **リソース制限**: コンテナにはメモリ（1.5GB）・CPU（1CPU）・プロセス数（PID 256）の制限と30分のタイムアウトが課され、リソースを過剰に消費する攻撃（ZIP爆弾などのDoS攻撃）やハングからシステム全体を保護します。これとは別に、`sfsp-worker` コンテナ自体にもメモリ2GBの制限があります。
*   **スキャナーを直接 Entrypoint として実行**: イメージ側の初期化スクリプト（`/init`）は `chown` などを行い、`CapDrop: ALL` の下では失敗するため通さず、スキャナーのコマンドを直接実行します。判定は終了コードで行います。
*   **Docker APIの経路**: `sfsp-worker` は `docker.sock` も `.env` も直接マウントしません。`DOCKER_HOST=tcp://sfsp-docker-proxy:2375` で `sfsp-docker-proxy`（`tecnativa/docker-socket-proxy`）を経由し、コンテナとイメージに関するAPIだけを許可しています。ただしプロキシはリクエスト本文（`Privileged` や `Binds` など）を検査しないため、`sfsp-worker` が乗っ取られた場合の攻撃面を減らすものであり、完全な隔離ではありません（`EditAI.md` 15.4）。

### 3.2. アプリケーションの脆弱性対策

*   **SQLインジェクション対策:** データベース操作には、GoのPostgreSQLドライバ（`pgx/v5`）のプレースホルダ（`$1`, `$2` …）を常に使用します。SQLクエリを文字列結合で組み立てることはありません。
*   **クロスサイトスクリプティング (XSS) 対策:** フロントエンドではReactを採用しており、デフォルトでJSX内の文字列がエスケープされるため、多くのXSS攻撃は自動的に防止されます。`dangerouslySetInnerHTML`のような危険なプロパティの使用は意図的に避けています。
*   **入力検証:** ユーザーからの入力値は、APIの各ハンドラで型、長さ、フォーマットを検証してから処理します。
*   **アップロードの上限:** 各アップロードAPIは `MaxBytesReader` でサイズを制限し、超過は `413` を返します（動画・ゲーム1GiB、静的サイト500MiB、画像10MiBが既定）。画像は先頭バイトで形式を確認し、画像以外は `415` を返します。
*   **アップロードされたコンテンツの配信:** ゲーム・静的サイトは `<id>.localhost` のサブドメインでのみ配信し、メインオリジンの `/games/<id>/` は `404` にします。サムネイル・プロフィール画像は、ワーカーが `http.DetectContentType` で `jpeg` / `png` / `gif` / `webp` と判定したものだけを、判定した形式の拡張子で保存し、配信時には `CSP: sandbox` と `X-Content-Type-Options: nosniff` を付けます。
*   **認可:** 編集・削除は投稿者本人または管理者のみ許可します（それ以外は `403`）。チームの操作はロールの強さ（`owner` > `admin` > `member`）で判定し、自分より強いロールの付与・削除はできません。
*   **サービス間の内部API:** `/api` の外に置き、`X-Internal-Key`（`INTERNAL_API_KEY`）で守ります。Nginxは `/api/profile/internal/` と `/api/*/internal` を `404` にして外部に出しません。

### 3.3. インフラと運用セキュリティ

*   **環境変数の管理:** データベースのパスワード、JWTの秘密鍵、外部APIキーなどの機密情報は、すべて`.env`ファイルで管理されます。このファイルは`.gitignore`に含まれており、Gitリポジトリにコミットされることはありません。DBロールのパスワードはSQLに書かず、`db-role-init` が `.env` の値で `ALTER ROLE` します。なお `.env.example` の値は公開済みなので、本番では必ず変更してください。`.env` はDockerイメージに入れません（Dockerfileはコピーせず、`.dockerignore` が `.env` / `.env.*` を除外します）。各コンテナには `.env` 全体ではなく、`docker-compose.yml` の `environment:` に書いた、そのサービスが使う変数だけが渡ります（`env_file` は使いません）。例えば `INTERNAL_API_KEY` は auth-service・profile-service・team-service だけ、`SFSP_API_KEY` は sfsp-api とそれを呼ぶサービスだけが持ちます。ただし app-db などはアプリ用ロールが無く、多くのサービスが `POSTGRES_USER` / `POSTGRES_PASSWORD`（スーパーユーザー）で接続している点は残っています（`EditAI.md` 15.4）。
*   **Dockerセキュリティ:**
    *   **最小イメージの利用:** 各サービスのDockerイメージは、`alpine`などの軽量なベースイメージを基に構築され、攻撃対象領域を削減しています。
    *   **ネットワーク分離:** 外向き通信ができるのは `atmosidea-network` だけです。それ以外の `backend-db-network`（アプリ用DB群）、`sfsp-isolated-net`（Redis・MinIO・`sfsp-api`・アップロードAPI/ワーカー）、`sfsp-db-network`（`sfsp-db` 専用）、`sfsp-docker-net`（`sfsp-docker-proxy` 専用）は `internal: true` で、外へ出られません。ホストに公開しているポートは `frontend` の `3001:80` だけで、データベース・Redis・MinIOのポートは公開されていません。
    *   **Redisの認証:** Redisは `requirepass`（`REDIS_PASSWORD`）で保護しており、`.env` に値が無いと `docker compose` がエラーにします。同じネットワークのコンテナが、パスワード無しでスキャン完了イベントを偽造することはできません。
    *   **サービス間の認証（Zero Trust）:** `sfsp-api` の `/api/v1/*` は `X-SFSP-API-Key`、内部API（`/internal/*`）は `X-Internal-Key` が無いとアクセスできません。
    *   **監視ダッシュボード:** 別のcomposeで起動し、`127.0.0.1:8090`（ループバック）にだけ公開します。全ルートでHTTP Basic認証を必須とし、Host・Originの確認も行います。コンテナの再起動APIは持たず、操作できるのはMinIOのオブジェクト（アップロード・削除）だけです。

---

## 4. 将来の開発者への注意点

このプロジェクトのセキュリティは、各コンポーネントの密な連携の上に成り立っています。変更を加える際は、以下の点を特に意識してください。

*   **サンドボックス設定の変更は慎重に:** `backend/security/sfsp/internal/sandbox/docker.go`内のネットワーク遮断（`NetworkMode: none`）、`CapDrop`、リソース制限は、SFSPの安全性の根幹です。`sfsp-docker-proxy` の許可範囲を広げることも同様です。安易な変更は深刻な脆弱性を生む可能性があります。
*   **サービス間契約の遵守:** Redisのキュー名やイベントのJSON構造、APIのレスポンス形式などは、サービス間の「契約」です。変更する場合は、関連するすべてのサービスで整合性を取る必要があります。イベント（`backend/shared/event`）とモデル（`backend/shared/model`）はSFSPも同じものを使いますが、`sfsp:scan_queue` の名前と信頼性キューの操作は `backend/shared/queue` と SFSP の `internal/queue` に二重にあるため、両方を直してください。完了イベントは `DequeueReliable` で取り出し、終端状態を記録してから `Ack` します（`BRPOP` は使いません）。
*   **機密情報の取り扱い:** 新しい秘密鍵やAPIキーを追加する場合は、必ず`.env`ファイルで管理し、コード内にハードコーディングしないでください。使うサービスの `environment:` にだけ追加し、Dockerfileで `.env` をコピーしたり `env_file` で全体を渡したりしないでください。ドキュメントにも実際の値を書かないでください。
*   **既知の未対応事項の確認:** 認可の無いMinIO直接配信（Nginxから配信するHLS・サムネイル・サブドメイン）や、`sfsp-docker-proxy` がリクエスト本文を検査しない点など、まだ残っている問題は `EditAI.md` の15.4にまとまっています。関連する箇所を変更するときは確認してください。

このドキュメントが、Atomosideaのセキュリティを維持し、さらに強化していく上での一助となることを願っています。