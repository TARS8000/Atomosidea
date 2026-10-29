# Monitoring Service（監視ダッシュボード）

開発者向けにシステム全体の状態（コンテナ・リソース・MinIO・ログ）を一元的に監視するサービス。Dockerソケットに直接接続し（読み取り専用でマウント）、各コンテナの稼働状況、CPU・メモリ使用率、依存関係、アクティブユーザー数、MinIOオブジェクトの閲覧・アップロード・削除、リアルタイムログのストリーミングを提供する。画面はコンテナをツリーで管理する形（左にツリー、右に概要・詳細）。コンテナの再起動などDockerを操作する機能は持たない（以前あった再起動APIは削除した）。

- **アクセス:** `http://127.0.0.1:8090`（ホストのループバックのみに公開。HTTP Basic認証あり）
- **技術スタック:** Go（バックエンド）+ React/Vite（フロントエンド）+ Nginx（配信・プロキシ）
- **依存:** 既存の`atmosidea-network`ネットワークに接続（`external`のため、先にメインのスタックを起動してネットワークを作成しておく）。以前は`sfsp-isolated-net`にも接続していたが、MinIO（`atmosidea-minio:9000`）へは`atmosidea-network`で届くため外した

## アーキテクチャ

3コンテナで構成され、Nginxがフロント（静的ファイル）とAPI/WebSocketを振り分ける。

| コンテナ | 画像 / ビルド | 役割 |
|---|---|---|
| `mon-nginx` | `nginx:alpine` | `127.0.0.1:8090`→80。静的フロントを`auth_request`による認証付きで配信し、`/api/`（REST のみ）と`/ws`（WebSocket の Upgrade ヘッダーを転送）を`mon-backend:8080`へプロキシ。`client_max_body_size 32m`（MinIO へのアップロード用。既定の 1MB だと 413 になる） |
| `mon-backend` | `golang:1.25-alpine`（マルチステージビルド） | Dockerソケット経由でコンテナ一覧・統計・MinIO操作・WebSocketログストリーミングを提供。全ルートでHost/Origin検証とHTTP Basic認証を行う。内部ポート8080 |
| `mon-frontend`（コンテナ名`mon-frontend-builder`） | `node:20-alpine` | `npm run build`で`./frontend/dist`へ出力して終了（ビルド専用、稼働しない） |

### 依存関係

- `mon-nginx` は `mon-backend`・`mon-frontend` の後に起動する（`depends_on`は起動順のみで、ビルド完了までは待たない）。ビルド成果物（`./frontend/dist`）はホストのバインドマウントなので、ビルドが終われば`/var/www/html`から配信される。
- `mon-backend` は `/var/run/docker.sock`をroでマウントし、`monitoring-network`と`atmosidea-network`に接続する。各コンテナのヘルス状態・起動時刻・再起動回数は`inspectContainerMeta`、ネットワーク接続情報は`getContainerNetworks`（`/api/topology`用）が`ContainerInspect`で動的に取得する。compose のプロジェクト名・サービス名・`depends_on` はコンテナのラベル（`com.docker.compose.*`）から読む。
- `docker-compose.monitoring.yml` に `version:` は無い（Compose v2 では不要）。

## セットアップ

`monitoring-service`ディレクトリ内で以下を実行。`http://localhost:8090`にアクセス。

```bash
docker compose --env-file ../.env -f docker-compose.monitoring.yml up -d --build
```

- **ログイン（HTTP Basic 認証）**: ユーザー名とパスワードはプロジェクトルートの `.env` の `MONITOR_USERNAME` / `MONITOR_PASSWORD`。`mon-backend` が `/api`・`/ws` を含む全ルートで定数時間比較により検証し、UI（静的ファイル）は `mon-nginx` の `auth_request`（`/auth/check`）で同じ資格情報を要求する。どちらかが未設定だと全リクエストを拒否する（fail closed）。
- `--env-file ../.env` を付けないと資格情報が渡らず、`/api`・`/ws` は 503 を返す（UI は `auth_request` が 503 を受けるため mon-nginx が 500 を返す）。`scripts/start.bat` は自動で付ける。
- **Host / Origin の検証**: `Host` は `localhost` / `127.0.0.1` / `::1` のみ許可し（DNSリバインディング対策）、`Origin` がある場合は同一ホストでなければ 403（WebSocket の `CheckOrigin` も同じ）。環境変数 `MONITORING_ALLOWED_HOSTS`（カンマ区切り、空白と空要素は無視）で許可ホストを追加できる。`docker-compose.monitoring.yml` が `.env` の同名の値を `mon-backend` に渡す（未設定なら追加なし）。

