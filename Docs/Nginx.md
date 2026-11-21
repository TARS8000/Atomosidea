# Nginx 設定ドキュメント

本ドキュメントは、Atomosideaの`frontend`コンテナで動作するNginxの設定と設計思想を解説します。Nginxはシステム全体の入り口となり、静的ファイル配信とリバースプロキシ（APIゲートウェイ）の両方の役割を担います。

## 目次

- [概要](#概要)
- [アーキテクチャと構成](#アーキテクチャと構成)
- [upstream 定義](#upstream-定義)
- [Server Block の構成](#server-block-の構成)
- [API ルーティング](#api-ルーティング)
- [HLS 動画配信](#hls-動画配信)
- [ストレージへのプロキシ](#ストレージへのプロキシ)
- [サブドメインによるゲーム・static-site 配信](#サブドメインによるゲーム・static-site-配信)
- [セキュリティとパフォーマンスの設定](#セキュリティとパフォーマンスの設定)
- [設定規約と設計思想](#設定規約と設計思想)
- [トラブルシューティング](#トラブルシューティング)

---

## 概要

Nginxは`frontend`コンテナ内に組み込まれ、すべてのHTTPリクエストを受け付ける唯一のエントリポイントです。主な役割は次の2つです。

- **静的コンテンツの配信:** `/` などのAPI以外のパスには、ビルド済みのReactアプリ（`index.html`）を返します。
- **動的コンテンツの振り分け:** `/api/...` へのリクエストを、パスに応じて適切なバックエンドサービスにプロキシします。

また、MinIOストレージに保存された動画（HLS）、サムネイル、プロフィール画像も直接配信します。ゲームと静的サイトの本体は、アプリとは別オリジンのサブドメイン用 Server Block から配信します（メインオリジンでは配信しません）。

---

## アーキテクチャと構成

NginxはDocker Composeにより`frontend`サービスとして定義され、`http://localhost:3001`からアクセスできます。

| 項目 | 内容 |
|---|---|
| コンテナ名 | `atmosidea-frontend` |
| 公開ポート | `3001:80`（ホスト3001 → コンテナ80） |
| ベースイメージ | `nginx:stable-alpine` |
| 設定ファイル | `./frontend/nginx.conf` を `/etc/nginx/nginx.conf` に読み込み（読み取り専用） |
| 依存サービス | `auth-service`・`video-upload-api`・`video-worker`・`game-upload-api`・`mypage-service`・`profile-service`・`static-site-upload-api`（`depends_on` で起動順序を制御。`team-service` と `minio` は含まれません） |

### ビルドフロー（`frontend/Dockerfile`）

1. **ビルドステージ:** `node:20-alpine` で `npm ci --legacy-peer-deps`（`package-lock.json` どおりに依存を入れる）→ `npm run build` を実行し、`dist` を生成。`VITE_STATIC_SITE_DOMAIN` などのビルド引数を取り込みます。
2. **プロダクションステージ:** `nginx:stable-alpine` にビルド成果物を `/usr/share/nginx/html` へ、`nginx.conf` を `/etc/nginx/nginx.conf` へコピーします。
3. コンテナ起動時は `nginx -g "daemon off;"` でフォアグラウンド起動します。

> **補足:** `Dockerfile` がイメージにコピーする先（`/etc/nginx/nginx.conf`）と、`docker-compose.yml` が `frontend/nginx.conf` を読み取り専用でマウントする先は同じパスです。compose で起動したときはマウントしたホストのファイルが使われるので、設定を変えたら再ビルドせずに `docker compose restart frontend` で反映できます（イメージ単体で起動した場合もビルド時の `nginx.conf` がそのまま使われます）。以前の `Dockerfile` は `/etc/nginx/conf.d/default.conf` にコピーしていました。
>
> ビルドコンテキストは `./frontend`（`frontend/.dockerignore` で `node_modules` などを除外）で、`.env` は使いません（ビルド引数は compose の `args` で渡します）。

---

## upstream 定義

Nginxは`upstream`で各サービスの内部ホスト名とポートを定義し、リクエストを振り分けます。各ホスト名はDocker Composeのサービス名と一致し、Docker内蔵DNSで解決されます。

### バックエンドサービス

| upstream 名 | 送信先 | 担当サービス |
|---|---|---|
| `auth_service` | `auth-service:8080` | 認証 |
| `profile_service` | `profile-service:8084` | プロフィール管理 |
| `mypage_service` | `mypage-service:8083` | マイページ |
| `team_service` | `team-service:8080` | チーム管理＋トークンベースのコンテンツ共有 |
| `game_upload_api` | `game-upload-api:8082` | ゲームアップロードAPI |
| `static_site_upload_api` | `static-site-upload-api:8085` | static-siteアップロードAPI |
| `video_upload_service` | `video-upload-api:8080` | 動画アップロード |
| `video_worker_service` | `video-worker:8081` | 動画配信・メタデータ |

### MinIO ストレージ

Atomosideaは**1つのMinIOインスタンス**（`minio:9000`）を持ち、Nginxの各storage upstreamはすべてこの単一MinIOを指します。バケットごとの分離はMinIO側（バケットとユーザーのポリシー）で対応します。

| upstream 名 | 送信先 | 保管内容 |
|---|---|---|
| `game_storage` | `minio:9000` | 展開済みのゲームファイルとゲームのサムネイル（`games`バケット） |
| `profile_storage` | `minio:9000` | アイコン・背景画像（`user-profiles`バケット） |
| `static_site_storage` | `minio:9000` | 静的サイトのファイル（`static-sites`バケット） |
| `video_storage` | `minio:9000` | 動画・サムネイル（`videos`バケット） |

すべてのMinIOストレージは内部ポート`9000`で統一されています。

> **補足:** サムネイルは専用バケットではなく、動画サムネイルが`videos`バケット内の`thumbnails/<id>`キー、static-siteサムネイルが`static-sites`バケット内の`thumbnails/<id>`キーに格納されます。独立した`thumbnails`バケットは存在しません（ゲームのサムネイルも`games`バケットの`thumbnails/`以下です）。

---

## Server Block の構成

`nginx.conf` は2つのServer Blockで構成されています。

### 1. サブドメイン用 Block（ゲーム・static-site配信）

- **listen:** `80`
- **server_name:** 正規表現 `~^(?<subdomain>[a-z0-9]{10})\.localhost$`
- **役割:** `{10文字の英小文字・数字}.localhost` というサブドメインで、ゲームまたはstatic-siteをMinIOから配信します。アプリ（`localhost`）とは別オリジンになります。
- `resolver` は指定していません（以前は `resolver 127.0.0.11 valid=5s` がありましたが、`proxy_pass` の宛先は upstream 名で、起動時に解決されるため不要として削除しました）。
- `server_name` の正規表現はバックスラッシュを含むため、引用符で囲む必要があります。

### 2. メインアプリケーション用 Block（localhost & default）

- **listen:** `80 default_server`
- **server_name:** `localhost 127.0.0.1`
- **役割:** メインのReactアプリ配信と、すべての`/api/...`ルーティング、MinIOからのサムネイル・プロフィール画像・HLSの配信を担当します。

---

## API ルーティング

メインBlockでは、パスに応じて以下のように振り分けます。

| location | 送信先 upstream | 特徴 |
|---|---|---|
| `/` | （静的ファイル） | Reactアプリを配信。`try_files` でSPAルートを処理。 |
| `^~ /api/profile/internal/`、`~ ^/api/.*/internal(/\|$)` | （`return 404`） | サービス間の内部APIを外部に出さない。 |
| `/api/auth` | `auth_service` | 認証 |
| `/api/profile` | `profile_service` | プロフィール |
| `/api/my` | `mypage_service` | マイページ |
| `/api/teams` | `team_service` | チーム管理＋トークンベースのコンテンツ共有（`/api/teams/:token/*` を含む） |
| `/api/games` | `game_upload_api` | ゲームメタデータ |
| `/api/games/upload` | `game_upload_api` | ゲームのアップロード。`client_max_body_size 1100m`、`proxy_request_buffering off`、`proxy_read_timeout` / `proxy_send_timeout` 300秒 |
| `/api/static-sites` | `static_site_upload_api` | static-siteメタデータ |
| `/api/static-sites/upload` | `static_site_upload_api` | static-siteのアップロード。`client_max_body_size 550m`、`proxy_request_buffering off`、`proxy_read_timeout` / `proxy_send_timeout` 300秒 |
| `/api/videos/upload` | `video_upload_service` | 動画のアップロード。`client_max_body_size 1100m`、`proxy_request_buffering off`、`proxy_read_timeout` / `proxy_connect_timeout` / `proxy_send_timeout` 300秒 |
| `~ ^/api/videos/delete/(?<video_id>[a-zA-Z0-9]+)$` | `video_upload_service` | 削除用。`/api/videos/$video_id` に rewrite して転送 |
| `~ ^/api/videos/[a-zA-Z0-9]+/thumbnail$` | `video_upload_service` | 動画サムネイル削除用。これが無いと下の `/api/videos` に転送され `video-worker` で処理できない |
| `~ '^/api/videos/([a-z0-9]{10})/stream/(.*)$'` | `video_storage` | HLS配信（`/videos/$1/$2` のオブジェクトキーへ rewrite） |
| `/api/videos` | `video_worker_service` | 動画リスト・詳細・メタデータ更新（上の正規表現に当たらなかった残り全部） |

`/api/profile/internal/` と、`/api/` 配下の任意の階層にある `/internal` は、バックエンドの内部API（`/internal/...`、`X-Internal-Key` で保護）を外部に晒さないよう、Nginxが 404 を返します。正規表現のlocationはプレフィックスのlocation（`/api/profile` など）より優先されるため、後者のプロキシが内部APIを受けてしまわないよう、`/api/profile/internal/` は `^~` を付けて正規表現より前に確定させています。

すべてのAPIプロキシでは、クライアント情報を正確に渡すために次の4つのヘッダーを送信します。

```nginx
proxy_set_header Host $http_host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

---

## HLS 動画配信

動画のストリーミングは、`video-worker` を通さず、Nginxが直接`video-storage`（MinIO）からファイルを配信します。

- **エンドポイント:** `/api/videos/:id/stream/:path`
- **処理:** URLをMinIOのパス `/videos/:id/:path`（バケット `videos`、オブジェクトキー `:id/:path`）へ rewrite し、`video_storage` へプロキシします。`:id` は10文字の英小文字・数字です。
- **キャッシュ:** `Cache-Control: public, max-age=3600` を付与し、マニフェスト（`.m3u8`）やセグメント（`.ts`）を1時間キャッシュします。
- **ヘッダー:** `Access-Control-Allow-Origin: *` に加え、ユーザーがアップロードしたオブジェクトをメインオリジンで配信するため、`Content-Security-Policy: sandbox`（`default-src 'none'` 等）と `X-Content-Type-Options: nosniff` を付与します。
- フロントエンドは`hls.js`を使用して再生します。

---

## ストレージへのプロキシ

動画・ゲーム・static-siteのサムネイルやプロフィール画像など、MinIOに保存された静的アセットもNginxが配信します。

| location | 送信先 | キャッシュ方針 |
|---|---|---|
| `/videos/thumbnails/*` | `video_storage` | キャッシュしない（毎回更新されるため） |
| `/static-sites/thumbnails/*` | `static_site_storage` | キャッシュしない（毎回更新されるため） |
| `/games/thumbnails/*` | `game_storage` | キャッシュしない（毎回更新されるため） |
| `/user-profiles/*` | `profile_storage` | デフォルト |
| `/games/:id/*`（`:id` は10文字の英小文字・数字） | （なし。`return 404`） | ゲーム本体はメインオリジンでは配信しない（サブドメインでのみ配信） |

サムネイルはアップロードのたびに差し替えられるため、`Cache-Control: no-cache, no-store, must-revalidate`（あわせて `Pragma: no-cache`、`Expires: 0`）を付与し、古い画像がキャッシュされないようにしています。

サムネイル・`/user-profiles/*` の location には、HLSと同じ `Content-Security-Policy: sandbox` と `X-Content-Type-Options: nosniff` も付与します。ユーザーがアップロードしたオブジェクトはメインオリジンで配信されるため、HTML/SVGとして解釈されてもスクリプトが動かないようにするためです。

ゲーム本体をメインオリジンで配信しないのは、ゲーム内のスクリプトが `localStorage` の JWT を読めてしまうためです。`thumbnails` も10文字なので、`/games/<10文字のid>/*` を 404 にする location よりも前に `/games/thumbnails/*` の location を書いています。

---

## サブドメインによるゲーム・static-site 配信

ゲームとstatic-siteは、10文字のサブドメインで個別配信されます。

- **ゲーム:** `{id}.localhost` にアクセスすると、リクエストが `/games/{id}/...` へ変換され、`game_storage` へプロキシされます。末尾が `/` のパスには `index.html` を補います（MinIOはディレクトリのインデックスを返さないため）。
- **static-site:** ゲームとして見つからず MinIO が 404 を返した場合は、`error_page 404 = @fallback_static_site` により `/static-sites/{id}/...` へ変換し、`static_site_storage` へプロキシします。

```nginx
rewrite ^(.*)/$ $1/index.html;                 # ディレクトリには index.html を補う（break を付けない）
rewrite ^/(.*)$ /games/$subdomain/$1 break;     # まず games バケット
error_page 404 = @fallback_static_site;        # 無ければ static-sites バケット
```

1行目に `break` を付けると2行目が実行されず、`/index.html` がバケット名としてMinIOに渡ってしまうため、付けません。

static-site配信（`@fallback_static_site`）では、Web APIの権限を制限する次のヘッダーを付与します。このほか `Access-Control-Allow-Origin: *`、`expires 3d`、`Cache-Control: public` も付きます。

```nginx
add_header Permissions-Policy "camera=(self), microphone=(self), geolocation=(), fullscreen=(self), payment=(), display-capture=(), clipboard-read=(), publickey-credentials-get=(), autoplay=(self)" always;
```

---

## セキュリティとパフォーマンスの設定

メインBlockでは、セキュリティとパフォーマンスの設定を行っています。

- **Content-Security-Policy (CSP):** スクリプト・スタイル・メディア・フレームなどの送信元を限定し、XSSなどの攻撃を緩和します。スクリプトは自オリジンのみ（`'self' 'unsafe-inline' 'unsafe-eval'`。以前あった外部CDN `https://cdn.jsdelivr.net` は削除）、`connect-src` は `'self'` と `http://localhost(:3001)`（以前あった `ws://localhost` は削除）で、ゲーム・static-siteのサブドメインをiframeで埋め込めるよう `frame-src` に `http://*.localhost` を許可しています。`worker-src` が無いため `default-src 'self'` が適用され、hls.js の blob: Worker は起動できません（hls.js はメインスレッドで処理するので再生は可能。`EditAI.md` 15.4）。
- **client_max_body_size:** メインBlockの既定は `12m`（画像10 MiB＋multipartのオーバーヘッド）です。大きなファイルを受け付けるアップロードAPIだけ、専用のlocationで引き上げています（`/api/videos/upload` と `/api/games/upload` は `1100m`、`/api/static-sites/upload` は `550m`。いずれも `proxy_request_buffering off`）。正確な上限はバックエンドが `MAX_*_UPLOAD_BYTES` で検査し、超過すると 413 を返します。
- **パフォーマンス:** `sendfile`、`tcp_nopush`、`tcp_nodelay`、`keepalive_timeout` などで静的ファイル配信を高速化しています。
- **タイムアウト:** 動画・ゲーム・static-siteのアップロードなど時間がかかる処理に対し、アップロード用のlocationで `proxy_read_timeout`・`proxy_send_timeout` を300秒に設定しています（`/api/videos/upload` のみ `proxy_connect_timeout` も300秒）。それ以外のlocationはNginxの既定値です。
- **内部APIの遮断:** `/api/profile/internal/` と `/api/*/internal` は 404 を返し、サービス間の内部APIを外部に出しません。
- **ユーザーがアップロードしたファイル:** サムネイル・プロフィール画像・HLSのlocationには、`Content-Security-Policy: sandbox` と `X-Content-Type-Options: nosniff` を付けます。locationの中で `add_header` を1つでも書くと、serverレベルの `add_header`（メインのCSP）は**継承されません**。ヘッダーを追加するときは、必要なものをすべてそのlocationに書いてください。

---

## 設定規約と設計思想

将来の変更でもシステムの安定性を保つため、以下の規約を維持します。

### 7.1. `proxy_pass` の末尾スラッシュは付けない（最重要規約）

`proxy_pass` のURL末尾にスラッシュを**付けません**。

```nginx
# 正しい例
location /api/auth {
    proxy_pass http://auth_service;
}

# 間違った例
location /api/auth/ {
    proxy_pass http://auth_service/;
}
```

- **理由:** 末尾のスラッシュを付けない場合、NginxはリクエストURIをそのままバックエンドへ渡します（例: `GET /api/auth/me` → `http://auth_service/api/auth/me`）。
- これにより、各バックエンド（Go/Gin）は自身の担当パスプレフィックス（`/api/auth` 等）を含めてルーティングを定義でき、コードの可読性が向上します。
- 末尾にスラッシュを付けると、`location`でマッチした部分が削られて `/me` だけが送られるため、バックエンドのルーティングが複雑化します。**現在の設計はこの規約に依存しているため、変更は禁止です。**
- 動画の削除（`/api/videos/delete/:id`）とHLS配信は、`rewrite ... break` で書き換えたURIがバックエンドへ渡ります。

### 7.2. ヘッダー転送の義務化

すべてのAPIの`proxy_pass`ブロックで、クライアントの元のIP・プロトコルをバックエンドへ正しく渡します（MinIOへのサムネイル・プロフィール画像のlocationでは`Host`と`X-Real-IP`のみ）。

```nginx
proxy_set_header Host $http_host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

### 7.3. `location` の優先順位

Nginxは次の順でlocationを選びます。本プロジェクトでもこの順序を前提にしています。

1. **完全一致（`=`）:** 一致すればそこで確定します。
2. **`^~` 付きのプレフィックス:** 最長一致したプレフィックスが `^~` なら、正規表現を評価せずに確定します（例: `^~ /api/profile/internal/`）。
3. **正規表現（`~`）:** 書いた**順**に評価し、最初に一致したものを使います。プレフィックスのlocation（`/api/auth` など）より優先されます（例: `~ ^/api/videos/delete/...`、`~ ^/api/.*/internal(/|$)`）。
4. **通常のプレフィックス（修飾子なし）:** 正規表現のどれにも一致しなかったときに、最長一致のものを使います（例: `/api/auth`、`/api/videos`）。

このため、たとえば `/games/thumbnails/*` の正規表現は、より広い `/games/<10文字のid>/*`（404 を返す）の正規表現より前に書く必要があります。

### 7.4. `if` 文を使わない（動画削除の振り分け）

Nginxでは**「if is evil」**として知られ、`if`の使用は予期せぬ挙動の原因となるため、`nginx.conf` では `if` を使いません。

同じURLでも`GET /api/videos/:id`（詳細）は`video-worker`、削除は`video-upload-api`と担当サービスが異なります。そのため、メソッドを判定する代わりに、削除専用のURL `/api/videos/delete/:id` を設け、正規表現のlocationで `/api/videos/:id` に書き換えて `video-upload-api` へ転送しています。

```nginx
location ~ ^/api/videos/delete/(?<video_id>[a-zA-Z0-9]+)$ {
    rewrite ^/api/videos/delete/(?<video_id>[a-zA-Z0-9]+)$ /api/videos/$video_id break;
    proxy_pass http://video_upload_service;
}
```

- `DELETE /api/videos/:id` を直接呼んでも `video-worker` に転送されるため、動画は削除できません。削除は必ず `/api/videos/delete/:id` を使います。
- サムネイル削除（`DELETE /api/videos/:id/thumbnail`）は、パスで判別できるため専用の正規表現のlocation（`~ ^/api/videos/[a-zA-Z0-9]+/thumbnail$`）で `video-upload-api` に振り分けます。`video-upload-api` に新しいエンドポイントを追加したときは、同様に専用のlocationを追加してください（追加しないと `video-worker` に転送されます）。

---

## トラブルシューティング

- **コンテンツが表示されない:** `frontend` のNginx設定、各`storage`のバケットポリシー、ファイルパスが正しいか確認してください。監視ダッシュボードのストレージブラウザ機能で、MinIO上にファイルが正しく配置されているか確認できます。
- **ファイルがアップロードできない:** アップロード処理のログを確認してください。SFSPサービスが利用できない、またはMinIOへの接続に失敗している可能性があります。
- **動画・ゲームが処理されない:** スキャンプロセスやFFmpegの実行エラーをログで確認してください。Redisへの接続も確認します。
- **設定を変更した場合:** `nginx.conf` だけを変えたなら、構文を確認（`nginx -t`）したうえで `docker compose restart frontend` で反映します。`update.bat` は `git pull`、全コンテナの停止、キャッシュを使わない再ビルド、起動までを行う完全な再構築です。
- **`/internal` を含むAPIが 404 になる:** 仕様です。サービス間の内部APIは Nginx から転送しません。
- **アップロードが 413 になる:** Nginxの `client_max_body_size`（既定 12m、アップロード用locationは 1100m / 550m）か、バックエンドの `MAX_*_UPLOAD_BYTES` の上限を超えています。

---

## 補足

- 本ドキュメントは`frontend/nginx.conf`、`frontend/Dockerfile`、`docker-compose.yml`の`frontend`サービス定義、および`README.md`・`EditAI.md`のアーキテクチャ記述を元に作成しています。
- Nginxはシステム全体の入り口であるため、設定変更時は上記の規約（特に`proxy_pass`の末尾スラッシュ）を必ず遵守してください。