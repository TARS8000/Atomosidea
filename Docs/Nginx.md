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
- [サブドメインによるゲーム・.static-site 配信](#サブドメインによるゲーム・static-site-配信)
- [セキュリティとパフォーマンスの設定](#セキュリティとパフォーマンスの設定)
- [設定規約と設計思想](#設定規約と設計思想)
- [トラブルシューティング](#トラブルシューティング)

---

## 概要

Nginxは`frontend`コンテナ内に組み込まれ、すべてのHTTPリクエストを受け付ける唯一のエントポイントです。主な役割は次の2つです。

- **静的コンテンツの配信:** `/` などのAPI以外のパスには、ビルド済みのReactアプリ（`index.html`）を返します。
- **動的コンテンツの振り分け:** `/api/...` へのリクエストを、パスに応じて適切なバックエンドサービスにプロキシします。

また、MinIOストレージに保存された動画（HLS）、ゲーム、静的サイト、プロフィール画像なども直接配信します。

---

## アーキテクチャと構成

NginxはDocker Composeにより`frontend`サービスとして定義され、`http://localhost:3001`からアクセスできます。

| 項目 | 内容 |
|---|---|
| コンテナ名 | `atmosidea-frontend` |
| 公開ポート | `3001:80`（ホスト3001 → コンテナ80） |
| ベースイメージ | `nginx:stable-alpine` |
| 設定ファイル | `./frontend/nginx.conf` を `/etc/nginx/nginx.conf` に読み込み（読み取り専用） |
| 依存サービス | 全バックエンドサービス（`depends_on`で起動順序を制御） |

### ビルドフロー（`frontend/Dockerfile`）

1. **ビルドステージ:** `node:20-alpine` でReactアプリをビルドし、`dist` を生成。`VITE_STATIC_SITE_DOMAIN` などのビルド引数を取り込みます。
2. **プロダクションステージ:** `nginx:stable-alpine` にビルド成果物を `/usr/share/nginx/html` へコピーし、`nginx.conf` を設定として読み込みます。
3. コンテナ起動時は `nginx -g "daemon off;"` でフォアグラウンド起動します。

> **補足:** `Dockerfile` は設定を `/etc/nginx/conf.d/default.conf` へコピーしますが、実行時には`docker-compose.yml` によるボリュームマウント（`/etc/nginx/nginx.conf`）が優先されます。実際の稼働設定はマウントされた`nginx.conf`（`frontend/nginx.conf`）が担当します。

---

## upstream 定義

Nginxは`upstream`で各サービスの内部ホスト名とポートを定義し、リクエストを振り分けます。各サービス名はDocker Composeのネットワーク名と一致します。

### バックエンドサービス

| upstream 名 | 送信先 | 担当サービス |
|---|---|---|
| `auth_service` | `auth-service:8080` | 認証 |
| `profile_service` | `profile-service:8084` | プロフィール管理 |
| `mypage_service` | `mypage-service:8083` | マイページ |
| `game_upload_api` | `game-upload-api:8082` | ゲームアップロードAPI |
| `static_site_upload_api` | `static-site-upload-api:8085` | static-siteアップロードAPI |
| `video_upload_service` | `video-upload-api:8080` | 動画アップロード |
| `video_worker_service` | `video-worker:8081` | 動画配信・メタデータ |

### MinIO ストレージ

Atomosideaは**1つのMinIOインスタンス**（`minio:9000`）を持ち、Nginxの各storage upstreamはすべてこの単一MinIOを指します。バケットごとの分離はMinIO側（バケット単位）で対応します。

| upstream 名 | 送信先 | 保管内容 |
|---|---|---|
| `game_storage` | `minio:9000` | ゲームのZIP（`games`バケット） |
| `profile_storage` | `minio:9000` | アイコン・背景画像（`user-profiles`バケット） |
| `static_site_storage` | `minio:9000` | 静的サイトのファイル（`static-sites`バケット） |
| `video_storage` | `minio:9000` | 動画・サムネイル（`videos`バケット） |

すべてのMinIOストレージは内部ポート`9000`で統一されています。

> **補足:** サムネイルは専用バケットではなく、動画サムネイルが`videos`バケット内の`thumbnails/<id>`キー、static-siteサムネイルが`static-sites`バケット内の`thumbnails/<id>`キーに格納されます。独立した`thumbnails`バケットは存在しません。

---

## Server Block の構成

`nginx.conf` は2つのServer Blockで構成されています。

### 1. サブドメイン用 Block（ゲーム・static-site配信）

- **listen:** `80`
- **server_name:** 正規表現 `~^(?<subdomain>[a-z0-9]{10})\.localhost$`
- **役割:** `{10文字の英数字}.localhost` というサブドメインで、ゲームまたはstatic-siteをMinIOから配信します。
- Dockerの内蔵DNS（`resolver 127.0.0.11`）を使用し、サブドメインを動的に解決します。

### 2. メインアプリケーション用 Block（localhost & default）

- **listen:** `80 default_server`
- **server_name:** `localhost 127.0.0.1`
- **役割:** メインのReactアプリ配信と、すべての`/api/...`ルーティングを担当します。

---

## API ルーティング

メインBlockでは、パスに応じて以下のように振り分けます。

| location | 送信先 upstream | 特徴 |
|---|---|---|
| `/` | （静的ファイル） | Reactアプリを配信。`try_files` でSPAルートを処理。 |
| `/api/auth` | `auth_service` | 認証 |
| `/api/profile` | `profile_service` | プロフィール |
| `/api/my` | `mypage_service` | マイページ |
| `/api/games` | `game_upload_api` | ゲームメタデータ |
| `/api/static-sites` | `static_site_upload_api` | static-siteメタデータ |
| `/api/videos/upload` | `video_upload_service` | アップロード（300秒のタイムアウト） |
| `/api/videos/delete/:id` | `video_upload_service` | 正規表現で`/api/videos/:id`へ変換後、削除用 |
| `/api/videos/:id/stream/*` | `video_storage` | HLS配信（オブジェクトキーへ変換） |
| `/api/videos` | `video_worker_service` | 動画リスト・詳細・ストリーミングメタデータ |

すべてのAPIプロキシでは、クライアント情報の正確な渡しのために次の4つのヘッダーを送信します。

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
- **処理:** URLをMinIOのオブジェクトキー `videos/:id/:path` へ変換し、`video_storage` へプロキシします。
- **キャッシュ:** `Cache-Control: public, max-age=3600` を付与し、マニフェスト（`.m3u8`）やセグメント（`.ts`）を1時間キャッシュします。
- フロントエンドは`hls.js`を使用して再生します。

---

## ストレージへのプロキシ

動画・ゲームのサムネイルやゲームファイル、プロフィール画像など、MinIOに保存された静的アセットもNginxが配信します。

| location | 送信先 | キャッシュ方針 |
|---|---|---|
| `/videos/thumbnails/*` | `video_storage` | キャッシュしない（每次都更新のため） |
| `/static-sites/thumbnails/*` | `static_site_storage` | キャッシュしない（每次都更新のため） |
| `/games/thumbnails/*` | `game_storage` | キャッシュしない（每次都更新のため） |
| `/user-profiles/*` | `profile_storage` | デフォルト |
| `/games/:id/*` | `game_storage` | デフォルト |

サムネイルはアップロードのたびにオブジェクト名がランダムに変更されるため、`Cache-Control: no-cache, no-store, must-revalidate` を付与し、古い画像がキャッシュされないようにしています。

---

## サブドメインによるゲーム・static-site 配信

ゲームとstatic-siteは、10文字のサブドメインで個別配信されます。

- **ゲーム:** `{id}.localhost` にアクセスすると、リクエストが `/games/{id}/...` へ変換され、`game_storage` へプロキシされます。
- **static-site:** ゲームの`@fallback_static_site` で、一致しなかった場合 `/static-sites/{id}/...` へフォールバックし、`static_site_storage` へプロキシされます。

このとき、Web APIの権限を反映したヘッダーが付与されます。

```nginx
add_header Permissions-Policy "camera=(self), microphone=(self), geolocation=(), fullscreen=(self), payment=(), display-capture=(), clipboard-read=(), publickey-credentials-get=(), autoplay=(self)" always;
```

---

## セキュリティとパフォーマンスの設定

メインBlockでは、セキュリティとパフォーマンスの設定を行っています。

- **Content-Security-Policy (CSP):** スクリプト・スタイル・メディア・フレームなどの送信元を限定し、XSSなどの攻撃を緩和します。`cdn.jsdelivr.net` をスクリプトの送信元に加えています。
- **client_max_body_size 3G:** アップロードファイル（動画・ゲーム・static-siteのZIP）に対応するため、リクエストボディの上限を3GBに設定しています。
- **パフォーマンス:** `sendfile`、`tcp_nopush`、`tcp_nodelay`、`keepalive_timeout` などで静的ファイル配信を高速化しています。
- **タイムアウト:** 動画アップロードなど時間がかかる処理に対し、`proxy_read_timeout`・`proxy_connect_timeout`・`proxy_send_timeout` を300秒に設定しています。

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

### 7.2. ヘッダー転送の義務化

すべての`proxy_pass`ブロックで、クライアントの元のIP・プロトコルをバックエンドへ正しく渡します。

```nginx
proxy_set_header Host $http_host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

### 7.3. `location` の優先順位

Nginxは最も具体的にマッチする`location`を優先します。本プロジェクトでは以下の優先順位で使い分けます。

1. **正規表現（`~`）:** パス内に動的なIDが含まれる場合（例: `/api/videos/([0-9]+)`）。
2. **完全一致（`=`）:** 他のパスと競合しない特定のパス。
3. **プレフィックス（修飾子なし）:** 特定のプレフィックスで始まるすべてのリクエスト（例: `/api/auth`）。

### 7.4. `if` 文の例外的使用（技術的負債）

`nginx.conf` には、`DELETE` メソッドを判定するための`if`文が存在します。

```nginx
location ~ ^/api/videos/([0-9]+)$ {
    if ($request_method = DELETE) {
        proxy_pass http://video_upload_service;
        break;
    }
    proxy_pass http://video_worker_service;
}
```

- **背景:** Nginxでは**「if is evil」**として知られ、`if`の使用は予期せぬ挙動の原因となるため原則として避けるべきです。
- **現状の理由:** 同じURLでも`GET /api/videos/:id`（詳細）は`video-worker`、`DELETE /api/videos/:id`（削除）は`video-upload-api`と担当サービスが異なるため、暫定的に`if`でメソッドを判定しています。
- **将来の展望:** 技術的負債として認識しており、将来的には削除専用エンドポイント（例: `/api/videos/delete/:id`）を設け、`if`を使いないルーティングへリファクタリングすることが望ましいです。

---

## トラブルシューティング

- **コンテンツが表示されない:** `frontend` のNginx設定、各`storage`のバケットポリシー、ファイルパスが正しいか確認してください。監視ダッシュボードのストレージブラウザ機能で、MinIO上にファイルが正しく配置されているか確認できます。
- **ファイルがアップロードできない:** アップロード処理のログを確認してください。SFSPサービスが利用できない、またはMinIOへの接続に失敗している可能性があります。
- **動画・ゲームが処理されない:** スキャンプロセスやFFmpegの実行エラーをログで確認してください。Redisへの接続も確認します。
- **設定を変更した場合:** `update.bat` で全コンテナを停止したのち、キャッシュを使わない完全な再起動を行います。

---

## 補足

- 本ドキュメントは`frontend/nginx.conf`、`frontend/Dockerfile`、`docker-compose.yml`の`frontend`サービス定義、および`README.md`・`EditAI.md`のアーキテクチャ記述を元に作成しています。
- Nginxはシステム全体の入り口であるため、設定変更時は上記の規約（特に`proxy_pass`の末尾スラッシュ）を必ず遵守してください。