`scripts/` に起動・停止・クリーンアップ用batが用意されている。

| スクリプト | 説明 |
|---|---|
| `start.bat` | ルートの`.env`を`--env-file`で渡し、キャッシュなしでビルド後、`up -d`で起動 |
| `stop.bat` | `down`で停止 |
| `clean.bat` | `down --volumes --remove-orphans`でボリューム込みの完全クリーンアップ |

3つとも `docker compose`（v2）を使い、パスはスクリプトの場所（`%~dp0`）から組み立てるので、どのディレクトリから実行してもよい。どれもルートの `.env` を `--env-file` で渡し、失敗したら終了コードを返して止まる。

> **注意（`clean.bat`）**: ルートの `.env` で `COMPOSE_PROJECT_NAME` を設定していると、`--env-file ../.env` で起動した監視サービスはメインのスタックと**同じ compose プロジェクト**になる（起動時に、メインのコンテナが orphan だという警告が出る）。この状態で `clean.bat` を実行すると、`--remove-orphans` によってメインのスタックのコンテナまで削除される。`COMPOSE_PROJECT_NAME` を設定している場合は `clean.bat` を使わず、`stop.bat`（`--remove-orphans` なし。mon-* だけを止める）を使う。

## API（mon-backend、内部ポート8080 / Nginx経由 `/api`）

| メソッド | エンドポイント | 説明 |
|---|---|---|
| `GET` | `/api/containers` | 停止中を含む全コンテナ一覧（ID・名前・イメージ・状態・作成時刻・ヘルス・起動時刻・稼働時間・再起動回数・compose のプロジェクト名`project`・サービス名`service`・依存先サービス名の配列`dependsOn`）。`project` / `service` は compose 外のコンテナでは空、`dependsOn` は`depends_on`ラベル（`redis:service_started:false,...`）からサービス名だけを取り出したもの（`parseDependsOn`）。0件のときは`null`ではなく`[]`を返す。ネットワークは含まない（`/api/topology`で取得） |
| `GET` | `/api/containers/stats` | 全runningコンテナのCPU％・メモリ使用量（並行取得）。CPU％は`calculateCPUPercent`が docker stats と同じ式で計算し、CPU数は`OnlineCPUs`を優先する（cgroup v2 では`PercpuUsage`が空になるため） |
| `GET` | `/api/topology` | 停止中を含む全コンテナのネットワーク接続情報（ID・名前・接続ネットワーク）。画面では詳細ペインの「ネットワーク」に使う |
| `GET` | `/api/volumes` | Dockerボリューム一覧（名前・ドライバ・マウントポイント・ラベル） |
| `GET` | `/api/connections/count` | `atmosidea-frontend`の直近5分のログからIPを正規表現抽出し、ユニークIP数をカウント（アクティブユーザー数推定） |
| `GET` | `/api/minio/list/{containerName}` | MinIOのバケット/フォルダ/オブジェクト一覧（クエリ: `bucket`、`prefix`） |
| `POST` | `/api/minio/upload/{containerName}` | MinIOバケットへファイルアップロード（multipart、key: `file`、クエリ: `bucket`、`prefix`） |
| `DELETE` | `/api/minio/delete/{containerName}` | MinIOからオブジェクト削除（クエリ: `bucket`、`key`） |
| `GET` | `/ws/logs` | コンテナログをWebSocketでリアルタイムストリーミング（クエリ: `container` = コンテナ名または ID。直近100行から追従）。各行の先頭の Docker のタイムスタンプは、RFC3339 として解析できたときだけ外して本文を送る。停止中のコンテナでは既存のログを送り終えると閉じる |
| `GET` / `HEAD` | `/auth/check` | mon-nginxの`auth_request`専用（内部location `/_monitor_auth`から呼ばれる）。認証済みなら204 |

### 使用ライブラリ（`backend/go.mod`）

- `github.com/docker/docker` — Dockerクライアント（コンテナ一覧・統計・inspect・ログ）
- `github.com/gorilla/mux` — ルーティング
- `github.com/gorilla/websocket` — WebSocketストリーミング
- `github.com/minio/minio-go/v7` — MinIOオブジェクト操作

## 画面（フロントエンド）

フロントエンド（`frontend/src/App.jsx`）が分類・状態判定・エラー検出・表示を担う。

