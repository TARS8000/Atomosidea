# Monitoring Service（監視ダッシュボード）

開発者向けにシステム全体の状態（コンテナ・リソース・MinIO・ログ）を一元的に監視・操作するサービス。Dockerソケットに直接接続し、各コンテナの稼働状況、CPU・メモリ使用率、アクティブユーザー数、MinIOオブジェクトの閲覧・アップロード・削除、リアルタイムログのストリーミングを提供する。

- **アクセス:** `http://127.0.0.1:8090`（ホストのループバックのみに公開。HTTP Basic認証あり）
- **技術スタック:** Go（バックエンド）+ React/Vite（フロントエンド）+ Nginx（配信・プロキシ）
- **依存:** 既存の`atmosidea-network`および`sfsp-isolated-net`ネットワークに接続（どちらも`external`のため、先にメインのスタックを起動してネットワークを作成しておく）

## アーキテクチャ

3コンテナで構成され、Nginxがフロント（静的ファイル）とAPI/WebSocketを振り分ける。

| コンテナ | 画像 / ビルド | 役割 |
|---|---|---|
| `mon-nginx` | `nginx:alpine` | `127.0.0.1:8090`→80。静的フロントを`auth_request`による認証付きで配信し、`/api/`と`/ws`を`mon-backend:8080`へプロキシ（WebSocket対応） |
| `mon-backend` | `golang:1.25-alpine`（マルチステージビルド） | Dockerソケット経由でコンテナ操作・統計・MinIO操作・WebSocketログストリーミングを提供。全ルートでHost/Origin検証とHTTP Basic認証を行う。内部ポート8080 |
| `mon-frontend`（コンテナ名`mon-frontend-builder`） | `node:20-alpine` | `npm run build`で`./frontend/dist`へ出力して終了（ビルド専用、稼働しない） |

### 依存関係

- `mon-nginx` は `mon-backend`・`mon-frontend` の後に起動する（`depends_on`は起動順のみで、ビルド完了までは待たない）。ビルド成果物（`./frontend/dist`）はホストのバインドマウントなので、ビルドが終われば`/var/www/html`から配信される。
- `mon-backend` は `/var/run/docker.sock`をroでマウントし、`atmosidea-network`・`sfsp-isolated-net`に接続して既存コンテナへアクセスする。各コンテナのネットワーク接続情報・ヘルス状態・稼働時間・再起動回数は`ContainerInspect`で動的に取得する。

## セットアップ

`monitoring-service`ディレクトリ内で以下を実行。`http://localhost:8090`にアクセス。

```bash
docker-compose --env-file ../.env -f docker-compose.monitoring.yml up -d --build
```

- **ログイン（HTTP Basic 認証）**: ユーザー名とパスワードはプロジェクトルートの `.env` の `MONITOR_USERNAME` / `MONITOR_PASSWORD`。`mon-backend` が `/api`・`/ws` を含む全ルートで定数時間比較により検証し、UI（静的ファイル）は `mon-nginx` の `auth_request`（`/auth/check`）で同じ資格情報を要求する。どちらかが未設定だと全リクエストを拒否する（fail closed）。
- `--env-file ../.env` を付けないと資格情報が渡らず、`/api`・`/ws` は 503 を返す（UI は `auth_request` が 503 を受けるため mon-nginx が 500 を返す）。`scripts/start.bat` は自動で付ける。
- **Host / Origin の検証**: `Host` は `localhost` / `127.0.0.1` / `::1` のみ許可し（DNSリバインディング対策）、`Origin` がある場合は同一ホストでなければ 403（WebSocket の `CheckOrigin` も同じ）。環境変数 `MONITORING_ALLOWED_HOSTS`（カンマ区切り、空白と空要素は無視）で許可ホストを追加できる。`docker-compose.monitoring.yml` が `.env` の同名の値を `mon-backend` に渡す（未設定なら追加なし）。

`scripts/` に起動・停止・クリーンアップ用batが用意されている。

| スクリプト | 説明 |
|---|---|
| `start.bat` | ルートの`.env`を`--env-file`で渡し、キャッシュなしでビルド後、`up -d`で起動 |
| `stop.bat` | `down`で停止（`scripts/`をカレントにして実行） |
| `clean.bat` | `down --volumes --remove-orphans`でボリューム込みの完全クリーンアップ（`scripts/`をカレントにして実行） |

## API（mon-backend、内部ポート8080 / Nginx経由 `/api`）

