# Monitoring Service（監視ダッシュボード）

開発者向けにシステム全体の状態（コンテナ・リソース・MinIO・ログ）を一元的に監視・操作するサービス。Dockerソケットに直接接続し、各コンテナの稼働状況、CPU・メモリ使用率、アクティブユーザー数、MinIOオブジェクトの閲覧・アップロード・削除、リアルタイムログのストリーミングを提供する。

- **アクセス:** `http://localhost:8090`
- **技術スタック:** Go（バックエンド）+ React/Vite（フロントエンド）+ Nginx（配信・プロキシ）
- **依存:** 既存の`atmosidea-network`および`sfsp-isolated-net`ネットワークに接続

## アーキテクチャ

3コンテナで構成され、Nginxがフロント（静态ファイル）とAPI/WebSocketを振り分ける。

| コンテナ | 画像 / ビルド | 役割 |
|---|---|---|
| `mon-nginx` | `nginx:alpine` | 8090→80。静态フロントを配信し`/api/`と`/ws`を`mon-backend:8080`へプロキシ（WebSocket対応） |
| `mon-backend` | `golang:1.25-alpine`（マルチステージビルド） | Dockerソケット経由でコンテナ操作・統計・MinIO操作・WebSocketログストリーミングを提供。内部ポート8080 |
| `mon-frontend` | `node:20-alpine` | `npm run build`で`./frontend/dist`へ出力して終了（ビルド専用、稼働しない） |

### 依存関係

- `mon-nginx` は `mon-frontend` のビルド完了を `depends_on` で待ち、ビルド成果物（`./frontend/dist`）を`/var/www/html`から読み込む。
- `mon-backend` は `/var/run/docker.sock`をroでマウントし、`atmosidea-network`・`sfsp-isolated-net`に接続して既存コンテナへアクセスする。各コンテナのネットワーク接続情報・ヘルス状態・稼働時間・再起動回数は`ContainerInspect`で動的に取得する。

## セットアップ

`monitoring-service`ディレクトリ内で以下を実行。`http://localhost:8090`にアクセス。

```bash
docker-compose -f docker-compose.monitoring.yml up -d --build
```

`scripts/` に起動・停止・クリーンアップ用batが用意されている。

| スクリプト | 説明 |
|---|---|
| `start.bat` | キャッシュなしでビルド後、`up -d`で起動 |
| `stop.bat` | `down`で停止 |
| `clean.bat` | `down --volumes --remove-orphans`でボリューム込みの完全クリーンアップ |

## API（mon-backend、内部ポート8080 / Nginx経由 `/api`）

| メソッド | エンドポイント | 説明 |
|---|---|---|
| `GET` | `/api/containers` | 稼働中の全コンテナ一覧（ID・名前・イメージ・状態・作成時刻・ヘルス・起動時刻・稼働時間・再起動回数・ネットワーク） |
| `GET` | `/api/containers/stats` | 全runningコンテナのCPU％・メモリ使用量（並行取得） |
| `GET` | `/api/topology` | 全コンテナのネットワーク接続情報（ID・名前・接続ネットワーク） |
| `GET` | `/api/volumes` | Dockerボリューム一覧（名前・ドライバ・マウントポイント・ラベル） |
| `POST` | `/api/containers/restart/{name}` | 指定コンテナの再起動（stopタイムアウト10秒） |
| `GET` | `/api/connections/count` | `atmosidea-frontend`の直近5分のログからIPを正規表現抽出し、ユニークIP数をカウント（アクティブユーザー数推定） |
| `GET` | `/api/minio/list/{containerName}` | MinIOのバケット/フォルダ/オブジェクト一覧（クエリ: `bucket`、`prefix`） |
| `POST` | `/api/minio/upload/{containerName}` | MinIOバケットへファイルアップロード（multipart、key: `file`、クエリ: `bucket`、`prefix`） |
| `DELETE` | `/api/minio/delete/{containerName}` | MinIOからオブジェクト削除（クエリ: `bucket`、`key`） |
| `GET` | `/ws/logs` / `/ws/logs/{id}` | コンテナログをWebSocketでリアルタイムストリーミング（クエリ: `container`） |

### 使用ライブラリ（`backend/go.mod`）

- `github.com/docker/docker` — Dockerクライアント（コンテナ一覧・統計・再起動・ログ）
- `github.com/gorilla/mux` — ルーティング
- `github.com/gorilla/websocket` — WebSocketストリーミング
- `github.com/minio/minio-go/v7` — MinIOオブジェクト操作

## 監視ロジック（フロントエンド）

フロントエンド（`frontend/src/App.jsx`）がエラー検出・可視化を担う。

### アクティブユーザー数（`/api/connections/count`）

`atmosidea-frontend`コンテナの直近5分間のログを解析し、IPアドレス正規表現で抽出したユニークIP数をアクティブユーザー数として推定する。ログベースの簡易的な推定であり、正確なセッション計測ではない。