```
┌ Atomosidea Monitor ─────────────────── LPS │ Users │ CPU │ MEM │ Volumes ┐
├─ ツリー ──────────────────────┬─ 概要 / 詳細 ───────────────────────────┤
│ [機能 › レイヤー|レイヤー › 機能] │ 未選択: 自プロジェクトの件数と「要確認」  │
│ [絞り込み] [□正常終了も表示]      │ 選択時: 状態・リソース・ネットワーク・   │
│ ▾ ● atomosidea-claude   21/21  │         依存先/依存元・ログ・Storage    │
│   ▾ ● game               2/2   │                                        │
│       ● game-upload-api Upload │                                        │
│       ● game-worker     Worker │                                        │
│ ▸ ● quake-monitor        3/4   │                                        │
└────────────────────────────────┴────────────────────────────────────────┘
```

### ヘッダー

| 表示 | 内容 |
|---|---|
| LPS | 1秒あたりのログ受信行数（2秒ごとに集計、直近30回分をスパークライン表示）。受信している全コンテナの合計 |
| Users | `/api/connections/count` の推定ユーザー数（5分間） |
| CPU | 自プロジェクトのコンテナの CPU％の合計（docker stats と同じく 1コア = 100%） |
| MEM | 自プロジェクトのコンテナのメモリ使用量の合計 / ホストのメモリ（メモリ制限のないコンテナが報告する上限 = 各コンテナの`memLimit`の最大値） |
| Volumes | Dockerボリューム一覧（名前・ドライバ・マウントポイント）のモーダルを開く（`VolumesModal`） |

### 自プロジェクトの判定

`ownProject` は、`atmosidea-frontend` コンテナが属する compose プロジェクト（`.env` の `COMPOSE_PROJECT_NAME`、例: `atomosidea-claude`）。見つからなければ、監視対象のコンテナ数が最も多いプロジェクトを使う。ヘッダーの CPU / MEM と概要の集計は自プロジェクトだけが対象。

### ツリー

階層は **compose プロジェクト → グループ → コンテナ**。

- **自プロジェクト**: 先頭に置き、機能またはレイヤーでグループ分けする。上部のボタンで「機能 › レイヤー」と「レイヤー › 機能」を切り替えられ（`GROUP_MODES`。選択はブラウザの `localStorage` の `mon.groupBy` に保存）、グループにしなかった方の軸は各行のバッジで示す（例: 機能で分けたとき `game-upload-api [Upload]`）。
- **他のプロジェクト**（同じ Docker で動いている別の compose プロジェクト）: グループ分けせず、プロジェクトの下にコンテナを直接並べる。既定では折りたたむ。compose 外のコンテナは `(compose 外)` にまとめる。
- **コンテナの行**: 状態の点、サービス名（compose のサービス名。ラベルが無ければコンテナ名から `atmosidea-` を外したもの）、バッジ、ログ出力中の印（直近0.8秒以内にログを受信したら点灯）、CPU％、メモリ使用量。マウスを乗せるとコンテナ名・イメージ・状態をツールチップで出す。クリックで右ペインに詳細を出す。
- **グループ・プロジェクトの行**: 配下で最も悪い状態の点と、`稼働中/全体` の数。クリックで開閉する（既定は自プロジェクトとそのグループが開、他プロジェクトが閉）。
- **絞り込み**: 名前・イメージの部分一致。絞り込み中は全ノードを開く。
- **正常終了も表示**: 既定はオフで、exit 0 で終わった一回きりのコンテナ（`db-role-init` / `minio-init` / `sfsp-scan-volume-init` など）を隠す。

監視対象外（ツリー・集計から除外。`isSystemContainer`）: `mon-` で始まるコンテナ（`mon-backend` / `mon-nginx` / `mon-frontend-builder`）と、`-builder` で終わるコンテナ（`sfsp-clamav-client-builder` など）。他プロジェクトのコンテナは除外せず、プロジェクトごとに分けて表示する。

### 分類（機能・レイヤー）

どちらもサービス名で判定する。

**レイヤー**（`getLayer`。上から順に判定し、最初に当たったもの）:

| レイヤー | 条件 | 例 |
|---|---|---|
| Frontend | `frontend` または `-frontend` で終わる | `frontend` |
| Upload | `upload-api` で終わる | `game-upload-api` |
| API | `-service` で終わる | `auth-service` / `profile-service` / `team-service` / `mypage-service` |
| Init/Infra | `-init` で終わる、または `docker-proxy` を含む | `db-role-init` / `minio-init` / `sfsp-docker-proxy` |
| Worker | `-worker` で終わる、`sfsp-api`、`clamav` / `yara` を含む | `game-worker` / `sfsp-api` / `sfsp-worker` |
| DB/Cache | `-db` で終わる、または `redis` | `auth-db` / `app-db` / `sfsp-db` / `redis` |
| Storage | `minio` / `storage` を含む | `minio` |
| その他 | 上記以外 | |

