# Atmosidea プロジェクト 完全技術仕様書

このドキュメントは、`Atmosidea`プロジェクトの全体像、アーキテクチャ、設計思想、操作方法、そして将来の展望までを網羅的に解説するものです。このドキュメントを読むことで、プロジェクトの知識がない開発者（またはAI）でも、システムの全体像と「なぜ」そうなっているのかを完全に理解できることを目的とします。
絶対遵守（AIは必ずこれを読み守ること）：本文中の内容は絶対に消去しないでください。基本的に内容を追記していく場合でお願いします。大規模な消去を行う場合は必ずユーザーの許可を取ってください。

## 目次
1.  [プロジェクト概要](#1-プロジェクト概要)
2.  [技術スタック](#2-技術スタック)
3.  [システムアーキテクチャ](#3-システムアーキテクチャ)
    -   [3.1. 概念図](#31-概念図)
    -   [3.2. サービス（コンテナ）詳細](#32-サービスコンテナ詳細)
4.  [主要なワークフロー](#4-主要なワークフロー)
    -   [4.1. ユーザー認証フロー](#41-ユーザー認証フロー)
    -   [4.2. ゲームアップロードと処理フロー（最重要）](#42-ゲームアップロードと処理フロー最重要)
    -   [4.3. 静的サイトアップロードと処理フロー](#43-静的サイトアップロードと処理フロー)
5.  [表示ロジックの核心（設計思想）](#5-表示ロジックの核心設計思想)
    -   [5.1. ゲーム表示ロジック](#51-ゲーム表示ロジック)
    -   [5.2. 動画表示ロジック](#52-動画表示ロジック)
6.  [認証と認可の仕様（重要）](#6-認証と認可の仕様重要)
    -   [6.1. 削除・編集時の認証](#61-削除編集時の認証)
7.  [Nginx設定の規約と設計思想（重要）](#7-nginx設定の規約と設計思想重要)
    -   [7.1. APIゲートウェイとしての役割](#71-api-ゲートウェイとしての役割)
    -   [7.2. `proxy_pass`の規約: 末尾のスラッシュ](#72-proxy_passの規約-末尾のスラッシュ)
    -   [7.3. ヘッダー転送の重要性](#73-ヘッダー転送の重要性)
    -   [7.4. `location`ブロックの評価順序と使い分け](#74-locationブロックの評価順序と使い分け)
    -   [7.5. `if`文の例外的使用（技術的負債）](#75-if文の例外的使用技術的負債)
8.  [APIエンドポイント一覧](#8-apiエンドポイント一覧)
9.  [ディレクトリと主要ファイルの役割](#9-ディレクトリと主要ファイルの役割)
10. [データベーススキーマ](#10-データベーススキーマ)
    -   [10.1. `users` テーブル](#101-users-テーブル)
    -   [10.2. `games` テーブル](#102-games-テーブル)
    -   [10.3. `static_sites` テーブル](#103-static_sites-テーブル)
11. [セットアップと開発フロー](#11-セットアップと開発フロー)
    -   [11.1. 環境設定 (`.env`)](#111-環境設定-env)
    -   [11.2. 管理用スクリプト (`.bat`)](#112-管理用スクリプト-bat)
    -   [11.3. 日常の開発サイクル](#113-日常の開発サイクル)
12. [将来の課題と改善点](#12-将来の課題と改善点)
13. [最近の主な変更点 (2026/08/01)](#13-最近の主な変更点-20260801)
14. [最近の主な変更点 (2026/08/05)](#14-最近の主な変更点-20260805)
15. [最近の主な変更点 (2026/08/06)](#15-最近の主な変更点-20260806)
16. [セキュリティモデルの設計思想と注意点](#16-セキュリティモデルの設計思想と注意点)
17. [セキュリティモデルの設計思想と注意点 (2)](#17-セキュリティモデルの設計思想と注意点-2)
18. [最近の主な変更点 (2026/08/08)](#18-最近の主な変更点-20260808)
19. [編集内容の概要 (EDIT_SUMMARY.md)](#19-編集内容の概要-edit_summarymd)

---

## 1. プロジェクト概要

`Atmosidea`は、動画、Unity WebGLゲーム、そして静的Webサイトのアップロード、共有、閲覧を行うためのWebアプリケーションです。マイクロサービスアーキテクチャを採用し、スケーラビリティとメンテナンス性を高めています。

## 2. 技術スタック

-   **フロントエンド**: React, TypeScript, Material-UI, Vite
-   **バックエンド**: Go, Gin
-   **データベース**: PostgreSQL
-   **メッセージキュー**: Redis
-   **オブジェクトストレージ**: MinIO (`game-storage`, `profile-storage`, `static-site-storage`)
-   **コンテナ・オーケストレーション**: Docker, Docker Compose
-   **リバースプロキシ / APIゲートウェイ**: Nginx

## 3. システムアーキテクチャ

### 3.1. 概念図

```
[ユーザー] -> [ブラウザ] -> [Nginx (frontend:80)]
                                |
           /api/auth/**         +-> [auth-service:8080] ----> [auth-db:5432]
           /api/profile/**      +-> [profile-service:8084] -> [auth-db:5432] -> [MinIO (profile-storage:9000)] (for icons)
           /api/my/**           +-> [mypage-service:8083] --> [app-db:5432]
                                |
           /api/videos/**       +-> [upload-service:8080] --> [app-db:5432]
                                |   [stream-service:8081] --> [app-db:5432]
                                |
           /api/games/**        +-> [game-upload-api:8082] -> [app-db:5432] -> [Redis:6379]
           /games/**            +-> [MinIO (game-storage:9000)]
                                |
           /api/static-sites/** +-> [static-site-upload-api:8085] -> [app-db:5432] -> [Redis:6379]
           /static-sites/**     +-> [MinIO (static-site-storage:9000)]
                                |
           (非同期処理)         [game-worker] <- [Redis:6379] -> [MinIO (game-storage:9000)]
                                                              -> [app-db:5432]
           (非同期処理)         [static-site-worker] <- [Redis:6379] -> [MinIO (static-site-storage:9000)]
                                                                      -> [app-db:5432]
```

### 3.2. サービス（コンテナ）詳細

| サービス名              | 内部ポート | 技術          | 責務                                                                                                                                                             |
| ----------------------- | ---------- | ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `frontend`              | `80`       | Nginx, React  | **UI/APIゲートウェイ**: Reactアプリを提供し、`/api`以下のリクエストを各バックエンドサービスに振り分ける。                                                              |
| `auth-service`          | `8080`     | Go, Gin       | **認証**: ユーザー登録、Google OAuth、JWT発行を担当。**`auth-db`**に接続。                                                                                        |
| `profile-service`       | `8084`     | Go, Gin       | **プロフィール管理**: ユーザー名、自己紹介、アイコン、背景画像の取得・更新を担当。**`auth-db`**と**`profile-storage`**に接続。                                        |
| `mypage-service`        | `8083`     | Go, Gin       | **マイページ**: ログインユーザーの投稿コンテンツ一覧（動画・ゲーム・静的サイト）を取得。**`app-db`**に接続。                                                       |
| `upload-service`        | `8080`     | Go, Gin       | **動画アップロード**: 動画ファイルのアップロード、サムネイル生成、DBへのメタデータ保存を担当。**`app-db`**に接続。                                                     |
| `stream-service`        | `8081`     | Go, Gin       | **動画配信**: 動画のストリーミング配信とメタデータ提供を担当。**`app-db`**に接続。                                                                                 |
| `game-upload-api`       | `8082`     | Go, Gin       | **ゲームメタデータAPI**: ゲームのメタデータ管理と、`game-worker`への処理要求（Redis経由）を担当。**`app-db`**に接続。                                                 |
| `game-worker`           | -          | Go            | **ゲーム非同期処理**: RedisからJobを受け取り、ZIP解凍、解像度抽出、MinIOへのファイルアップロード、DB更新といった時間のかかる処理を実行。**`app-db`**に接続。          |
| `static-site-upload-api`| `8085`     | Go, Gin       | **静的サイトAPI**: 静的サイトのZIPアップロード受付、メタデータ管理、`static-site-worker`への処理要求（Redis経由）を担当。**`app-db`**に接続。                         |
| `static-site-worker`    | -          | Go            | **静的サイト非同期処理**: RedisからJobを受け取り、ZIP解凍、MinIOへのファイルアップロードを実行。**`app-db`**に接続。                                               |
| `auth-db`               | `5432`     | PostgreSQL    | **認証・ユーザーDB**: ユーザー情報、プロフィール、アカウント状態を永続化。                                                                                       |
| `app-db`                | `5432`     | PostgreSQL    | **アプリケーションDB**: 動画、ゲーム、静的サイトのメタデータを永続化。                                                                                           |
| `redis`                 | `6379`     | Redis         | **メッセージキュー**: `game-upload-api`/`game-worker`間、`static-site-upload-api`/`static-site-worker`間の非同期タスクの受け渡し。                               |
| `game-storage`          | `9000`     | MinIO         | **オブジェクトストレージ**: ゲームの全アセット（HTML, JS, WASM等）をホスト。                                                                                       |
| `profile-storage`       | `9000`     | MinIO         | **オブジェクトストレージ**: プロフィール画像（アイコン、背景）をホスト。                                                                                           |
| `static-site-storage`   | `9000`     | MinIO         | **オブジェクトストレージ**: 静的サイトのHTML, CSS, JSなどのファイルをホスト。                                                                                      |

## 4. 主要なワークフロー

### 4.1. ユーザー認証フロー
1.  ユーザーがログイン情報を入力すると、`frontend`は`/api/auth/login`にリクエストを送信。
2.  Nginxがリクエストを`auth-service`に転送。
3.  `auth-service`はDBのユーザー情報を検証し、成功すればJWT（ペイロードに`userID`, `username`, `isAdmin`を含む）を生成して返す。
4.  `frontend`は受け取ったJWTを`localStorage`に保存し、以降のAPIリクエストの`Authorization`ヘッダーに含める。

### 4.2. ゲームアップロードと処理フロー（最重要）
本システムで最も重要な非同期処理フローです。
1.  **アップロード**: ユーザーが`frontend`からゲームのZIPファイルをアップロード。
2.  **API受付**: `frontend`は`/api/games/upload`にリクエストを送信。Nginx経由で`game-upload-api`がこれを受け取る。
3.  **一次保存とJob発行**:
    -   `game-upload-api`は受け取ったZIPファイルを直接MinIOに一次保存。
    -   DBの`games`テーブルに`status: 'processing'`でレコードを作成し、`gameId`を取得。
    -   Redisキューに`{gameId, objectName}`を含むJobをPushする。
    -   `frontend`に`gameId`を返し、`AdjustGamePage`へリダイレクトさせる。
4.  **非同期処理**:
    -   `game-worker`がRedisキューからJobを取得。
    -   MinIOからZIPファイルをダウンロードし、解凍。
    -   `index.html`を**正規表現で解析**し、`canvas.style.width`や`width="..."`属性から**ネイティブ解像度（`native_width`, `native_height`）を抽出**。
    -   解凍した全ファイルを、`/{gameId}/`というプレフィックスでMinIOに再アップロード。
    -   DBの該当ゲームレコードを`status: 'public'`に更新し、`game_url`と抽出した解像度を保存。
5.  **ポーリングと表示**:
    -   `AdjustGamePage`は、`status`が`'public'`になるまで`/api/games/{id}`を数秒おきにポーリング（問い合わせ）。
    -   `status`が`'public'`になると、ポーリングを停止し、取得した`game_url`と`native_width`/`height`を使ってプレビューを表示する。

### 4.3. 静的サイトアップロードと処理フロー
1.  **アップロード**: ユーザーが`frontend`から静적サイトのZIPファイルをアップロード。
2.  **API受付**: `frontend`は`/api/static-sites/upload`にリクエストを送信。Nginx経由で`static-site-upload-api`がこれを受け取る。
3.  **一次保存とJob発行**:
    -   `static-site-upload-api`は受け取ったZIPファイルをMinIO (`static-site-storage`) に一次保存。
    -   DBの`static_sites`テーブルに`status: 'processing'`でレコードを作成し、`siteId`を取得。
    -   Redisキューに`{siteId, objectName}`を含むJobをPushする。
    -   `frontend`に`siteId`を返し、サイト詳細ページへリダイレクトさせる。
4.  **非同期処理**:
    -   `static-site-worker`がRedisキューからJobを取得。
    -   MinIOからZIPファイルをダウンロードし、解凍。
    -   解凍した全ファイルを、`/{siteId}/`というプレフィックスでMinIO (`static-site-storage`) に再アップロード。
    -   DBの該当サイトレコードを`status: 'public'`に更新し、`minio_path`を保存。
5.  **表示**:
    -   ユーザーは`/static-sites/{siteId}/index.html`のようなURLで静的サイトにアクセスできる。Nginxがこのリクエストを`static-site-storage`にプロキシする。

## 5. 表示ロジックの核心（設計思想）

### 5.1. ゲーム表示ロジック
**課題**: Unityゲームは様々な解像度で作成されるため、固定の`iframe`サイズに合わせると、UIの見切れやアスペクト比の歪みが発生する。
**解決思想**: Unityのレンダリング解像度には介入せず、React側でCSSの`transform: scale()`を用いて「見た目」だけをスケーリングする。

-   **通常表示 (800x450のコンテナ内)**:
    1.  `game-worker`はUnityの`index.html`に**一切のサイズ変更を加えず**、フッター非表示などの最小限のCSSのみを注入する。
    2.  `GameDetailPage`は、DBから取得した**ネイティブ解像度**（例: `960x600`）を持つ`<Box>`コンテナを生成する。
    3.  この`<Box>`の中に、`width: 100%`, `height: 100%`の`iframe`を配置する。
    4.  `800x450`の親コンテナ（`<Paper>`）に収めるための基本縮小率（`baseScale`）を計算する。
    5.  `<Box>`に`transform: scale(baseScale * game.scale)`を適用し、見た目上だけを縮小・調整する。
    6.  これにより、Unityは常にネイティブ解像度でレンダリングするため、UIの見切れや歪みが発生しない。

-   **フルスクリーン表示**:
    1.  `GameDetailPage`が`fullscreenchange`イベントを検知し、`isFullscreen`ステートを更新する。
    2.  `isFullscreen`が`true`の場合、`Math.max(画面幅 / ゲーム幅, 画面高さ / ゲーム高さ)`の計算により、画面を完全に覆う「カバー」表示のための拡大率を動的に算出。
    3.  この拡大率を`<Box>`の`transform: scale()`に適用し、ゲーム画面を拡大する。**ユーザーが設定したオフセット値は無視され、常に中央に配置される。**
    4.  通常表示とフルスクリーン表示の切り替えは、`transform`プロパティの変更のみで行われるため、CSSの`transition`によって滑らかなズームアニメーションが実現される。

### 5.2. 動画表示ロジック
-   動画は`800x450`の固定サイズの黒いコンテナ内に表示。
-   `<video>`タグに`object-fit: contain`スタイルを適用。これにより、動画のアスペクト比を維持したままコンテナ内に収まり、アスペクト比が異なる場合は自動的に黒帯（レターボックスまたはピラーボックス）が追加される。

## 6. 認証と認可の仕様（重要）

### 6.1. 削除・編集時の認証
**課題**: 誰でもコンテンツを削除・編集できてしまうと、セキュリティ上の問題となる。
**解決策**: コンテンツの所有者または管理者のみが操作できるように、バックエンドで厳格な認可チェックを行う。

-   **フロントエンドの責務**: `DELETE`や`PUT`リクエストを送信する際、必ず`Authorization: Bearer <token>`ヘッダーを付与する。
    ```javascript
    // 例: GameDetailPage.tsx
    await axios.delete(`/api/games/${gameIdFromPath}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    ```
-   **バックエンドの責務**:
    1.  `authMiddleware`がJWTを検証し、リクエストコンテキストに`userID`と`isAdmin`フラグをセットする。
    2.  各ハンドラ（`deleteGameHandler`など）は、まずDBからコンテンツの`uploader_id`を取得する。
    3.  コンテキストの`userID`が`uploader_id`と一致するか、または`isAdmin`が`true`であるかを検証する。条件を満たさない場合は`403 Forbidden`エラーを返す。

## 7. Nginx設定の規約と設計思想（重要）

このプロジェクトの`nginx.conf`は、単なる静的ファイル配信だけでなく、システム全体の入り口となる**APIゲートウェイ**としての重要な役割を担っています。将来の変更時にも以下の規約を維持することで、システムの安定性を保ってください。

### 7.1. APIゲートウェイとしての役割
-   **責務**: `frontend`コンテナ内のNginxは、すべてのHTTPリクエストを受け取ります。
-   **静的コンテンツ**: `/`など、API以外のパスへのリクエストには、ビルドされたReactアプリ (`index.html`) を返します。
-   **動的コンテンツ**: `/api/...` へのリクエストは、パスに応じて適切なバックエンドサービスに転送（プロキシ）します。

### 7.2. `proxy_pass`の規約: 末尾のスラッシュ
**最重要規約**: `proxy_pass`ディレクティブのURLの末尾にスラッシュを**付けません**。

```nginx
# 正しい例 (Good)
location /api/auth {
    proxy_pass http://auth_service;
}

# 間違った例 (Bad)
location /api/auth/ {
    proxy_pass http://auth_service/;
}
```

-   **理由**: 末尾にスラッシュを付けない場合、NginxはリクエストURIをそのままバックエンドに渡します。
    -   リクエスト: `GET /api/auth/me`
    -   転送先: `http://auth_service/api/auth/me`
-   これにより、各バックエンドサービス（Go/Gin）は自身の担当するパスプレフィックス（例: `/api/auth`）を含めてルーティングを定義でき、コードの可読性が向上します。
-   もし末尾にスラッシュを付けると (`proxy_pass http://auth_service/`)、`location`でマッチした部分が削られて転送されるため (`/me`だけが送られる)、バックエンドのルーティングが複雑化します。**現在の設計はこの規約に依存しているため、変更は禁止です。**

### 7.3. ヘッダー転送の重要性
すべての`proxy_pass`ブロックには、以下のヘッダー転送設定を含めます。

```nginx
proxy_set_header Host $http_host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
```

-   **理由**: これにより、バックエンドサービスはロードバランサやプロキシの背後にいることを意識せず、クライアントの元のIPアドレスやプロトコル（http/https）を正しく認識できます。

### 7.4. `location`ブロックの評価順序と使い分け
Nginxは最も具体的にマッチする`location`を優先します。このプロジェクトでは以下の優先順位で使い分けています。
1.  **`=` (完全一致)**: `/api/videos/upload` のように、他のパスと絶対に競合しない特定のパスに使用します。最も高速に処理されます。
2.  **`~` (正規表現)**: `/api/videos/([0-9]+)` のように、パス内に動的なIDが含まれる場合に使用します。
3.  **プレフィックス (修飾子なし)**: `/api/auth` のように、特定のプレフィックスで始まるすべてのリクエストを捕捉するために使用します。

### 7.5. `if`文の例外的使用（技術的負債）
`nginx.conf`内には、`DELETE`メソッドを判定するための`if`文が存在します。

```nginx
location ~ ^/api/videos/([0-9]+)$ {
    if ($request_method = DELETE) {
        proxy_pass http://upload_service;
        break;
    }
    proxy_pass http://stream_service;
}
```

-   **背景**: Nginxの世界では**「if is evil」**として知られており、`if`の使用は予期せぬ挙動の原因となるため、原則として避けるべきです。
-   **現状の理由**: `GET /api/videos/:id`（動画詳細）は`stream-service`、`DELETE /api/videos/:id`（動画削除）は`upload-service`と、同じURLで担当サービスが異なるため、暫定的に`if`でメソッドを判定しています。
-   **将来の展望**: これは技術的負債として認識しており、将来的には`upload-service`に削除専用のエンドポイント（例: `/api/videos/delete/:id`）を設けるなど、`if`を使わないルーティングへのリファクタリングが望ましいです。

## 8. APIエンドポイント一覧

| メソッド | パス                     | 担当サービス              | 説明                               | 認証 |
| -------- | ------------------------ | ------------------------- | ---------------------------------- | ---- |
| `POST`   | `/api/auth/register`     | `auth-service`            | 管理者アカウントの登録             | 不要 |
| `POST`   | `/api/auth/login`        | `auth-service`            | 管理者ログイン（デバッグ用）       | 不要 |
| `GET`    | `/api/auth/google/login` | `auth-service`            | Google OAuth認証を開始             | 不要 |
| `GET`    | `/api/profile/me`        | `profile-service`         | 自分のプロフィール情報を取得       | 要   |
| `GET`    | `/api/profile/status`    | `profile-service`         | 自分のアカウント状態を取得         | 要   |
| `PUT`    | `/api/profile`           | `profile-service`         | プロフィール情報（名前・自己紹介）を更新 | 要   |
| `PUT`    | `/api/profile/icon`      | `profile-service`         | アイコン画像を更新                 | 要   |
| `PUT`    | `/api/profile/background`| `profile-service`         | 背景画像を更新                     | 要   |
| `GET`    | `/api/my/videos`         | `mypage-service`          | 自分の動画一覧を取得               | 要   |
| `GET`    | `/api/my/games`          | `mypage-service`          | 自分のゲーム一覧を取得             | 要   |
| `GET`    | `/api/my/static-sites`   | `mypage-service`          | 自分の静的サイト一覧を取得         | 要   |
| `POST`   | `/api/videos/upload`     | `upload-service`          | 動画アップロード                   | 要   |
| `PUT`    | `/api/videos/:id`        | `upload-service`          | 動画メタデータの更新               | 要   |
| `DELETE` | `/api/videos/:id`        | `upload-service`          | 動画の削除                         | 要   |
| `GET`    | `/api/videos`            | `stream-service`          | 動画一覧を取得                     | 不要 |
| `GET`    | `/api/videos/:id`        | `stream-service`          | 動画詳細を取得                     | 不要 |
| `GET`    | `/api/videos/:id/stream` | `stream-service`          | 動画ストリーミング                 | 不要 |
| `POST`   | `/api/games/upload`      | `game-upload-api`         | ゲームアップロードと処理Jobの発行  | 要   |
| `GET`    | `/api/games`             | `game-upload-api`         | ゲーム一覧を取得                   | 不要 |
| `GET`    | `/api/games/:id`         | `game-upload-api`         | ゲーム詳細を取得                   | 不要 |
| `PUT`    | `/api/games/:id`         | `game-upload-api`         | ゲームメタデータの更新             | 要   |
| `PUT`    | `/api/games/adjust/:id`  | `game-upload-api`         | ゲームの表示調整値を保存           | 要   |
| `DELETE` | `/api/games/:id`         | `game-upload-api`         | ゲームの削除                       | 要   |
| `POST`   | `/api/static-sites/upload`| `static-site-upload-api` | 静的サイトアップロードと処理Jobの発行 | 要   |
| `GET`    | `/api/static-sites`      | `static-site-upload-api` | 静的サイト一覧を取得               | 不要 |
| `GET`    | `/api/static-sites/:id`  | `static-site-upload-api` | 静的サイト詳細を取得               | 不要 |
| `DELETE` | `/api/static-sites/:id`  | `static-site-upload-api` | 静的サイトの削除                   | 要   |

## 9. ディレクトリと主要ファイルの役割
```
/
├── .idea/
│   └── .name             # IDEの表示名を 'Atmosidea' に設定
├── docker-compose.yml    # 全サービスの構成定義
├── frontend/
│   ├── src/
│   │   ├── pages/        # 各ページのコンポーネント
│   │   │   ├── GameDetailPage.tsx  # ゲーム表示ページ
│   │   │   ├── AdjustGamePage.tsx  # ゲーム調整ページ
│   │   │   ├── EditGamePage.tsx    # ゲーム編集ページ
│   │   │   ├── EditVideoPage.tsx   # 動画編集ページ
│   │   │   └── StaticSiteDetailPage.tsx # 静的サイト表示ページ (仮)
│   │   └── context/
│   │       └── AuthContext.tsx   # 認証状態のグローバル管理
│   ├── nginx.conf        # Nginxリバースプロキシ設定
│   └── Dockerfile
│
├── game-upload-api/      # ゲームメタデータAPI
│   └── main.go
│
├── game-worker/          # ゲーム非同期処理ワーカー
│   └── main.go
│
├── static-site-upload-api/ # 静的サイトメタデータAPI
│   └── main.go
│
├── static-site-worker/   # 静的サイト非同期処理ワーカー
│   └── main.go
│
├── postgres/
│   └── init.sql          # DB初期化スキーマ
│
├── stream-service/       # 動画配信サービス
│   └── main.go
│
├── upload-service/       # 動画アップロードサービス
│   └── main.go
│
├── game_storage_db/      # MinIO (game-storage) の永続化ディレクトリ
├── profile_storage_db/   # MinIO (profile-storage) の永続化ディレクトリ
├── static_site_storage_db/ # MinIO (static-site-storage) の永続化ディレクトリ
└── video_storage_db/     # 動画ファイルとサムネイルの永続化ディレクトリ
```

## 10. データベーススキーマ

### 10.1. `users` テーブル
| カラム名        | 型          | 説明                               |
| --------------- | ----------- | ---------------------------------- |
| `id`            | `SERIAL`    | 主キー                             |
| `username`      | `VARCHAR`   | ユーザー名                         |
| `email`         | `VARCHAR`   | メールアドレス（UNIQUE）           |
| `password_hash` | `VARCHAR`   | ハッシュ化されたパスワード         |
| `provider`      | `VARCHAR`   | 認証プロバイダ ('local', 'google') |
| `provider_id`   | `VARCHAR`   | OAuthプロバイダのユーザーID        |
| `is_admin`      | `BOOLEAN`   | 管理者フラグ                       |

### 10.2. `games` テーブル
| カラム名          | 型          | 説明                                         |
| ----------------- | ----------- | -------------------------------------------- |
| `id`              | `VARCHAR(10)`| 主キー                                       |
| `user_id`         | `INTEGER`   | 投稿者のID                                   |
| `title`           | `VARCHAR(255)`| ゲームのタイトル                             |
| `description`     | `TEXT`      | ゲームの説明                                 |
| `status`          | `VARCHAR(20)`| 処理状態 ('processing', 'public', 'error')   |
| `game_url`        | `VARCHAR(255)`| ゲームの`index.html`へのURL                  |
| `thumbnail_url`   | `VARCHAR(255)`| サムネイル画像のURL                          |
| `scale`           | `REAL`      | ユーザーが調整した拡大率                     |
| `offset_x`        | `INTEGER`   | ユーザーが調整したX軸オフセット              |
| `offset_y`        | `INTEGER`   | ユーザーが調整したY軸オフセット              |
| `native_width`    | `INTEGER`   | **抽出されたゲームのネイティブ解像度（幅）** |
| `native_height`   | `INTEGER`   | **抽出されたゲームのネイティブ解像度（高さ）** |
| `created_at`      | `TIMESTAMP WITH TIME ZONE`| 作成日時                                     |
| `updated_at`      | `TIMESTAMP WITH TIME ZONE`| 更新日時                                     |

### 10.3. `static_sites` テーブル
| カラム名          | 型          | 説明                                         |
| ----------------- | ----------- | -------------------------------------------- |
| `id`              | `VARCHAR(10)`| 主キー                                       |
| `user_id`         | `INTEGER`   | 投稿者のID                                   |
| `title`           | `VARCHAR(255)`| 静的サイトのタイトル                         |
| `description`     | `TEXT`      | 静的サイトの説明                             |
| `minio_path`      | `VARCHAR(255)`| MinIO上のサイトのルートパス（例: `siteId/`）|
| `status`          | `VARCHAR(20)`| 処理状態 ('processing', 'public', 'error')   |
| `created_at`      | `TIMESTAMP WITH TIME ZONE`| 作成日時                                     |

## 11. セットアップと開発フロー

### 11.1. 環境設定 (`.env`)
`.env.example`をコピーして`.env`を作成し、各値を設定してください。特に`JWT_SECRET`と`ADMIN_REGISTRATION_CODE`は必ず変更してください。
新しいMinIOサービスのために、`STATIC_SITE_MINIO_ACCESS_KEY_ID`と`STATIC_SITE_MINIO_SECRET_ACCESS_KEY`も設定が必要です。

### 11.2. 管理用スクリプト (`.bat`)
-   **`clean.bat`**: **環境を完全にクリーンアップします。** コンテナ、ネットワーク、名前付きボリューム、ビルドキャッシュをすべて削除します。
-   **`update.bat`**: コード変更を反映させるための最も一般的なスクリプト。
-   **`start.bat` / `stop.bat`**: 日常的なコンテナの起動・停止に。

### 11.3. 日常の開発サイクル
1.  最初に`clean.bat`を実行し、環境を完全に初期化します。
2.  `update.bat`で全サービスをビルド・起動します。
3.  ブラウザで `http://localhost:3001` にアクセスします。
4.  コード変更後は、再度`update.bat`を実行して変更を反映させます。

## 12. 将来の課題と改善点
-   **Nginxの`if`文**: 上記「7.5. `if`文の例外的使用」で述べた通り、リファクタリングが望ましいです。
-   **テスト**: 現状、自動テストがありません。ユニットテストやE2Eテストを導入することで、将来の機能追加やリファクタリングをより安全に行えるようになります。
-   **エラーハンドリング**: 各サービスのログ出力は強化されましたが、ユーザーに表示されるエラーメッセージはまだ汎用的なものが多いです。より具体的で分かりやすいエラーフィードバックの向上が期待されます。

## 13. 最近の主な変更点 (2026/08/01)

このセクションでは、最近行われた大規模なリファクタリングと機能追加について解説します。

### 13.1. アーキテクチャの変更: DB分割と新サービスの追加

システムの関心事をより明確に分離するため、データベースとサービス構成に以下の変更が加えられました。

-   **データベースの分割**:
    -   従来単一だった`postgres`コンテナを、**認証・ユーザー情報**を専門に扱う`auth-db`と、**アプリケーションのコンテンツ（動画・ゲーム）**を専門に扱う`app-db`の2つに分割しました。
    -   これにより、ユーザーデータとコンテンツデータの独立性が高まり、将来的なスケーリングやメンテナンスが容易になります。

-   **新サービスの追加**:
    -   **`mypage-service`**: ユーザーが投稿した動画やゲームの一覧を取得するための専用サービスです。`/api/my/*` のエンドポイントを担当します。
    -   **`profile-service`**: ユーザープロフィールの取得・更新（ユーザー名、自己紹介、アイコン、背景画像）を担当する専用サービスです。`/api/profile/*` のエンドポイントを担当します。

#### 13.1.1. 更新後の概念図

```
[ユーザー] -> [ブラウザ] -> [Nginx (frontend:80)]
                                |
           /api/auth/**         +-> [auth-service:8080] ----> [auth-db:5432]
           /api/profile/**      +-> [profile-service:8084] -> [auth-db:5432] -> [MinIO:9000] (for icons)
           /api/my/**           +-> [mypage-service:8083] --> [app-db:5432]
                                |
           /api/videos/**       +-> [upload-service:8080] --> [app-db:5432]
                                |   [stream-service:8081] --> [app-db:5432]
                                |
           /api/games/**        +-> [game-upload-api:8082] -> [app-db:5432] -> [Redis:6379]
           /games/**            +-> [MinIO (game-storage:9000)]
                                |
           (非同期処理)         [game-worker] <- [Redis:6379] -> [MinIO:9000]
                                                              -> [app-db:5432]
```

#### 13.1.2. 更新後のサービス詳細

| サービス名          | 内部ポート | 技術          | 責務                                                                                                                                                             |
| ------------------- | ---------- | ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `frontend`          | `80`       | Nginx, React  | **UI/APIゲートウェイ**: Reactアプリを提供し、`/api`以下のリクエストを各バックエンドサービスに振り分ける。                                                              |
| `auth-service`      | `8080`     | Go, Gin       | **認証**: ユーザー登録、Google OAuth、JWT発行を担当。**`auth-db`**に接続。                                                                                        |
| `profile-service`   | `8084`     | Go, Gin       | **プロフィール管理**: ユーザー名、自己紹介、アイコン、背景画像の取得・更新を担当。**`auth-db`**と**`minio`**に接続。                                                 |
| `mypage-service`    | `8083`     | Go, Gin       | **マイページ**: ログインユーザーの投稿コンテンツ一覧（動画・ゲーム）を取得。**`app-db`**に接続。                                                                   |
| `upload-service`    | `8080`     | Go, Gin       | **動画アップロード**: 動画ファイルのアップロード、サムネイル生成、DBへのメタデータ保存を担当。**`app-db`**に接続。                                                     |
| `stream-service`    | `8081`     | Go, Gin       | **動画配信**: 動画のストリーミング配信とメタデータ提供を担当。**`app-db`**に接続。                                                                                 |
| `game-upload-api`   | `8082`     | Go, Gin       | **ゲームメタデータAPI**: ゲームのメタデータ管理と、`game-worker`への処理要求（Redis経由）を担当。**`app-db`**に接続。                                                 |
| `game-worker`       | -          | Go            | **ゲーム非同期処理**: RedisからJobを受け取り、ZIP解凍、解像度抽出、MinIOへのファイルアップロード、DB更新といった時間のかかる処理を実行。**`app-db`**に接続。          |
| `auth-db`           | `5432`     | PostgreSQL    | **認証・ユーザーDB**: ユーザー情報、プロフィール、アカウント状態を永続化。                                                                                       |
| `app-db`            | `5432`     | PostgreSQL    | **アプリケーションDB**: 動画、ゲームのメタデータを永続化。                                                                                                       |
| `redis`             | `6379`     | Redis         | **メッセージキュー**: `game-upload-api`と`game-worker`間の非同期タスクの受け渡し。                                                                               |
| `minio`             | `9000`     | MinIO         | **オブジェクトストレージ**: ゲームアセット、プロフィール画像（アイコン、背景）をホスト。                                                                           |

---

### 13.2. 認証・新規登録フローの変更

ユーザー体験とセキュリティ向上のため、認証フローが大幅に変更されました。

-   **Google OAuthへの一本化**:
    -   一般ユーザーの新規登録およびログインは、**Googleアカウント認証のみ**になりました。これにより、パスワード管理の必要がなくなり、セキュリティが向上します。
    -   管理者のみ、特別なログインフォームからデバッグ用の認証情報でログインできます。

-   **プロフィール登録を必須化する新しいワークフロー**:
    1.  ユーザーが初めてGoogleでログインすると、`auth-service`は`users`テーブルに新しいレコードを`status: 'pending'`で作成します。
    2.  ログインが成功すると、フロントエンドはユーザーの`status`を`/api/profile/status`に問い合わせます。
    3.  `status`が`pending`の場合、ユーザーは強制的に**プロフィール編集ページ (`/edit-profile`)**にリダイレクトされます。
    4.  ユーザーがプロフィール（ユーザー名、自己紹介など）を更新すると、`profile-service`は`users`テーブルの`status`を`active`に変更します。
    5.  `status`が`active`になると、ユーザーはマイページやその他の機能へアクセスできるようになります。

---

### 13.3. 更新後のAPIエンドポイント一覧

| メソッド | パス                     | 担当サービス        | 説明                               | 認証 |
| -------- | ------------------------ | ------------------- | ---------------------------------- | ---- |
| `POST`   | `/api/auth/register`     | `auth-service`      | 管理者アカウントの登録             | 不要 |
| `POST`   | `/api/auth/login`        | `auth-service`      | 管理者ログイン（デバッグ用）       | 不要 |
| `GET`    | `/api/auth/google/login` | `auth-service`      | Google OAuth認証を開始             | 不要 |
| `GET`    | `/api/profile/me`        | `profile-service`   | 自分のプロフィール情報を取得       | 要   |
| `GET`    | `/api/profile/status`    | `profile-service`   | 自分のアカウント状態を取得         | 要   |
| `PUT`    | `/api/profile`           | `profile-service`   | プロフィール情報（名前・自己紹介）を更新 | 要   |
| `PUT`    | `/api/profile/icon`      | `profile-service`   | アイコン画像を更新                 | 要   |
| `PUT`    | `/api/profile/background`| `profile-service`   | 背景画像を更新                     | 要   |
| `GET`    | `/api/my/videos`         | `mypage-service`    | 自分の動画一覧を取得               | 要   |
| `GET`    | `/api/my/games`          | `mypage-service`    | 自分のゲーム一覧を取得             | 要   |
| `POST`   | `/api/videos/upload`     | `upload-service`    | 動画アップロード                   | 要   |
| `PUT`    | `/api/videos/:id`        | `upload-service`    | 動画メタデータの更新               | 要   |
| `DELETE` | `/api/videos/:id`        | `upload-service`    | 動画の削除                         | 要   |
| `GET`    | `/api/videos`            | `stream-service`    | 動画一覧を取得                     | 不要 |
| `GET`    | `/api/videos/:id`        | `stream-service`    | 動画詳細を取得                     | 不要 |
| `GET`    | `/api/videos/:id/stream` | `stream-service`    | 動画ストリーミング                 | 不要 |
| `POST`   | `/api/games/upload`      | `game-upload-api`   | ゲームアップロードと処理Jobの発行  | 要   |
| `GET`    | `/api/games`             | `game-upload-api`   | ゲーム一覧を取得                   | 不要 |
| `GET`    | `/api/games/:id`         | `game-upload-api`   | ゲーム詳細を取得                   | 不要 |
| `PUT`    | `/api/games/:id`         | `game-upload-api`   | ゲームメタデータの更新             | 要   |
| `PUT`    | `/api/games/adjust/:id`  | `game-upload-api`   | ゲームの表示調整値を保存           | 要   |
| `DELETE` | `/api/games/:id`         | `game-upload-api`   | ゲームの削除                       | 要   |

---

### 13.4. 更新後のデータベーススキーマ

#### 13.4.1. `auth-db` の `users` テーブル
| カラム名                 | 型          | 説明                               |
| ------------------------ | ----------- | ---------------------------------- |
| `id`                     | `SERIAL`    | 主キー                             |
| `username`               | `VARCHAR`   | ユーザー名                         |
| `email`                  | `VARCHAR`   | メールアドレス（UNIQUE）           |
| `password_hash`          | `VARCHAR`   | ハッシュ化されたパスワード         |
| `provider`               | `VARCHAR`   | 認証プロバイダ ('local', 'google') |
| `provider_id`            | `VARCHAR`   | OAuthプロバイダのユーザーID        |
| `is_admin`               | `BOOLEAN`   | 管理者フラグ                       |
| `icon_url`               | `TEXT`      | アイコン画像のURL                  |
| `bio`                    | `TEXT`      | 自己紹介文                         |
| `background_image_url`   | `TEXT`      | プロフィール背景画像のURL          |
| `status`                 | `VARCHAR`   | アカウント状態 ('pending', 'active') |

#### 13.4.2. `app-db` のテーブル
`videos`テーブルと`games`テーブルの`uploader_id`および`user_id`カラムは、`auth-db`への外部キー制約を持たない単なる`INTEGER`型として扱われます。

---
### 13.5. HLS動画再生機能のトラブルシューティングと修正履歴 (2026/08/01)

HLS動画再生機能の実装において発生した問題と、その解決までの経緯を以下にまとめます。

#### 13.5.1. 問題の概要
動画詳細ページでHLS動画が再生されず、「メディアを利用できません」というエラーが表示されました。DevToolsの確認では、`video`要素の`src`が空で、HLS関連のネットワークリクエストが一切発生していない状態でした。

#### 13.5.2. トラブルシューティングと修正ステップ

1.  **フロントエンド (`VideoDetailPage.tsx`) の初期設定ミス**
    *   **問題**: `hls.js`は導入されていたものの、`hls.loadSource()`に渡すURLが、APIゲートウェイ経由のパス (`/api/videos/:id/stream`) ではなく、バックエンドが返すMinIO上のファイルパス (`/storage/videos/...`) を直接参照していたため、ブラウザからアクセスできませんでした。
    *   **修正**: `VideoDetailPage.tsx`の`streamUrl`を`/api/videos/${video.id}/stream`に変更し、APIゲートウェイ経由でリクエストするように修正しました。

2.  **Nginxルーティングの不備**
    *   **問題**: `/api/videos/:id/stream`へのリクエストを処理する`location`ブロックが`nginx.conf`に存在せず、Nginxがリクエストを`stream-service`に転送できていませんでした。
    *   **修正**: `nginx.conf`に`location ~ ^/api/videos/([0-9]+)/stream { proxy_pass http://stream_service; ... }`を追加し、HLSストリームリクエストを`stream-service`に転送するようにしました。

3.  **TypeScriptコンパイルエラー**
    *   **問題**: `VideoDetailPage.tsx`のHLSイベントリスナーで、`event`引数が宣言されているものの使用されていなかったため、`TS6133: 'event' is declared but its value is never read.`というコンパイルエラーが発生し、ビルドが失敗しました。
    *   **修正**: `event`引数を`_event`に変更し、未使用であることを明示することでエラーを解消しました。

4.  **React `useEffect`の実行タイミング問題**
    *   **問題**: `video`ステートが更新された直後でも`videoRef.current`が`null`のままで、HLSの初期化処理が実行されないタイミングの問題が発生しました。
    *   **修正**: `useEffect`の依存配列を`[video]`から`[loading, video]`に変更し、`!loading && video && videoRef.current`という条件でHLSのセットアップを実行するようにしました。これにより、ローディングが完了し、`<video>`要素がDOMに描画された後に処理が実行されるようになりました。

5.  **NginxによるHLSマニフェストファイル名指定の不足**
    *   **問題**: フロントエンドが`/api/videos/:id/stream`というディレクトリパスにリクエストを送信していたため、NginxがMinIOに転送しても`404 Not Found`エラーが発生していました。
    *   **修正**: `VideoDetailPage.tsx`の`streamUrl`を`/api/videos/${video.id}/stream/playlist.m3u8`に変更し、HLSマニフェストファイル名（`playlist.m3u8`）を明示的に指定するようにしました。

6.  **MinIOバケットのアクセス権限 (`403 Forbidden`)**
    *   **問題**: `minio-init`サービスが`videos`バケットに対して匿名ダウンロードポリシーを設定していなかったため、MinIOへのアクセスが`403 Forbidden`エラーで拒否されました。
    *   **修正**: `docker-compose.yml`内の`minio-init`サービスの`entrypoint`を修正し、`games`バケットと`videos`バケットの両方を作成し、匿名ダウンロードポリシーを設定するようにしました。

7.  **動画ファイルの保存場所の誤解とNginx設定の不一致**
    *   **問題**: これまでのトラブルシューティングは「動画ファイルがMinIOに保存されている」という誤った前提で進められていました。実際には、`docker-compose.yml`の`volumes`設定から、動画ファイルはホストマシンの`./storage/videos`ディレクトリに保存されていることが判明しました。このため、NginxがMinIOにリクエストを転送しても`404 Not Found`エラーが発生していました。
    *   **最終修正**:
        *   **`docker-compose.yml`**: `frontend`サービスに`./storage/videos:/storage/videos:ro`のボリュームマウントを追加し、Nginxがローカルの動画ファイルにアクセスできるようにしました。
        *   **`nginx.conf`**: `/api/videos/([0-9]+)/stream/(.*)$`へのリクエストを`proxy_pass`ではなく`alias /storage/videos/$1/$2;`で処理するように変更し、コンテナ内のローカルファイルパスに直接マッピングしました。また、HLSに必要なMIMEタイプとCORSヘッダーも設定しました。
        *   **`nginx.conf`**: `Content-Security-Policy`に`media-src 'self' blob:;`を追加し、HLS.jsが内部で利用する`blob:` URLからのメディア読み込みを許可しました。

これらの修正により、HLS動画が正常に再生されるようになりました。

### 13.6. HLS動画再生の内部フロー

`Atmosidea`におけるHLS (HTTP Live Streaming) 動画再生の内部的な流れは以下のようになります。

1.  **ユーザーが動画詳細ページにアクセス**
    *   ブラウザが `http://localhost:3001/videos/:id` にアクセスします。
    *   `frontend`サービス (Nginx) がReactアプリケーションの `index.html` を返します。

2.  **Reactコンポーネント (`VideoDetailPage.tsx`) の初期化**
    *   `VideoDetailPage.tsx` がマウントされ、`useEffect` フックが実行されます。
    *   `axios.get('/api/videos/:id')` を通じて、バックエンドの `stream-service` から動画のメタデータ（タイトル、説明、IDなど）を取得します。この際、動画のファイルパスは `filename` フィールドに含まれますが、これはHLSのルートディレクトリを示すもので、直接再生には使用しません。

3.  **HLS.js のセットアップとストリームURLの構築**
    *   動画メタデータの取得が完了し、`loading` ステートが `false` になると、HLSセットアップ用の `useEffect` が再実行されます。
    *   `videoRef.current` が `<video>` 要素を指していることを確認後、HLSストリームのURLを構築します。
    *   **構築されるURL**: `/api/videos/${video.id}/stream/playlist.m3u8`
        *   ここで `playlist.m3u8` は、HLS変換によって生成されるマスタープレイリストファイルの名前です。

4.  **HLS.js によるストリームのロード**
    *   `Hls.isSupported()` でブラウザがHLSをサポートしているか確認します。
    *   `hls = new Hls({ debug: true })` で `hls.js` インスタンスが生成されます。
    *   `hls.loadSource(streamUrl)` が呼び出され、構築されたURL (`/api/videos/:id/stream/playlist.m3u8`) に対してHTTPリクエストが発行されます。
    *   `hls.attachMedia(videoEl)` により、`hls.js` が `<video>` 要素にアタッチされ、メディアソース拡張 (MSE) を通じて動画データのバッファリングと再生を制御する準備が整います。

5.  **Nginx による HLS ストリームのルーティング**
    *   フロントエンドから発行された `/api/videos/:id/stream/playlist.m3u8` へ
のリクエストは、`frontend` コンテナ内のNginxに到達します。
    *   `nginx.conf` 内の以下の `location` ブロックがこのリクエストにマッチします。
        ```nginx
        location ~ ^/api/videos/([0-9]+)/stream/(.*)$ {
            alias /storage/videos/$1/$2;
            # ... (MIMEタイプ、CORSヘッダー設定)
        }
        ```
    *   Nginxは `alias` ディレクティブに基づき、リクエストパスをコンテナ内のローカルファイルパス (`/storage/videos/:id/playlist.m3u8`) にマッピングします。
    *   `frontend` コンテナには `docker-compose.yml` で `./storage/videos:/storage/videos:ro` がボリュームマウントされているため、NginxはこのローカルパスにあるHLSファイル（`playlist.m3u8`）にアクセスできます。

6.  **HLSプレイリストの解析とセグメントの要求**
    *   Nginxから返された `playlist.m3u8` ファイルを `hls.js` が受信し、解析します。
    *   `playlist.m3u8` には、動画の異なる品質（解像度、ビットレート）のストリームへのリンク（サブプレイリスト）と、それぞれのストリームを構成する短い動画セグメント（`.ts` ファイル）へのURLが含まれています。
    *   `hls.js` は、ネットワーク状況やバッファの状態に応じて最適な品質のサブプレイリストを選択し、その中の `.ts` セグメントファイルを順次リクエストします。
    *   これらの `.ts` セグメントファイルへのリクエストも、同様に `/api/videos/:id/stream/segmentX.ts` の形式でNginxに送られ、Nginxはそれを `/storage/videos/:id/segmentX.ts` としてローカルファイルシステムから取得し、 `hls.js` に返します。

7.  **動画の再生**
    *   `hls.js` は受信した `.ts` セグメントをデコードし、`<video>` 要素のメディアバッファに供給します。
    *   十分なデータがバッファされると、`hls.js` は `videoEl.play()` を呼び出し、動画の再生が開始されます。
    *   `hls.js` は再生中に `MANIFEST_PARSED` や `ERROR` などのイベントを発行し、ログ出力やエラーハンドリングに利用されます。

このフローにより、ユーザーはHLS形式でエンコードされた動画をスムーズに視聴することができます。
---
## 14. 最近の主な変更点 (2026/08/05)

このセクションでは、2026年8月5日に行われた大規模なリファクタリングと機能追加について解説します。

### 14.1. アーキテクチャの変更: MinIOの物理的分離

**課題**: 従来、ゲームのアセットとユーザーのプロフィール画像（アイコン、背景）は、単一のMinIOコンテナ内の同じバケットに保存されており、データの関心事が混在していました。

**解決策**: MinIOコンテナを**用途別に3つに分割**し、それぞれがプロジェクトルートの独立した物理ディレクトリにデータを保存するように変更しました。

-   **`game-storage`**:
    -   **責務**: ゲーム関連のアセットのみを管理します。
    -   **バケット**: `games`
    -   **物理ディレクトリ**: `./game_storage_db/`
-   **`profile-storage`**:
    -   **責務**: ユーザーのプロフィール画像（アイコン、背景）のみを管理します。
    -   **バケット**: `user-profiles`
    -   **物理ディレクトリ**: `./profile_storage_db/`
-   **`static-site-storage`**:
    -   **責務**: 静的サイトのHTML, CSS, JSなどのファイルを管理します。
    -   **バケット**: `static-sites`
    -   **物理ディレクトリ**: `./static_site_storage_db/`
-   **`video_storage_db`**:
    -   **責務**: 動画ファイル（HLS形式）とサムネイルをローカルファイルシステムに保存します。
    -   **物理ディレクトリ**: `./video_storage_db/`

この変更により、以下のメリットがもたらされました。
-   **データ管理の明確化**: データの種類ごとに保存場所が物理的に分離され、バックアップや管理が容易になりました。
-   **セキュリティの向上**: コンテナごとに異なるアクセスキーを設定できるため、各サービスの権限を最小限に留めることができます。

### 14.2. ID体系の刷新: ランダム文字列IDの導入

**課題**: 従来、動画やゲームのIDはデータベースの連番（`SERIAL`）であり、URLから次のコンテンツが容易に推測可能でした。

**解決策**: 全ての動画とゲームのIDを、**予測不可能な10文字のランダムな英数字**に変更しました。

-   **データベース**: `app-db`内の`videos`および`games`テーブルの`id`カラムの型を`SERIAL`から`VARCHAR(10)`に変更。
-   **バックエンド**:
    -   `upload-service`と`game-upload-api`に、`crypto/rand`を利用した安全なランダムID生成関数を追加。
    -   コンテンツ作成時に、連番ではなくこの関数で生成したIDを付与するように変更。
    -   関連する全てのサービス（`stream-service`, `mypage-service`, `game-worker`など）で、IDを`string`として扱うように修正。
-   **フロントエンド**:
    -   関連する全てのページコンポーネント（`HomePage`, `MyPage`, `VideoDetailPage`など）で、IDの型を`number`から`string`に修正。
    -   Nginxの設定ファイル(`nginx.conf`)のルーティングも、新しい英数字ID形式に対応するように正規表現を修正。

### 14.3. UI/UXの改善

#### 14.3.1. プロフィールページのUI刷新

-   **Discord風レイアウトの採用**: ユーザーのプロフィールページ(`MyPage.tsx`)のUIを、上部にバナー画像、その境界線上にアイコンが重なるモダンなレイアウトに変更しました。
-   **アイコンへの縁取り**: プロフィールページ、ホームページ、ヘッダーに表示される全てのユーザーアイコンに、白と灰色の二重の縁取りを追加し、視認性とデザイン性を向上させました。

#### 14.3.2. 画像アップロード機能の強化: 画像クロッパーの導入

**課題**: 従来、アイコンや背景画像はアップロードされたファイルがそのまま使用されるため、ユーザーが意図しない部分が表示される可能性がありました。

**解決策**: `react-image-crop`ライブラリを導入し、画像アップロード時に**切り抜き・拡縮・回転**を行えるポップアップモーダルを実装しました。

-   **`ImageCropperModal.tsx`**: 再利用可能な画像クロッパーコンポーネントを新規作成。
-   **`EditProfilePage.tsx`**:
    -   ユーザーが画像ファイルを選択すると、このモーダルが起動します。
    -   アイコンの場合はアスペクト比`1:1`（円形）、背景画像の場合は`679:160`の調整枠が表示されます。
    -   ユーザーが「保存」を押すと、切り抜かれた画像がプレビューに即時反映され、最終的に「更新する」ボタンでサーバーにアップロードされます。

#### 14.3.3. その他のUI改善

-   **コンテンツ種別アイコン**: ホームページの各コンテンツカードの右上に、それが「動画」か「ゲーム」かを示すアイコン（`VideocamIcon`または`SportsEsportsIcon`）を表示するようにし、一覧性を向上させました。
-   **マイページのコンテンツ一覧**: マイページにもジャンルごちゃまぜのコンテンツ一覧を追加し、各コンテンツカードに投稿者情報（アイコンと名前）と、動画/ゲームアイコン、編集・削除ボタンを表示するようにしました。カードサイズは`266x292`に統一し、カード間の隙間も調整しました。
-   **認証フローの改善**: Googleアカウントでの新規登録時に、Googleアカウント名を取得せず、`username`を`NULL`で登録するように変更。ユーザーはプロフィール編集ページで最初に自身のユーザー名を設定するフローを明確化しました。これに伴い、`auth-db`の`users`テーブルスキーマと、`auth-service`および`profile-service`のDBアクセス処理を修正しました。
-   **CSPの修正**: フロントエンド開発サーバーで`blob:` URLからの画像読み込みがブロックされる問題を解決するため、`index.html`に`<meta>`タグを追加し、適切な`Content-Security-Policy`を設定しました。

### 14.4. 検索機能の導入

**課題**: コンテンツ一覧ページで特定のコンテンツを探すのが困難でした。

**解決策**: コンテンツ一覧ページに検索バーを導入し、タイトルによる検索機能を追加しました。

-   **バックエンド (`stream-service`, `game-upload-api`)**:
    -   `listVideosHandler` および `listGamesHandler` に検索クエリパラメータ (`q`) を受け取るロジックを追加。
    -   データベースクエリに `WHERE title ILIKE $1` を追加し、検索キーワードをプリペアドステートメントで安全にバインドすることで、SQLインジェクション攻撃を防止。
-   **フロントエンド (`HomePage.tsx`)**:
    -   検索キーワードを保持する `searchTerm` ステートと、デバウンスされた検索値を保持する `debouncedSearchTerm` ステートを追加。
    -   検索バー (`TextField`) をUIに実装。
    -   入力のたびにAPIリクエストが走るのを防ぐため、**デバウンス処理**を導入。ユーザーが入力し終えるまで（500ms入力が停止するまで）APIリクエストを遅延させ、不要なリロードを抑制。
    -   検索中のUIカクつきを軽減するため、`CircularProgress` の表示を `isSearching` ステートで制御し、入力中はコンテンツを表示したまま、検索完了後に結果を反映するように改善。

### 14.5. アカウント管理機能の導入

**課題**: ユーザーが自分のアカウントを削除する手段がありませんでした。特にGoogleアカウント連携ユーザーの場合、アプリ内のデータ削除とGoogle側の連携解除のフローが不明確でした。

**解決策**: ヘッダーのユーザーメニューに「アカウント管理」を追加し、アカウント削除機能を提供しました。

-   **`AccountDeletionModal.tsx`**: アカウント削除確認用のモーダルコンポーネントを新規作成。
    -   ユーザーのプロバイダ (`'local'` または `'google'`) に応じてメッセージを調整。
    -   Googleアカウント連携ユーザーの場合でも、Atomosideaアプリケーション内のデータを削除できるようにしました。
-   **`auth-service` の拡張**:
    -   `DELETE /api/auth/me` エンドポイントを拡張。
    -   ユーザーIDに基づいて、`auth-db` からユーザーレコードを削除（ローカルアカウントの場合）。
    -   Googleアカウント連携ユーザーの場合、`auth-db` のユーザーレコードは削除せず、`status` を `'deleted_data'` に更新し、プロフィール情報（ユーザー名、アイコンURL、背景画像URL、自己紹介）を `NULL` に設定。これにより、Googleアカウントそのものには影響を与えず、Atomosidea内のデータのみを削除。
    -   **関連データの削除**:
        -   `profile-storage` (MinIO) からユーザーのアイコンと背景画像を削除。
        -   `game-storage` (MinIO) からユーザーが投稿したゲームアセットを削除。
        -   `app-db` からユーザーが投稿したゲームのレコードを削除。
        -   `video_storage_db` (ローカルファイルシステム) からユーザーが投稿した動画ファイル（HLS形式）とサムネイルを削除。
        -   `app-db` からユーザーが投稿した動画のレコードを削除。
    -   `GET /api/auth/user/:userId` エンドポイントを追加し、ユーザーのプロバイダ情報を取得できるようにしました。
-   **`Header.tsx` の修正**:
    -   ユーザーメニューに「アカウント管理」の `MenuItem` を追加し、クリック時に `AccountDeletionModal` を開くように設定。
-   **`AuthContext.tsx` の修正**:
    -   `User` インターフェースに `provider` と `status` プロパティを追加。
    -   `loadUser` 関数内で `profile-service` および `auth-service` から `status` と `provider` を取得し、`User` オブジェクトに設定。
    -   `currentUser.status === 'pending'` の場合、`navigate('/edit-profile', { replace: true })` でプロフィール編集ページに強制リダイレクトするロジックを実装。
    -   `logout` 関数も `navigate('/login', { replace: true })` を使用するように修正。

---
## 15. 最近の主な変更点 (2026/08/06)

このセクションでは、2026年8月6日に行われた大規模な機能追加について解説します。

### 15.1. 静的サイト配信機能の導入

**概要**: ユーザーがHTML、CSS、JavaScriptなどの静的ファイルをZIP形式でアップロードし、それをWebサイトとして公開できる機能が追加されました。GitHub Pagesのような体験をアプリケーション内で提供します。

#### 15.1.1. アーキテクチャの変更点

-   **新しいMinIOインスタンス**:
    -   `static-site-storage`コンテナが追加されました。これは静的サイトのファイルを保存するための専用MinIOインスタンスです。
    -   物理ディレクトリとして`./static_site_storage_db/`を使用します。
    -   `minio-init`サービスが`static-site-storage`の`static-sites`バケットを初期化し、匿名ダウンロードポリシーを設定します。
-   **新しいバックエンドサービス**:
    -   **`static-site-upload-api`**:
        -   静的サイトのZIPファイルアップロードを受け付けます。
        -   `static-site-storage`にZIPファイルを一時保存します。
        -   `app-db`の`static_sites`テーブルにメタデータ（タイトル、説明、ユーザーIDなど）を`processing`ステータスで登録します。
        -   Redisキューに処理ジョブをプッシュします。
        -   静的サイトの一覧、詳細、削除などのAPIエンドポイントを提供します。
    -   **`static-site-worker`**:
        -   Redisキューからジョブを取得します。
        -   `static-site-storage`からZIPファイルをダウンロードし、解凍します。
        -   解凍した全てのファイルを、サイトIDをプレフィックスとしたパス（例: `siteId/index.html`）で`static-site-storage`にアップロードします。
        -   `app-db`の`static_sites`テーブルのステータスを`public`に更新します。
-   **Nginx設定の更新**:
    -   `frontend`コンテナのNginx設定（`nginx.conf`）に、`static-site-upload-api`へのAPIリクエストをルーティングする`location /api/static-sites`ブロックが追加されました。
    -   `static-site-storage`から静的ファイルを直接配信するための`location ~ ^/static-sites/([a-z0-9]{10})/(.*)$`ブロックが追加されました。これにより、`/static-sites/{siteId}/path/to/file.html`のようなURLでサイトコンテンツにアクセス可能になります。
-   **データベーススキーマの更新**:
    -   `app-db`に`static_sites`テーブルが追加されました。このテーブルは、静的サイトのID、投稿者ID、タイトル、説明、MinIO上のパス、ステータス、作成日時を管理します。
    -   `id`カラムは`VARCHAR(10)`型で、ランダムな英数字IDを使用します。

#### 15.1.2. 主要なワークフロー

1.  **アップロード**: ユーザーが`frontend`から静的サイトのZIPファイルをアップロードします。
2.  **API受付**: `frontend`は`/api/static-sites/upload`にリクエストを送信し、Nginx経由で`static-site-upload-api`がこれを受け取ります。
3.  **一次保存とJob発行**:
    -   `static-site-upload-api`は受け取ったZIPファイルを`static-site-storage`に一時保存します。
    -   `app-db`の`static_sites`テーブルに`status: 'processing'`でレコードを作成し、ランダムな`siteId`を取得します。
    -   Redisキューに`{siteId, objectName}`を含むジョブをプッシュします。
    -   `frontend`に`siteId`を返し、サイト詳細ページなどへリダイレクトさせます。
4.  **非同期処理**:
    -   `static-site-worker`がRedisキューからジョブを取得します。
    -   `static-site-storage`からZIPファイルをダウンロードし、解凍します。
    -   解凍した全てのファイルを、`/{siteId}/`というプレフィックスで`static-site-storage`に再アップロードします。この際、適切なMIMEタイプが自動的に設定されます。
    -   `app-db`の該当サイトレコードを`status: 'public'`に更新し、`minio_path`を保存します。
5.  **表示**:
    -   ユーザーは`/static-sites/{siteId}/index.html`のようなURLで静的サイトにアクセスできます。
    -   Nginxがこのリクエストを`static-site-storage`にプロキシし、MinIOから直接静的ファイルが配信されます。

この機能により、ユーザーは手軽に自身のWebコンテンツを公開できるようになります。
---
## 16. 最近の主な変更点 (2026/08/06) - 静的サイト機能の安定化と改善

このセクションでは、2026年8月6日に行われた静的サイト配信機能の安定化と改善について解説します。

### 16.1. 機能概要

ユーザーがZIP形式で静的サイトをアップロードし、それをWebサイトとして公開できる機能を追加しました。GitHub Pagesのような体験をアプリケーション内で提供します。

### 16.2. アーキテクチャの変更点

-   **3つの新サービス**:
    -   **`static-site-storage`**: 静的サイトのファイルを保存するための専用MinIOコンテナ。
    -   **`static-site-upload-api`**: ZIPファイルのアップロード受付、メタデータ管理、ワーカーへの処理要求を担当するAPI。
    -   **`static-site-worker`**: アップロードされたZIPを解凍し、MinIOに展開する非同期処理ワーカー。
-   **`docker-compose.yml`の更新**:
    -   上記3つのサービスを定義。
    -   `minio-init`サービスを強化し、各MinIOコンテナが完全に起動するのを待ってから、バケット作成 (`mc mb`) と公開ポリシー設定 (`mc policy set download`) を確実に行うように`entrypoint`を修正。これにより、起動時の競合状態によるアクセス権限の問題を根本的に解決しました。
-   **データベーススキーマの更新**:
    -   `app-db`の`postgres/init.sql`に`static_sites`テーブルを追加。
    -   ZIPファイル内のサブディレクトリにある`index.html`に対応するため、`entry_point_path`カラムを追加。`static-site-worker`が`index.html`を探索し、その相対パスをこのカラムに保存します。

### 16.3. 主要なワークフロー

1.  **アップロード**: ユーザーが`frontend`から静的サイトのZIPファイルをアップロードします。
2.  **API受付**: `static-site-upload-api`がリクエストを受け付け、ZIPファイルを`static-site-storage`に一時保存し、`static_sites`テーブルにレコードを作成後、Redisに処理ジョブを発行します。
3.  **非同期処理**:
    -   `static-site-worker`がジョブを取得し、ZIPファイルを解凍します。
    -   **`index.html`の探索**: ZIPファイル内を再帰的に探索し、`index.html`のパス（例: `my-dist/index.html`）を特定します。
    -   解凍した全ファイルを`/{siteId}/`プレフィックスで`static-site-storage`にアップロードします。
    -   `static_sites`テーブルのステータスを`public`に更新し、特定した`index.html`のパスを`entry_point_path`に保存します。
4.  **表示**:
    -   フロントエンド (`StaticSiteDetailPage.tsx`) は、APIから取得した`entry_point_path`を使い、`/static-sites/{id}/{entry_point_path}`という正しいURLを構築して`iframe`で表示します。

### 16.4. フロントエンドの変更点

-   **新規ページの作成**:
    -   `UploadStaticSitePage.tsx`: 静的サイトのアップロード用UI。
    -   `StaticSiteDetailPage.tsx`: アップロードされたサイトのプレビューと情報表示ページ。
-   **既存ページの更新**:
    -   `HomePage.tsx`と`MyPage.tsx`に「静的サイト」タブを追加し、コンテンツ一覧に静的サイトが含まれるように修正。
-   **`iframe`の権限設定**:
    -   `StaticSiteDetailPage.tsx`の`iframe`に`sandbox`属性と`allow`属性を適切に設定 (`sandbox="allow-scripts allow-popups allow-forms allow-modals allow-same-origin"`, `allow="camera"`)。これにより、`iframe`内のサイトがカメラアクセスなどの権限を要求した際に、ブラウザの権限要求ポップアップが正しく表示されるようになりました。

### 16.5. トラブルシューティングとデバッグの経緯

本機能の実装にあたり、複数のエラーが発生しましたが、以下の通り段階的に解決しました。

-   **ビルドエラー**:
    -   **原因**: `go.sum`の不整合、`Dockerfile`の`COPY`コマンドの順序やパスの間違い。
    -   **解決**: `Dockerfile`の記述をGoのベストプラクティスに沿った形（`go.mod`と`go.sum`を先にコピー → `go mod download` → `COPY . .` → `go mod tidy` → `go build`）に修正し、ビルドプロセスを安定化させました。
-   **実行時エラー**:
    -   **`502 Bad Gateway`**: バックエンドサービスが起動時の競合状態（DBやMinIOより先に起動してしまう）でクラッシュしていたことが原因でした。各Goサービスの`main`関数に、依存サービスへの接続リトライ処理を追加して解決しました。
    -   **`403 Forbidden`**: `minio-init`サービスがバケットの公開ポリシーを確実に設定できていなかったことが原因でした。`minio-init`の待機処理を`mc admin info`を使うより堅牢なものに変更し、ポリシー設定を確実に行うことで解決しました。
    -   **`404 Not Found`**: `index.html`がZIPのサブディレクトリ内にある場合にパスを見つけられなかったことが原因でした。`static-site-worker`が`index.html`を探索し、そのパスをDBに保存するよう修正して解決しました。
-   **権限エラー**:
    -   **`SecurityError: Invalid security origin`**: `iframe`の`sandbox`属性に`allow-same-origin`が不足していたことが原因でした。これを追加することで、カメラなどの高度なAPIへのアクセスが可能になりました。

これらの修正により、静的サイト配信機能は安定して動作するようになりました。
---
## 17. セキュリティモデルの設計思想と注意点

このプロジェクトの認証・認可は、**JWT (JSON Web Token) を `localStorage` に保存する方式**を採用しています。これは、伝統的なCookieベースのセッション管理とは異なるトレードオフを持つ、モダンなWebアプリケーションで広く採用されている設計です。

### 17.1. この設計の強み

1.  **CSRF対策の根本解決**
    -   Cookieはその性質上、ブラウザがリクエスト時に自動で送信してしまいます。これが、ユーザーの意図しないリクエストを強制的に実行させるCSRF（クロスサイト・リクエスト・フォージェリ）攻撃の温床となります。
    -   本プロジェクトでは、認証トークン（JWT）を`localStorage`に保存し、APIリクエストの都度、JavaScriptが明示的に`Authorization: Bearer <token>`ヘッダーを付与しています。
    -   これにより、**Cookieの自動送信機能に依存するCSRF攻撃は原理的に成立しません。** 複雑なCSRFトークンの管理が不要になるという大きなメリットがあります。

2.  **`iframe sandbox` によるコンテキストの遮断**
    -   ユーザーがアップロードした静的サイトは`iframe`内で表示されます。この`iframe`には`sandbox`属性が設定されており、`iframe`内のコンテンツが親ページ（Atmosidea本体）のリソース（`localStorage`やCookieなど）にアクセスすることを防ぎます。
    -   これは、たとえアップロードされた静的サイトに悪意のあるスクリプト（XSS）が含まれていたとしても、**Atmosidea本体のJWTが盗まれるのを防ぐ**ための非常に重要なセキュリティ境界となっています。

### 17.2. プロダクション運用で必ずチェックすべき「3つの注意点」

このアプローチは非常に優秀ですが、`localStorage` 採用ゆえのトレードオフが存在します。以下の3点は実装・運用時に必ず確認しておく必要があります。

#### 1. XSSが1箇所でも刺さると「JWTが一瞬で即死（完全盗難）」する

-   **リスク**: `HttpOnly`属性を持つCookieはJavaScriptから読み取れないため、XSS脆弱性があってもCookie自体を直接盗むことは困難です。しかし、`localStorage`に保存されたJWTは、**1箇所でもXSSが成功すると、`localStorage.getItem('token')`という簡単なコードで一瞬で盗まれてしまいます。**
-   **対策**:
    -   **Reactの自動エスケープ**: Reactはデフォルトで文字列をエスケープするため、基本的なXSSは防がれます。
    -   **厳格なCSP**: `Content-Security-Policy`ヘッダーで、信頼できないドメインからのスクリプト実行を禁止します。特に`script-src`ディレクティブで`unsafe-inline`や`unsafe-eval`を安易に許可しないことが重要です。
    -   **依存関係の監査**: `npm audit`などを定期的に実行し、利用しているサードパーティライブラリに既知のXSS脆弱性がないかを確認します。

#### 2. `iframe` の `sandbox` 属性の設定値（超重要）

`iframe`の`sandbox`属性に指定するフラグの組み合わせには、セキュリティ上の有名な落とし穴があります。

-   **🚨 最も危険な組み合わせ**: `sandbox="allow-scripts allow-same-origin"`
-   **理由**: この2つを**同時**に付与すると、`iframe`内のJavaScriptがサンドボックス制限を（一部）解除し、親画面の`window.parent.localStorage`などへアクセスできてしまう可能性があります。
-   **本プロジェクトでの設定**: `sandbox="allow-scripts allow-popups allow-forms allow-modals allow-same-origin"`
    -   本プロジェクトでは、`iframe`内のコンテンツ（静的サイト）がカメラAPIなど、同一オリジンであることを要求する機能を利用できるようにするため、意図的に`allow-same-origin`を許可しています。
    -   これによりサンドボックスの強度は若干低下しますが、ユーザーが自身のコンテンツをアップロードするというサービスの性質上、このリスクは許容可能と判断しています。代替案として、静的サイトを全く別のドメイン（例: `*.atmosidea-usercontent.com`）で配信する方法もありますが、実装がより複雑になります。

#### 3. JWTの「無効化（ログアウト・強制ログアウト）」の難しさ

-   **リスク**: JWTは自己完結型（ステートレス）であるため、一度発行すると有効期限が切れるまで有効です。そのため、万が一JWTが漏洩した場合や、管理者が特定のユーザーを強制的にログアウトさせたい場合に、サーバー側でそのトークンだけを即座に無効化することが困難です。
-   **対策**:
    -   **短い有効期限**: アクセストークン（JWT）の有効期限を極めて短く（例: 15分〜1時間）設定し、頻繁に再発行する。
    -   **リフレッシュトークン**: より安全なリフレッシュトークン（これは`HttpOnly` Cookieで保持することが多い）を導入し、アクセストークンの再発行を管理する。
    -   **失効リスト（Blocklist）**: Redisなどの高速なインメモリデータベースに、無効化したいJWTのIDを保存し、APIリクエストの都度そのリストをチェックする。本プロジェクトでは、この方法はまだ実装されていません。

### 17.3. まとめ

このプロジェクトのセキュリティモデルは、「**CSRFのリスクをゼロにする代わりに、XSS防御（`localStorage` の保護）に全振りする**」という、非常に明確で理にかなった方針を取っています。

`iframe`の`sandbox`属性のトレードオフを理解し、厳格なCSPを維持できている限り、個人開発から中規模サービスまで十分に耐えうる堅牢な設計と言えます。

---
## 18. 最近の主な変更点 (2026/08/08) - 静的サイトのサブドメイン分離とセキュリティ強化

このセクションでは、静的サイトの配信をメインアプリケーションと完全に分離し、セキュリティを大幅に強化するための変更点について解説します。

### 18.1. アーキテクチャの変更: サブドメインによる静的サイト配信

**課題**: 従来のパスベース (`http://localhost:3001/static-sites/{siteId}/...`) での静icサイト配信は、メインアプリケーションと同一オリジンであるため、`iframe`の`sandbox`属性に`allow-same-origin`を許可せざるを得ず、セキュリティ上の懸念が残っていました。

**解決策**: 静的サイトを専用のサブドメイン (`http://{siteId}.localhost:3001/`) で配信するように変更しました。これにより、ブラウザの**同一オリジンポリシー**が働き、メインアプリケーションと静的サイトが完全に分離され、セキュリティが大幅に向上します。

#### 18.1.1. 概念図の更新

[ユーザー] -> [ブラウザ] -> [Nginx (frontend:80)]
/api/auth/**         +-> [auth-service:8080] ----> [auth-db:5432]
/api/profile/**      +-> [profile-service:8084] -> [auth-db:5432] -> [MinIO (profile-storage:9000)] (for icons)
/api/my/**           +-> [mypage-service:8083] --> [app-db:5432]

       /api/videos/**       +-> [upload-service:8080] --> [app-db:5432]
                            |   [stream-service:8081] --> [app-db:5432]

       /api/games/**        +-> [game-upload-api:8082] -> [app-db:5432] -> [Redis:6379]
       /games/**            +-> [MinIO (game-storage:9000)]

       /api/static-sites/** +-> [static-site-upload-api:8085] -> [app-db:5432] -> [Redis:6379]
       http://{siteId}.localhost:3001/... +-> [MinIO (static-site-storage:9000)]
       http://localhost:3001/static-sites/{siteId}/... (プレビュー用) +-> [MinIO (static-site-storage:9000)]

       (非同期処理)         [game-worker] <- [Redis:6379] -> [MinIO (game-storage:9000)]
                                                          -> [app-db:5432]
       (非同期処理)         [static-site-worker] <- [Redis:6379] -> [MinIO (static-site-storage:9000)]


#### 18.1.2. サービス詳細の更新

| サービス名              | 内部ポート | 技術          | 責務                                                                                                                                                             |
| ----------------------- | ---------- | ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `frontend`              | `80`       | Nginx, React  | **UI/APIゲートウェイ**: Reactアプリを提供し、`/api`以下のリクエストを各バックエンドサービスに振り分ける。**サブドメイン (`*.localhost`) からの静的サイトリクエストも処理する。** |
| `static-site-worker`    | -          | Go            | **静的サイト非同期処理**: RedisからJobを受け取り、ZIP解凍、**ZIP内の多重ディレクトリ構造を考慮して**MinIOへのファイルアップロードを実行。**`app-db`**に接続。          |

### 18.2. Nginx設定 (`nginx.conf`) の詳細

*   **`server_name` の修正**:
    *   サブドメイン配信用 `server` ブロックの `server_name` を `~^([a-z0-9]{10})\.localhost$;` に変更。正規表現を引用符で囲むことで、Nginxの構文エラーを解消しました。
    *   `set $site_id $1;` を追加し、キャプチャしたサイトIDを変数に格納します。
*   **`rewrite` ルールの調整**:
    *   MinIOがディレクトリインデックスをサポートしないため、ディレクトリパスへのリクエスト (`/` や `/dir/`) に対して自動的に `index.html` を付加する `rewrite` ルールを追加しました。
*   **`Permissions-Policy` の強化**:
    *   サブドメイン用 `server` ブロックおよびパスベースの静的サイトアクセス用 `location` ブロックの `Permissions-Policy` を更新。
    *   `payment`, `display-capture`, `clipboard-read`, `publickey-credentials-get` など、リスクの高い権限は明示的に禁止 (`()`)。
    *   `camera`, `microphone`, `fullscreen`, `autoplay` など、機能に必要な権限は `(self)` で自己オリジンからのアクセスを許可。
    *   `pointer-lock` は `allow` 属性ではなく `sandbox` 属性で制御されるべきであるため、`Permissions-Policy` から削除し、警告を解消しました。
*   **`Content-Security-Policy` の強化**:
    *   メインアプリケーション用 `server` ブロックの `Content-Security-Policy` の `frame-src` に `http://*.localhost` および `http://*.localhost:3001` を追加。これにより、メインアプリがサブドメインの静的サイトを `iframe` で埋め込むことを許可しました。
    *   `frontend/index.html` からCSPの`<meta>`タグを削除し、CSPの一元管理をNginxで行うようにしました。

### 18.3. フロントエンドの修正

*   **`StaticSiteDetailPage.tsx`**:
    *   `iframe`の`src`を**サブドメイン形式のURL (`http://{siteId}.localhost:3001/{entry_point_path}`) に一本化**しました。これにより、`iframe`内も完全に分離されたオリジンで動作し、セキュリティが大幅に向上します。
    *   `iframe`の`sandbox`属性に`allow-same-origin`を追加しました。これは、サブドメイン分離環境下でもカメラなどの機能が正しく動作するために、`iframe`内に明確なオリジンを提供する必要があるためです。
    *   `iframe`の`allow`属性から`pointer-lock`を削除し、`sandbox`属性に`allow-pointer-lock`を追加しました。
    *   プレビューURLの表示を削除しました。
*   **`HomePage.tsx` / `MyPage.tsx`**:
    *   静的サイトのコンテンツカードのリンクを、`StaticSiteDetailPage`へのパスベースのURL (`/static-sites/{siteId}`) に戻しました。これにより、メインアプリケーション内で詳細ページに遷移し、そこでプレビューと公開URLの両方を確認できるようにします。

### 18.4. `static-site-worker` の修正

*   ZIPファイル内の多重ディレクトリ構造に対応するため、`processStaticSiteJob` 関数を修正しました。
*   `index.html` がZIP内のどの階層にあっても、そのディレクトリを基準としてMinIOのサイトID直下にファイルを展開するようにしました。
*   `entryPointPath` は常に `index.html` のファイル名のみをDBに保存するように変更しました。
*   **【重要】JavaScriptファイルの自動改変ロジックは削除しました。** ユーザーがアップロードしたファイルを意図せず改変することはリスクが高いため、この機能は実装しない方針としました。カメラ機能を利用する静的サイトをアップロードする際は、ユーザー自身が`script.js`を「ボタンクリックでカメラが起動する」ように修正する必要があります。

### 18.5. 開発用バッチスクリプトの追加

*   **`open_ports.bat`**: Windowsファイアウォールでポート `3001` と `8080` を開放します。
*   **`close_ports.bat`**: `open_ports.bat` で開いたポートを閉じます。
*   **`check_ports_closed.bat`**: ポートが `localhost` のみにバインドされているかを確認し、意図せず外部に公開されていないかをチェックします。

### 18.6. トラブルシューティングと解決策

*   **Nginx構文エラー (`directive "server_name" is not terminated by ";"` など)**:
    *   `server_name` の正規表現にバックスラッシュ (`\`) が含まれる場合、文字列全体を引用符 (`""` または `''`) で囲む必要があるというNginxの構文ルールに従い修正しました。
    *   `set $site_id $1;` を `server_name` の直後に配置し、キャプチャグループを番号付き (`$1`) にすることで、より互換性の高い設定としました。
*   **CSPエラー (`Framing '...' violates...`)**:
    *   `frontend/index.html` に記述されていたCSPの`<meta>`タグを削除し、Nginxで設定したCSPが正しく適用されるようにしました。
    *   Nginxのメインアプリケーション用 `server` ブロックの `Content-Security-Policy` ヘッダーに `frame-src 'self' http://*.localhost http://*.localhost:3001;` を追加し、クロスオリジンでの `iframe` 埋め込みを許可しました。
*   **`Permissions-Policy` 警告 (`Unrecognized feature: 'pointer-lock'`)**:
    *   `nginx.conf` の `Permissions-Policy` ヘッダーから `pointer-lock` を削除しました。`pointer-lock` は `allow` 属性ではなく `sandbox` 属性で制御されるべきであるためです。
*   **カメラアクセスエラー (`SecurityError: Invalid security origin`)**:
    *   `StaticSiteDetailPage.tsx` の `iframe` の `sandbox` 属性に `allow-same-origin` を追加しました。サブドメイン分離環境下でも、カメラなどの機能が正しく動作するために `iframe` 内に明確なオリジンを提供する必要があるためです。
*   **カメラアクセスエラー (`NotAllowedError: Permission denied`)**:
    *   これはブラウザのセキュリティ仕様による挙動であり、以下の手順でユーザー側での対応が必要です。
        1.  **ブラウザの個別ドメイン設定を「許可」に変更する**: `http://{siteId}.localhost:3001` を直接開き、URLバーの設定アイコンからカメラ権限を「許可」に設定。
        2.  **OS（Windows / Mac）のカメラプライバシー設定を確認する**: OSレベルでChromeのカメラ使用が許可されているか確認。
        3.  **Chromeの安全扱いフラグ（`chrome://flags`）を適用する**: `chrome://flags/#unsafely-treat-insecure-origin-as-secure` をEnabledにし、`http://localhost:3001, http://*.localhost:3001, http://*.localhost` を追加して再起動。
        4.  **ユーザー操作によるカメラ起動**: `script.js` 内で、ページ読み込み時に自動でカメラを起動するのではなく、**ユーザーがボタンをクリックするなどの操作をトリガーとしてカメラを起動する**ように実装する必要があります。

これらの修正とトラブルシューティングにより、静的サイトのサブドメイン分離とセキュリティ強化が実現され、カメラ機能も正しく動作する環境が整いました。                                                                  -> [app-db:5432]

### 18.7. Goマイクロサービス（モノリポ）における依存関係・Dockerビルドエラーと解決策

*   **`go.mod` 初期状態における依存関係欠落と依存構造（直接/間接依存）の理解**:
    *   **課題**: 一部のサービス（`upload-service` など）の `go.mod` が `module` 名と `go` バージョン宣言のみの初期状態（たった3行）であり、プログラムの動作に必要な外部ライブラリの記録（`require`）や、改ざん防止・バージョン固定用のチェックサムファイル（`go.sum`）が存在しませんでした。
    *   **背景と構造**: Go言語では、コード内で直に呼び出している「直接依存（`import` しているライブラリ）」と、そのライブラリが内部で勝手に使っている「間接依存（`// indirect` コメントが付く孫ライブラリ）」の2種類が存在します。正常なサービス（`profile-service` など）の構成と比較・分析し、`go mod tidy` コマンドを実行してプロジェクト全体でこの依存関係を正確に記録・整理する必要性を明らかにしました。

*   **モノリポ共通パッケージ（`shared`）の参照不可による GitHub 認証エラー (`exit status 128`)**:
    *   **課題**: 各マイクロサービス（`upload-service`, `static-site-worker`, `game-worker` 等）から、チーム共有の共通モジュール（`github.com/atmosidea/shared`）を呼び出そうとした際、Go言語が「このプログラムはインターネット（GitHub）上にある非公開リポジトリ（プライベートリポジトリ）に取りに行かなければならない」と勘違いしてアクセスを試みました。その結果、Dockerビルドなどの画面が出ない自動処理（非対話型環境）の中でユーザー名やパスワード（SSHキー）を入力できず、`fatal: could not read Username for 'https://github.com': terminal prompts disabled` という認証エラー（exit status 128）で処理が停止していました。
    *   **解決策**: 該当するすべてのサービスの `go.mod` ファイルの最末尾に `replace github.com/atmosidea/shared => ../shared` という「置換（replace）指示」を明示的に追記しました。これにより、インターネットへの通信を一切発生させず、同じパソコン内にある隣の `../shared` フォルダを直接読み込ませるように設定を修正しました。

*   **Docker ビルドコンテキストの誤りと相対パス参照失敗 (`replacement directory ../shared does not exist`)**:
    *   **課題**: 従来は `cd upload-service` のように各サービスフォルダの中に移動してから `docker build .` を実行したり、`Dockerfile` 内で該当サービス単体のフォルダだけをコンテナ内部にコピー（`COPY . .`）していました。この状態だと、コンテナの中から見ると「自分自身のフォルダ」しか見えず、親階層にある `../shared` フォルダが存在しないため、`go.mod` で設定した `replace` 指示が「指定された `../shared` ディレクトリが存在しない（does not exist）」というパス切れエラーを起こしていました。
    *   **解決策**: Dockerのビルド対象範囲（ビルドコンテキスト）を、常にプロジェクト全体の最ルート階層（`Atomosidea` フォルダ直下）に指定（`docker build -f <サービス>/Dockerfile .`）するように変更しました。さらに `Dockerfile` の中で `COPY . .` を行なってプロジェクト全体（`shared` 含む）をコンテナ内に一度まるごとコピーした上で、`WORKDIR /app/<サービス名>` へ移動してビルドを行う安全な設計に一括改修しました。

*   **Dockerfile 内での不適切な `RUN go mod tidy` 実行とビルド安定化の阻害**:
    *   **課題**: 以前の `Dockerfile` の内部に `RUN go mod tidy`（コンテナの中でプログラムに必要なライブラリを都度検索・整理する命令）が書かれていました。コンテナの中にはGitHubのログイン情報（認証情報）がないため、ビルドのたびに外部通信が発生して処理が遅くなるばかりか、認証エラーや通信エラーを引き起こしてビルドが失敗する原因になっていました。
    *   **解決策**: `Dockerfile` から `RUN go mod tidy` という命令を完全に削除しました。ライブラリの整理（`go.mod` / `go.sum` の完成）はあらかじめローカル開発環境（ホスト側）で事前に終わらせておき、コンテナ内では確定した構成ファイルを読み込んで高速にダウンロードする `RUN go mod download` だけを行う運用（マルチステージビルド構成）に統一しました。

*   **全 Go サービスへの一括設定適用に伴う安全対策と `shared` 依存フィルタリング**:
    *   **課題**: プロジェクト内には様々なサービス（Go言語以外のフロントエンド等含む）が存在するため、無差別に `Dockerfile` を書き換えると正常に動いている他のサービスを壊してしまうリスクがありました。
    *   **解決策**: `go.mod` の中身を自動解析し、`shared` パッケージを参照している（または `replace` 設定を必要としている）Go言語のサービスだけを自動的に検出して上書きする条件分岐付きの PowerShell スクリプトを作成・適用しました。これにより、無関係な独立サービスやフロントエンドのコードには一切手を加えず、必要な部分だけを安全に一括標準化しました。

*   **モグラ叩き的なエラー連鎖を防ぐ全自動修正・同期スクリプトの導入**:
    *   **課題**: 1つのサービスのエラーを直しても、別のサービス（`static-site-worker` や `game-worker` など）で全く同じ設定漏れによるエラーが順番に発生し、手動修正では果てしないモグラ叩き状態になっていました。
    *   **解決策**: 以下の3ステップを全自動で一括実行する統合処理 PowerShell スクリプトを作成し、プロジェクト全体に対して実行・完工しました。
        1.  全Goサービスの `go.mod` ファイルを自動巡回し、`replace github.com/atmosidea/shared => ../shared` の記述が無ければ自動追記する。
        2.  全Goサービスの `Dockerfile` を、`../shared` を正しく認識できる安全なマルチステージビルド構成へ一括書き換える。
        3.  プロジェクトルート全体を一時的なDockerコンテナにマウント（接続）して起動し、全サービスフォルダ上で `go mod tidy` を一気に連続実行して `go.sum` を生成・完全同期させる。

*   **`alpine:latest` 実行環境における SSL 通信・タイムゾーン対応**:
    *   **課題**: Dockerビルドの最終段階で使用する超軽量な実行環境イメージ（`alpine:latest`）は、不要なファイルが徹底的に削られているため、標準では「インターネットの安全性を証明するルート証明書（ca-certificates）」が入っていません。このままだと、ビルド自体は成功しても、いざアプリを起動してデータベース（DB）やAmazon S3/MinIO、外部API等とHTTPS通信（暗号化通信）を行おうとした際に「証明書が検証できない」としてアプリが強制終了（クラッシュ）する潜在リスクがありました。
    *   **解決策**: `Dockerfile` の最終ステージ（Final Stage）に `RUN apk --no-cache add ca-certificates tzdata` という命令を追加しました。これにより、SSL/TLS暗号化通信に必要なルート証明書と、日本時間などのタイムゾーン設定をコンテナ内に標準装備させました。

*   **成果物の保存と今後の開発運用の手放し化（完全自動化の実現）**:
    *   **成果と運用**: 一連の修正スクリプトと設定見直しにより、ローカル環境上の `go.mod` および `go.sum` が完璧に同期され、Dockerによるコンテナビルドの手順・パイプラインが完全に固定・自動化されました。
    *   **手放し化**: これらの変更結果（更新された `go.mod`, `go.sum`, `Dockerfile`）を Git にバージョン管理として保存（`git add . && git commit`）しておくことで、次回以降や他の開発者の環境では、単に `.\first-setup.ps1` や `docker compose up --build` を1回実行するだけで、手動の修正や複雑なコマンドを一切叩くことなく、全マイクロサービスがエラーなしで自動ビルド・起動する完全手放しの開発環境を確立しました。

---
## 19. 編集内容の概要 (EDIT_SUMMARY.md)

このドキュメントは、Atmosideaプロジェクトの機能改善とバグ修正のために行われた、一連の編集作業をまとめたものです。
主な目的は、ユーザーがアップロードするコンテンツ（ゲーム、静的サイト、動画）のセキュリティスキャンを導入し、その処理状況をフロントエンドで適切に表示すること、および関連するバグを修正することでした。

### 19.1. SFSP (Secure File Scanning Platform) の構築と統合

ユーザーがアップロードするファイルのセキュリティを確保するため、新しいマイクロサービス群「SFSP」を導入し、既存のアップロードフローに統合しました。

#### 19.1.1. SFSPアーキテクチャ

*   **`sfsp-api`**: ファイルを受け取り、スキャンジョブを作成するAPIサービス。
*   **`sfsp-worker`**: Redisキューからジョブを取得し、サンドボックス内でスキャンを実行するワーカーサービス。
*   **`sfsp-db`**: スキャンジョブと結果を保存するPostgreSQLデータベース。
*   **`sfsp-minio`**: スキャン対象ファイル（`raw-files`）とスキャン済みファイル（`clean-files`, `quarantine`）を保存するオブジェクトストレージ。
*   **`sfsp-clamav-client`**: ClamAVを実行するためのDockerイメージ（サンドボックス内で動的に起動）。
*   **`sfsp-yara-client`**: YARAを実行するためのDockerイメージ（サンドボックス内で動的に起動）。
*   **`redis`**: スキャンジョブキュー (`sfsp:scan_queue`) と完了イベントキュー (`sfsp:completed_jobs`) を提供。

#### 19.1.2. 統合されたアップロードフロー

1.  **各アップロードAPI (`game-upload-api`, `static-site-upload-api`, `upload-service`)**:
    *   各アップロードAPIは、ファイルを受け取ると、まずSFSP (`sfsp-api`) にファイルを転送します。
    *   SFSPからジョブIDと初期ステータスを受け取り、各コンテンツのDBテーブル（`games`, `static_sites`, `videos`）に `sfsp_job_id` を保存し、ステータスを `scanning` に設定します。
    *   SFSPが重複ファイルを検知し、`clean` ステータスを返した場合、各アップロードAPIは `sfsp:completed_jobs` キューに完了イベントを再発行し、ワーカーを直接トリガーします。

2.  **`sfsp-worker`**:
    *   Redisの`sfsp:scan_queue`からジョブIDを取得し、スキャンを実行します。
    *   スキャン完了後、結果（`clean`, `malicious`など）を `sfsp:completed_jobs` キューに発行します。
    *   **Unity WebGLビルドの検証ロジックを改善**: ZIP展開後、Unity WebGLルートと静的サイトルートの両方が見つかる場合に `invalid` と判定される問題を修正。Unity WebGLルートを優先的に探索するように変更しました。

3.  **各ワーカーサービス (`game-worker`, `static-site-worker`, `upload-service`)**:
    *   `sfsp:completed_jobs` キューをリッスンします。
    *   対応するジョブの完了イベントを受け取ると、`final_status` が `clean` であることを確認し、それぞれのコンテンツ処理（HLS変換、ZIP展開など）を開始します。
    *   処理の各段階でDBの `processing_details` フィールドを更新し、フロントエンドに詳細な進捗を伝えます。
    *   `game-worker` が `sfsp_job_id` でゲームを検索する際に、`ORDER BY created_at DESC LIMIT 1` を追加し、常に最新のゲームレコードを処理するように修正しました。

### 19.2. ゲームホスティングのセキュリティ強化とURL形式の変更

ゲームコンテンツをメインドメインから隔離し、Cookieなどのセッション情報を保護するため、ゲームのホスティング方法をサブドメイン形式に変更しました。

*   **URL形式の変更**:
    *   **変更前**: `http://<メインドメイン>/games/<game_id>/index.html`
    *   **変更後**: `http://<game_id>.<APP_URLのドメイン>:<APP_URLのポート>/index.html`
*   **`game-worker`の修正**:
    *   ゲーム公開時に、新しいサブドメイン形式のURLを生成し、`games`テーブルの`game_url`に保存するように修正しました。
    *   `APP_URL`環境変数を読み込み、URL生成に使用するように変更しました。
*   **Nginx (`frontend/nginx.conf`) の修正**:
    *   `~^(?<subdomain>[a-z0-9]{10})\.localhost$` のようなワイルドカードサブドメインへのリクエストを受け付ける`server`ブロックを修正しました。
    *   リクエストされたサブドメイン（＝`game_id`）を元に、リクエストを`game-storage` (MinIO) の適切なパスにプロキシするように設定しました。
    *   ルート (`/`) へのアクセスを自動的に `/index.html` に書き換えるルールを追加し、`NoSuchKey`エラーを防ぎました。

### 19.3. フロントエンドのUX改善

ファイルのスキャンと処理中、ユーザーに進捗状況を明確に伝えるためのUI改善を行いました。

*   **対象ページ**: `GameDetailPage.tsx`, `StaticSiteDetailPage.tsx`, `VideoDetailPage.tsx`
*   **実装内容**:
    *   各詳細ページは、コンテンツの `status` が `public` になるまで、APIを5秒ごとにポーリングします。
    *   ポーリング中、`loading` または `status` が `scanning` もしくは `processing` の間は、以下のメッセージとローディングインジケーターを表示します。
        *   `status: 'scanning'`: 「セキュリティスキャンを実行中...」
        *   `status: 'processing'`: 「ファイルを準備中...」（DBの`processing_details`があればそれを表示）
    *   `status` が `public` になると、ポーリングを停止し、コンテンツ（ゲーム、サイト、動画）を表示します。
    *   `status` が `error`, `rejected`, `invalid` などになった場合は、エラーメッセージを表示します。
*   **アップロードページ (`UploadGamePage.tsx`, `UploadStaticSitePage.tsx`, `UploadVideoPage.tsx`) の修正**:
    *   アップロード成功後、新しく作成されたコンテンツの詳細ページにリダイレクトする際に、初期ステータス (`initialStatus`) を渡すように修正しました。

### 19.4. 主なバグ修正とデバッグの過程

*   **ビルドエラー**:
    *   Goの`imported and not used`エラーを修正 (`upload-service/main.go`から`archive/zip`のimportを削除)。
    *   TypeScriptの`Cannot find namespace 'NodeJS'`エラーを修正（`GameDetailPage.tsx`で`NodeJS.Timeout`を`number`に変更）。
*   **実行時エラー**:
    *   **DB/Redis接続エラー**: サービス起動時に接続をリトライするロジックを導入。
    *   **ClamAVサンドボックスエラー**:
        *   `No supported database files found`: `tmpfs`のマウントパスを修正し、ウイルス定義ファイルが読み込まれるようにしました。
        *   `Read-only file system`: `chown`操作を許可するため、`ReadonlyRootfs`を`false`に設定しました。
        *   `exitCode=137` (OOM Killer): サンドボックスのメモリ制限を512MBから2GBに増強しました。
    *   **MinIO `AccessDenied` / `NoSuchKey`**:
        *   `game-worker`とNginxに、バケットの存在確認と公開ポリシーを確実に設定する処理を追加しました。
        *   Nginxのサブドメインプロキシで、ルートパス`/`を`/index.html`に書き換えるルールを追加し、`NoSuchKey`エラーを防ぎました。
    *   **Content-Type/Encodingの問題**:
        *   `game-worker`がMinIOにファイルをアップロードする際に、`.br`や`.wasm`などの拡張子に応じて適切な`Content-Type`と`Content-Encoding`を設定するように修正しました。
    *   **iframeのURLポート番号問題**: `game-worker`が生成するURLに`APP_URL`環境変数から取得したポート番号を含めるように修正しました。
*   **アプリケーションロジックのバグ**:
    *   **SFSP APIのHTTPステータス判定**: `game-upload-api`がSFSPからの`200 OK`レスポンスをエラーとして扱う問題を修正し、2xx台のステータスコードを成功として扱うように変更しました。
    *   **重複ファイルアップロード時の無限ロード**: `sfsp-api`が重複ファイルを検知した際に、`game-worker`に再処理イベントを投入するロジックを追加して解消しました。
    *   **複数ゲームの`sfsp_job_id`重複**: `game-worker`がDBからゲームを検索する際に、`ORDER BY created_at DESC LIMIT 1`を追加し、常に最新のレコードを処理するように修正しました。

このドキュメントは、将来のメンテナンスや機能追加の際に、システムの全体像と重要な注意点を把握するための一助となることを目的としています。

## 20. DBマイグレーションに伴う `user_id` (`VARCHAR`) 型不整合の解消

データベースの `user_id` カラムを `INTEGER` から `VARCHAR(255)` (UUID v7 対応) へ移行したことに伴い、各マイクロサービスで発生していたランタイムエラーおよび型スキャン失敗（`Scan` エラー）を解消しました。

#### 20.1. 発生していた主な障害・エラー
- **`game-upload-api` ビルドエラー:**  
  `sfspResponse.Close` 呼び出し時の型不整合 (`bool is not a function`)。
- **`stream-service` 404 (Not Found) エラー:**  
  動画詳細取得 API (`/api/videos/:id`) 実行時、DB の `uploader_id` (VARCHAR) を Go 構造体の `int` 型フィールドへスキャンしようとして `can't scan into dest...` エラーが発生し、フォールバックで 404 を返却。

#### 20.2. 各サービスの修正内容

##### ① `game-upload-api` (`main.go`)
- **レスポンス破棄処理の修正:**  
  `defer sfspResponse.Close()` を **`defer sfspResponse.Body.Close()`** に修正してコンパイルエラーを解消。
- **型変更対応:**  
  `Game` 構造体の `UploaderID` を `string` に統一。JWT ミドルウェアおよび各ハンドラー (`upload`, `update`, `adjust`, `delete`) 内の `userID` / `uploaderID` 変数を `string` 型へ変更。

##### ② `stream-service` (`main.go`)
- **型変更対応:**  
  `Video` 構造体の `UploaderID` フィールドを `int` から **`string`** へ変更。
- **スキャン処理の修正:**  
  `listVideosHandler` および `videoDetailsHandler` において、`uploader_id` カラムを `string` 型変数へ正常にスキャンできるよう修正。

#### 20.3. 動作確認結果
1. **コンテナ再ビルド＆起動:**  
   `game-upload-api` および `stream-service` のビルドが正常完了。
2. **API 正常性確認:**  
   フロントエンドからの `/api/videos/:id` 呼び出しに対して DB スキャンエラーが解消され、`200 OK` で動画メタデータが正常返却されることを確認。