### エラー検出（10秒間に5件）

エラー表示はフロントエンドでの判定（バックエンドは各行を個別にストリーミングするだけ）。

1. WebSocketで受信した各行を正規表現`/(error|fatal|fail|exception|stderr|panic)/i`で判定（`App.jsx:401`）
2. マッチした時刻を`errorLoopState[コンテナ名]`に保持。**直近5件だけ保持**（`.slice(-5)`、`App.jsx:404`）
3. **errorログが5件以上** かつ **最初のerrorから10秒以内**（`now - timestamps[0] < 10000`）を満たすとエラーと判定（`App.jsx:429-439`）

条件を満たすと、対象コンテナのノード枠と接続線が赤（rose）に変換される。エラー表示後は状態がリセットされ、再度5件/10秒で再検出可能。

### 可視化

- **System Activity（LPS）**: 直近2秒間のログ受信数をグラフ表示
- **Active Users**: `/api/connections/count`の推定ユーザー数（5分間）
- **System Load**: 全コンテナのCPU・メモリの平均使用率（90%超で赤、70%超で amber、それ以下で green）
- **トポロジー図**: コンテナを5層（UI / API Gateway & Services / Security Scan & Workers / DB & Cache / Storage & Persistence）に分類し、接続線を付与。接続線はログ出力時に点滅（cyan）、エラー時に赤に変換

### コンテナ分類（`App.jsx`）

各コンテナ名を接尾辞で分類し、レイヤーへ割り当てる。`getContainerGroup`（`App.jsx:324`）:

| レイヤー | 分類対象 |
|---|---|
| UI | `frontend` / `frontend-builder` |
| API | `auth-service` / `profile-service` / `team-service` / `mypage-service` / `upload-api` |
| Workers | `sfsp-api` / `sfsp-worker` / `video-worker` / `game-worker` / `static-site-worker` / `clamav` / `yara` |
| DB | `auth-db` / `app-db` / `profile-db` / `team-db` / `sfsp-db` / `redis` |
| Storage | `profile-storage` / `game-storage` / `static-site-storage` / `video-storage` / `minio` / `storage` |
| Unknown | 上記以外 |

監視対象外（トポロジー・レイアウトから除外）: `mon-backend` / `mon-nginx` / `mon-frontend-builder`、および `mon-` プレフィックスのコンテナ。

### 接続ポロジー（ネットワーク共有から動的生成）

`/api/topology`で取得した各コンテナの接続ネットワークから、**2層以上でネットワークを共有するコンテナ間**に接続線を生成する。`App.jsx:636`の`connections`が該当。

- **ソース（UI / API）→ デンディペンデント（Workers / DB / Storage / Unknown）** の方向に限定し、レイヤー跨ぎの依存関係のみを描画する。
- 接続線の色は共有ネットワークで色分け（`networkColor`、`App.jsx:368`）: `sfsp`系=amber、`db`系=green、その他=blue。
- これにより、`docker-compose.yml`のハードコード済みトポロジーを追従せず、実行中のコンテナの実際のネットワーク接続から可視化される。

### コンテナカード情報

各ノードカードに以下の情報を追加表示:

- **ヘルスステータス**: `healthy`/`unhealthy`の場合のみドット表示（`unhealthy`は赤点滅）。`App.jsx:204`
- **再起動回数**: `restartCount > 0` のみ `↻ N restarts` と表示（`App.jsx:218`）
- **稼働時間**: 起動時刻（`StartedAt`）から現在までの経過時間（秒単位、`App.jsx:219`）
- **不正状態**（`unhealthy` / 停止中）はカード枠を赤（rose）で強調（`App.jsx:167`）

### ボリューム一覧パネル

右上の「Volumes」ボタンでDockerボリューム一覧（名前・ドライバ・マウントポイント）を表示するモーダルを開く。`/api/volumes`から取得し、`VolumesModal`（`App.jsx:376`）で描画する。

## MinIO操作

`getMinioClient`（`main.go:366`）は、対象コンテナの環境変数から認証情報を取得する。順序は `MINIO_ROOT_USER`→`MINIO_ACCESS_KEY`、`MINIO_ROOT_PASSWORD`→`MINIO_SECRET_KEY`。エンドポイントは`{containerName}:9000`。SFSP隔離ネットワーク接続により`sfsp-minio`へのアクセスも可能。

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
├── nginx.conf                  # /api/ と /ws を mon-backendへプロキシ
└── scripts/                    # start.bat / stop.bat / clean.bat
```

## 補足

- 監視サービスは任意サービス。メインのフロントエンド（`http://localhost:3001`）とは独立して起動する。
- Dockerソケットへの直接アクセスとMinIOへの直接書き込みを含むため、開発・運用環境向けの機能。本番公開は想定していない。
- エラー検出閾値（10秒間に5件）やerrorキーワード一覧は`frontend/src/App.jsx`に集約されている。