**機能**（`getFeature`）: サービス名が `auth` / `profile` / `team` / `mypage` / `game` / `video` / `static-site` / `sfsp` のいずれか、またはそれに `-` が続く名前ならその機能（`FEATURES`）。どれにも当たらないもの（`frontend` / `app-db` / `redis` / `minio` / `db-role-init` / `minio-init`）は「共通基盤」。

### 状態

`getStatus` が各コンテナを5段階に分ける。ツリーの点・概要の件数・詳細ペインで共通に使う。

| 状態 | 点 | 条件 |
|---|---|---|
| error | 赤（点滅） | ヘルスが `unhealthy`、またはエラーログ多発（後述）。ツリーの行も赤い背景になる |
| down | 赤 | 異常終了（exit 0 以外）または `dead` |
| warn | 黄 | 稼働中でヘルスチェック待ち（`starting`）、または `created` / `paused` / `restarting` など |
| ok | 緑 | 稼働中 |
| done | 白抜き | exit 0 で終了（init 系など。異常ではない） |

グループ・プロジェクトの点は、配下で最も悪い状態（error > down > warn > ok > done）。

### 右ペイン

- **未選択時（概要、`Overview`）**: 自プロジェクトの件数（稼働中 = ok + warn / 正常終了 / 異常終了・停止 / 異常）と「要確認」の一覧。要確認には、エラーログ多発・unhealthy・異常終了・ヘルスチェック待ち・再起動回数 1回以上のコンテナを、理由付きで悪い順に並べる。クリックでそのコンテナを選ぶ。
- **選択時（詳細、`DetailPane`）**: サービス名・コンテナ名・イメージ、状態・ヘルス・稼働時間・再起動回数・CPU・メモリ（使用量と上限に対する割合）、接続ネットワーク（`/api/topology`）、**依存先 / 依存元**、ログ・Storage のタブ。× で概要に戻る。
  - **依存先** は選んだコンテナの `depends_on` に書かれたサービス、**依存元** は選んだコンテナを `depends_on` に書いているサービス（同じプロジェクト内のみ）。各チップに状態の点が付き、クリックでそのコンテナに移る。compose の定義そのものなので、ネットワークの共有から推測していた旧マップの接続線と違い、実際の依存だけが出る。
  - **ログ**: コンテナごとに直近500行を保持し、新しい行が来たら末尾までスクロールする。エラーの正規表現に当たる行は赤で表示する。
  - **Storage**: MinIO のブラウザ（後述）。MinIO のコンテナを選ぶと最初からこのタブを開く。

### ログの受信（WebSocket）

`/ws/logs` への接続は、コンテナの種類で張り方を変える。

- **自プロジェクトの稼働中（`running` / `starting` / `restarting`）のコンテナ**: 常に接続する（ツリーのログ出力中の印・LPS・エラー検出に使う）。
- **他プロジェクトの稼働中のコンテナ**: 選んでいる間だけ接続する。
- **停止中のコンテナ**: 選んだときに一度だけ接続して、残っているログを読む（読み終わるとサーバー側が閉じる）。再び稼働したら通常の扱いに戻る。

受信した行はバッファに溜め、300ms ごとにまとめて画面に反映する（1行ごとに再描画しない）。切断されたときは自動の再接続タイマーを使わず、コンテナ一覧の定期取得（3秒ごと）で張り直す。ログに `min.io` が出たコンテナは Storage タブを表示する対象になる。

### エラー検出（10秒間に5件）

エラー表示はフロントエンドでの判定（バックエンドは各行を個別にストリーミングするだけ）。

1. WebSocketで受信した各行を正規表現`/(error|fatal|fail|exception|stderr|panic)/i`（`ERROR_LINE`）で判定
2. マッチした受信時刻を`errorLoopState[コンテナ名]`に保持。**直近5件だけ保持**（`.slice(-5)`）
3. **errorログが5件以上** かつ **最初のerrorから10秒以内**（`now - timestamps[0] < 10000`）を満たすとエラーと判定（`errorLogsMap`）

検出されると状態が error になり、ツリーの行・所属するグループとプロジェクトの点が赤くなり、概要の「要確認」に出る。検出後は `ERROR_HIGHLIGHT_MS`（10秒）だけ保持し、その後リセットされて再度5件/10秒で再検出できる（`errorClearTimeoutsRef`）。