| メソッド | エンドポイント | 説明 |
|---|---|---|
| `GET` | `/api/containers` | 停止中を含む全コンテナ一覧（ID・名前・イメージ・状態・作成時刻・ヘルス・起動時刻・稼働時間・再起動回数・ネットワーク） |
| `GET` | `/api/containers/stats` | 全runningコンテナのCPU％・メモリ使用量（並行取得） |
| `GET` | `/api/topology` | 停止中を含む全コンテナのネットワーク接続情報（ID・名前・接続ネットワーク） |
| `GET` | `/api/volumes` | Dockerボリューム一覧（名前・ドライバ・マウントポイント・ラベル） |
| `POST` | `/api/containers/restart/{name}` | 指定コンテナの再起動（stopタイムアウト10秒） |
| `GET` | `/api/connections/count` | `atmosidea-frontend`の直近5分のログからIPを正規表現抽出し、ユニークIP数をカウント（アクティブユーザー数推定） |
| `GET` | `/api/minio/list/{containerName}` | MinIOのバケット/フォルダ/オブジェクト一覧（クエリ: `bucket`、`prefix`） |
| `POST` | `/api/minio/upload/{containerName}` | MinIOバケットへファイルアップロード（multipart、key: `file`、クエリ: `bucket`、`prefix`） |
| `DELETE` | `/api/minio/delete/{containerName}` | MinIOからオブジェクト削除（クエリ: `bucket`、`key`） |
| `GET` | `/ws/logs` / `/ws/logs/{id}` | コンテナログをWebSocketでリアルタイムストリーミング（クエリ: `container`。直近100行から追従） |
| `GET` / `HEAD` | `/auth/check` | mon-nginxの`auth_request`専用（内部location `/_monitor_auth`から呼ばれる）。認証済みなら204 |

### 使用ライブラリ（`backend/go.mod`）

- `github.com/docker/docker` — Dockerクライアント（コンテナ一覧・統計・再起動・ログ）
- `github.com/gorilla/mux` — ルーティング
- `github.com/gorilla/websocket` — WebSocketストリーミング
- `github.com/minio/minio-go/v7` — MinIOオブジェクト操作

## 監視ロジック（フロントエンド）

フロントエンド（`frontend/src/App.jsx`）がエラー検出・可視化を担う。

### アクティブユーザー数（`/api/connections/count`）

`atmosidea-frontend`コンテナの直近5分間のログを解析し、IPアドレス正規表現で抽出したユニークIP数をアクティブユーザー数として推定する（フロントエンドは10秒ごとに更新）。ログベースの簡易的な推定であり、正確なセッション計測ではない。

### エラー検出（10秒間に5件）

エラー表示はフロントエンドでの判定（バックエンドは各行を個別にストリーミングするだけ）。

1. WebSocketで受信した各行を正規表現`/(error|fatal|fail|exception|stderr|panic)/i`で判定（`App.jsx:540`）
2. マッチした時刻を`errorLoopState[コンテナ名]`に保持。**直近5件だけ保持**（`.slice(-5)`、`App.jsx:543`）
3. **errorログが5件以上** かつ **最初のerrorから10秒以内**（`now - timestamps[0] < 10000`）を満たすとエラーと判定（`App.jsx:573`）

条件を満たすと、対象コンテナのノード枠と接続線が赤（rose）で表示される。検出後は `ERROR_HIGHLIGHT_MS`（10秒）だけ赤表示を保持し、その後リセットされて再度5件/10秒で再検出できる（`App.jsx:580-595`）。

### 可視化

- **System Activity（LPS）**: 直近2秒間のログ受信数をグラフ表示
- **Active Users**: `/api/connections/count`の推定ユーザー数（5分間）
- **System Load**: CPUは全runningコンテナの平均使用率、メモリは全コンテナの合計使用量÷合計上限（いずれも90%超で赤、70%超で amber、それ以下で green）
- **トポロジー図**: コンテナを5グループ（UI / API / Workers / DB / Storage。該当なしはUnknown）に分類し、縦軸=サービス種別の6行（Frontend / API / Upload / Worker / DB/Cache / Storage）、横軸=名前の接頭辞によるカテゴリ列のグリッドに配置して接続線を付与（`upload-api`は`Upload`行、Unknownは`Storage`行）。接続線はログ出力時に共有ネットワークの色でパケットが流れるアニメーションになり、エラー時に赤で表示

### コンテナ分類（`App.jsx`）

各コンテナ名を接尾辞で分類し、レイヤーへ割り当てる。`getContainerGroup`（`App.jsx:317`）:

| レイヤー | 分類対象 |
|---|---|
| UI | `frontend` / `frontend-builder` |
| API | `auth-service` / `profile-service` / `team-service` / `mypage-service` |
| Workers | `sfsp-api` / `sfsp-worker` / `video-worker` / `game-worker` / `static-site-worker` / `upload-api` / `clamav` / `yara` |
| DB | `auth-db` / `app-db` / `profile-db` / `team-db` / `sfsp-db` / `redis` |
| Storage | `profile-storage` / `game-storage` / `static-site-storage` / `video-storage` / `minio` / `storage` |
| Unknown | 上記以外 |

監視対象外（トポロジー・レイアウトから除外）: `mon-backend` / `mon-nginx` / `mon-frontend-builder`、`mon-` プレフィックスのコンテナ、`-builder` で終わるコンテナ（`sfsp-clamav-client-builder` など）、およびプロジェクト外の `ba_halo_db` / `ba_halo_worker` / `open-webui`。

### 接続トポロジー（ネットワーク共有から動的生成）

`/api/topology`で取得した各コンテナの接続ネットワークから、**ネットワークを1つ以上共有し、かつ異なるレイヤーに属するコンテナ間**に接続線を生成する。`App.jsx:727`の`connections`が該当。

- **起点（UI / API）→ 接続先（Workers / DB / Storage / Unknown）** の方向に限定し、レイヤーを跨ぐ接続のみを描画する。
- 接続線の色は共有ネットワークで色分け（`networkColor`、`App.jsx:373`）: `sfsp`系=amber、`db`系=green、その他=blue（ネットワーク情報が無い場合はcyan）。
- これにより、`docker-compose.yml`のハードコードされたトポロジーに依存せず、実行中のコンテナの実際のネットワーク接続から可視化される。

### コンテナカード情報

各ノードカードに以下の情報を追加表示:

- **ヘルスステータス**: `healthy`/`unhealthy`の場合のみドット表示（`unhealthy`は赤点滅）。`App.jsx:204`
- **再起動回数**: `restartCount > 0` のみ `↻ N restarts` と表示（`App.jsx:218`）
- **稼働時間**: 起動時刻（`StartedAt`）から現在までの経過時間（秒に丸めた表記。例: `1h2m3s`、`App.jsx:219`）
- **異常状態**（`unhealthy` またはエラー検出中）はカード枠を赤（rose）で強調（`App.jsx:167`）。停止中のコンテナは枠が暗く半透明になる

### ボリューム一覧パネル

右上の「Volumes」ボタンでDockerボリューム一覧（名前・ドライバ・マウントポイント）を表示するモーダルを開く。`/api/volumes`から取得し、`VolumesModal`（`App.jsx:381`）で描画する。

## MinIO操作

`getMinioClient`（`main.go:605`）は、対象コンテナの環境変数から認証情報を取得する。順序は `MINIO_ROOT_USER`→`MINIO_ACCESS_KEY`、`MINIO_ROOT_PASSWORD`→`MINIO_SECRET_KEY`。エンドポイントは`{containerName}:9000`（TLSなし）。MinIOは`atmosidea-minio`の1コンテナのみで、全バケットをここから操作する（旧構成の`sfsp-minio`等は存在しない）。root資格情報で操作するため、アップロード・削除はIAMポリシーの制限を受けない。フロントエンドは、コンテナ名・イメージ名に`minio` / `storage`を含む、またはログに`min.io`が出るコンテナでStorageタブを表示する。

## ディレクトリ構成

```
monitoring-service/
├── backend/                    # Goバックエンド
│   ├── Dockerfile              # golang:1.25-alpine (multi-stage build)
│   ├── go.mod
│   └── main.go                 # 全API・WebSocket・MinIOロジック
├── frontend/                   # React/Viteフロントエンド
│   ├── Dockerfile              # node:20-alpine、`npm run build`でdist出力
│   ├── package.json
│   └── src/App.jsx             # 監視ロジック・可視化UI
├── docker-compose.monitoring.yml
├── nginx.conf                  # /api/ と /ws を mon-backend へプロキシ、UIは auth_request で保護
└── scripts/                    # start.bat / stop.bat / clean.bat
```

## 補足

- 監視サービスは任意サービス。メインのフロントエンド（`http://localhost:3001`）とは独立して起動する（ただし、メインのスタックが作成するネットワークに接続する）。
- Dockerソケットへの直接アクセスとMinIOへの直接書き込みを含むため、開発・運用環境向けの機能。ホストのループバックのみに公開し、HTTP Basic認証とHost/Origin検証で保護しているが、インターネットへの公開は想定していない。
- エラー検出閾値（10秒間に5件）とフロントエンドのerrorキーワード一覧は`frontend/src/App.jsx`に集約されている（`backend/main.go`の`errorKeywords`は`isError`を計算するが、WebSocketにはメッセージ本文のみを送るため、画面の判定には使われない）。