**既知の問題**: 接続のたびに直近100行がまとめて届き、判定は受信時刻で行うため、過去のエラー行が5件以上あると、ページを開いた直後や再接続の直後にエラーと誤検出することがある。同じ理由で、直後の LPS も一時的に跳ね上がる。

### 更新間隔

コンテナ一覧と統計は3秒ごと、アクティブユーザー数は10秒ごと、トポロジー（ネットワーク接続）とボリューム一覧は15秒ごとに取り直す（コンテナの追加・削除やネットワークの変化を反映するため）。

### アクティブユーザー数（`/api/connections/count`）

`atmosidea-frontend`コンテナの直近5分間のログを解析し、IPアドレス正規表現で抽出したユニークIP数をアクティブユーザー数として推定する。ログベースの簡易的な推定であり、正確なセッション計測ではない（プロキシなど内部の IP も数に含まれうる）。

## MinIO操作

`getMinioClient`（`backend/main.go`）は、対象コンテナの環境変数から認証情報を取得する。順序は `MINIO_ROOT_USER`→`MINIO_ACCESS_KEY`、`MINIO_ROOT_PASSWORD`→`MINIO_SECRET_KEY`。エンドポイントは`{containerName}:9000`（TLSなし）。MinIOは`atmosidea-minio`の1コンテナのみで、全バケットをここから操作する（旧構成の`sfsp-minio`等は存在しない）。root資格情報で操作するため、アップロード・削除はIAMポリシーの制限を受けない。フロントエンドは、コンテナ名・イメージ名に`minio` / `storage`を含む、またはログに`min.io`が出るコンテナで、詳細ペインに Storage タブを表示する。バケット → フォルダとダブルクリックで降り、← で戻る。アップロードは開いているバケット・フォルダに置き、削除は選択したファイルだけが対象（確認ダイアログあり）。

## ディレクトリ構成

```
monitoring-service/
├── backend/                    # Goバックエンド
│   ├── Dockerfile              # golang:1.25-alpine (multi-stage build)。go.mod / go.sum をコピーして go mod download（go mod tidy はしない）
│   ├── go.mod
│   ├── go.sum                  # コミット済み（ビルド時の整合性検証に使う）
│   └── main.go                 # 全API・WebSocket・MinIOロジック
├── frontend/                   # React/Viteフロントエンド
│   ├── Dockerfile              # node:20-alpine、`npm run build`でdist出力
│   ├── .eslintrc.cjs           # ESLint の設定（`npm run lint`）
│   ├── package.json
│   └── src/App.jsx             # 分類・状態判定・エラー検出・ツリー/詳細UI
├── docker-compose.monitoring.yml
├── nginx.conf                  # /api/ と /ws を mon-backend へプロキシ、UIは auth_request で保護
└── scripts/                    # start.bat / stop.bat / clean.bat
```

## 補足

- 監視サービスは任意サービス。メインのフロントエンド（`http://localhost:3001`）とは独立して起動する（ただし、メインのスタックが作成するネットワークに接続する）。
- Dockerソケットへの直接アクセスとMinIOへの直接書き込みを含むため、開発・運用環境向けの機能。ホストのループバックのみに公開し、HTTP Basic認証とHost/Origin検証で保護しているが、インターネットへの公開は想定していない。なお、ソケットを`:ro`でマウントしても Docker API の操作自体は制限されない（読み取り専用になるのはソケットファイルだけ）。
- 画面は以前、コンテナをカードでグリッドに並べ、ネットワークの共有から接続線を引くレイヤーマップだった。カードや見出しの重なり、他プロジェクトのコンテナによる横幅の増加、ほぼ全ペアを結ぶ接続線で読みにくかったため、ツリー＋詳細ペインに置き換えた。
- エラー検出閾値（10秒間に5件）とerrorキーワード一覧は`frontend/src/App.jsx`だけにある（以前`backend/main.go`にあった`errorKeywords` / `isError`は画面の判定に使われていなかったため削除した。バックエンドは本文をそのまま送るだけ）。
- mon-backend は CORS ヘッダーを返さず、OPTIONS（プリフライト）も扱わない（UI は同じ mon-nginx から配信される同一オリジン）。
- フロントエンドの依存から、未使用だった `websocket` パッケージと `@types/react` / `@types/react-dom` を削除した（ブラウザ標準の WebSocket を使う）。Tailwind は `index.html` で読み込む CDN 版だけを使い、ビルド時の PostCSS 処理はしていない（そのため、インターネットにつながらない環境ではスタイルが当たらない）。
