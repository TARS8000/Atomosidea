# Atomosidea — AI向けプロジェクト入門ガイド

このドキュメントは、**Atomosideaプロジェクトについて何も知らないAI**が、これを読み終えるだけでプロジェクトの全体像を理解し、仕様・設計上の注意点や起こりやすいエラーを把握した上でコード変更を行えることを目的に書かれています。

読み終えた時点で、次のことができるはずです:
- プロジェクトの全体像と各サービスの役割を説明できる。
- ユーザーがファイルをアップロードしてから公開されるまでの流れを記述できる。
- 各サービスのパス・DB・MinIOバケット・Redisキュー・APIを正しく引用できる。
- このプロジェクト特有の「陥りやすい失敗（トラップ）」を回避できる。
- 安全にコード変更・機能追加を行える。

> **このドキュメントは「教え方」に特化しています。** 現行の実装詳細（正確なパス、テーブル列、環境変数名）は `EditAI.md` を参照してください。本ドキュメントは「どう理解し、どう考えるか」を優先します。
> 数字や名前が `EditAI.md` と食い違う場合は、`docker-compose.yml` と `backend/` の実装を確認し、実装を正としてください。

---

## 0. 読み方と前提

- このドキュメントは**順序立てて読む**ことを推奨します。0→1→2…の順で進めてください。
- 技術用語（マイクロサービス、MQ、S3等）が出てきたら、その場で理解しようとせず、文脈から推測してください。後述の「用語集」も活用してください。
- 「⚠️」はAIが実際に失敗しやすい箇所、「🔑」は設計の核心（なぜそうなっているか）です。

---

## 1. プロジェクトとは（一言で言うと）

**Atomosideaは、「動画・ゲーム・静的サイト」を人がアップロードして公開・閲覧できるウェブプラットフォーム**です。

- ユーザーは自分の作ったUnity WebGLゲームや動画、静的サイトをアップロードする。
- システムがそれをスキャン・変換・展開し、「公開」になってから他ユーザーが見られる。
- 作者自身も、自分の投稿の一覧をマイページで見られ、編集・削除できる。
- 投稿はチーム限定にもでき、その場合はチームのメンバー（または公開チームの閲覧者）だけが見られる。

これは **Vimeo/YouTube（動画）+ Itch.io（ゲーム）+ GitHub Pages（静的サイト）を1つのアプリにまとめた**ようなものとイメージすると分かりやすいです。

---

## 2. なぜマイクロサービスなのか（設計思想）

単一の巨大なプログラム（モノリシック）にせず、**各機能を独立した小さなサービス（コンテナ）に分けています**。

🔑 **理由:**
- **独立してスケールできる**: 動画変換は重いがゲーム処理は軽い、といった状況でも、重いサービスだけ増やせる。
- **壊れ方を隔離できる**: 1つのサービスが落ちても、他のサービスは動き続ける。
- **並行開発できる**: チーム（またはAI）が各サービスを同時に開発できる。
- **セキュリティを隔離できる**: マルウェアを処理するSFSPだけを特別に隔離できる（後述）。

⚠️ **代償（AIが理解すべきトレードオフ）:**
- サービス間通信（HTTP・キュー）が必要で、コードが分断される。
- デプロイ・設定が複雑になる。
- **サービス間の契約（インターフェース契約）を守らないと壊れる**: キュー名・レスポンス形式・DBスキーマ・内部APIの認証キーは「契約」なので、一方だけ変えると全体が壊れます。

---

## 3. 全体像（頭の中に地図を作る）

```
                        [ユーザーのブラウザ]
                                 │
                                 ▼
        ┌───────────────────────────────────────────┐
        │  frontend (Nginx)  ← 入口。ポート3001      │
        │  ・Reactアプリを配信                        │
        │  ・/api/... を各サービスに振り分ける        │
        │  ・MinIO上のHLS動画・画像・サイトを中継     │
        └───────────────────────────────────────────┘
         │      │      │       │        │
         ▼      ▼      ▼       ▼        ▼
      auth   profile  videos  games   static-sites   ← APIサービス（REST）
      │       │                          │
      ▼       ▼                          ▼
   [DB: PostgreSQL × 5]   [Redis: キュー]   [MinIO × 1（バケットで分離）]
                                 │
                                 ▼
        ┌─────────────────────────────────────┐
        │  SFSP（セキュリティスキャン）        │
        │  ・ファイルをスキャン（ClamAV/YARA） │
        │  ・Docker Sandbox内で隔離実行        │
        └─────────────────────────────────────┘
                                 │
                                 ▼
        [worker: 展開・公開処理] → 公開用ファイルをMinIOへ
        （動画のHLS変換だけは video-upload-api 内のgoroutineが行う）
```

> 図は主要な経路だけを描いています。`team-service`・`mypage-service`・`video-worker` などは省略しています。

🔑 **重要概念: 「API」と「worker」の分離**
- **upload-api** は「受け付ける」だけ。重い処理（スキャン・変換・展開）はしません。
- **worker** は「裏で動く処理役」。Redisというキューを介してapiと切り離されています。
- これにより、ユーザーはアップロードに即座に反応し、重い処理はバックグラウンドで行えます。
- ⚠️ 例外: 動画のHLS変換は `video-worker` ではなく、`video-upload-api` の中のgoroutineが行います。`video-worker` は動画の一覧・詳細・更新APIとサムネイル差し替えを担当します。
- キューは「信頼性キュー」（`BRPOPLPUSH` で取り出し、処理後に `Ack`）なので、worker が途中で落ちても、起動時にメッセージが元のキューへ戻されます。

---

## 4. 用語集（先に読んでおく）

| 用語 | 意味 |
|---|---|
| **マイクロサービス** | 小さなプログラムを複数組み合わせて1つのシステムを構成する方式。 |
| **コンテナ (Docker)** | アプリを依存関係ごと隔離して実行する箱。再現性が高い。 |
| **Docker Compose** | 複数のコンテナを1つの設定ファイル(`docker-compose.yml`)で一括管理。 |
| **MQ (メッセージキュー)** | サービス間で非同期にメッセージをやり取りする仕組み。Redisで実装。 |
| **S3 / MinIO** | Amazon S3互換のオブジェクトストレージ。画像・動画・ゲームファイルを保存。 |
| **HLS** | 動画を小さな区切り(.ts)に分け、品質に応じて配信する方式。`.m3u8`は再生リスト。 |
| **JWT** | 認証トークン。署名付きの小さなデータ。提示することで「本人確認」。本プロジェクトでは HS256・有効期限7日。 |
| **Sandbox (サンドボックス)** | 不正プログラムを隔離して実行する安全な箱。本プロジェクトではネットワーク無しのDockerコンテナ。 |
| **ClamAV / YARA** | アンチウイルス(ClamAV)とマルウェアパターン検出(YARA)のツール。 |
| **Nginx** | ウェブサーバー。リクエストを振り分ける「受付係」的役割も担う。 |
| **Redis** | 超高速なメモリ上のデータストア。ジョブのキュー・JWTブロックリスト・OAuthの一時コードに使用。`requirepass` でパスワード保護している。 |
| **信頼性キュー** | `BRPOPLPUSH` で取り出したメッセージを `<キュー名>:processing` に退避し、処理後に `Ack`（LREM）で消す方式。途中で落ちても起動時に元のキューへ戻せる。 |
| **PostgreSQL** | 高機能なリレーショナルデータベース。 |

---

## 5. サービス一覧（誰が何を担当するか）

`docker-compose.yml` には25サービスが定義されています（DB・Redis・MinIO・初期化用・イメージのビルド専用を含む）。**役割別にグループ化して覚えると分かりやすい**です。

### 5.1. ユーザー向け（直接アクセスされるもの）
| サービス | 役割 | 入口パス |
|---|---|---|
| `frontend` | Nginx＋React。全体の入口。 | ホスト3001 |
| `auth-service` | ログイン/登録/Googleログイン/JWT/ログアウト/アカウント削除 | `/api/auth/*` |
| `profile-service` | プロフィール管理＋アイコンスキャン | `/api/profile/*` |
| `mypage-service` | マイページ（自分の投稿一覧） | `/api/my/*` |
| `team-service` | チーム管理＋トークンベースのクローズドコンテンツ共有（閲覧・投稿権限の判定） | `/api/teams/*` |

### 5.2. コンテンツアップロード（受け付け＋裏処理）
| サービス | 役割 | 入口パス |
|---|---|---|
| `video-upload-api` + `video-worker` | upload-api: 動画の受け付け・HLS変換（内部goroutine）・削除。video-worker: 一覧・詳細・更新 | `/api/videos/*` |
| `game-upload-api` + `game-worker` | ゲームの受け付け＋展開 | `/api/games/*` |
| `static-site-upload-api` + `static-site-worker` | 静的サイトの受け付け＋展開 | `/api/static-sites/*` |

> **ペア構造に注意:** 各コンテンツは「upload-api（受け付け）」と「worker（処理）」の2つで1セット。workerはRedis経由でスキャン完了イベントを受け取ります。ただし動画だけは役割が違い、完了イベント（HLS変換）は `video-upload-api` が処理し、`video-worker` は参照・更新APIが中心です。

### 5.3. セキュリティ（隔離された中核）
| サービス | 役割 |
|---|---|
| `sfsp-api` | ファイル受け付け（`X-SFSP-API-Key` 必須）・`raw-files` への保存・ジョブキューイング（SHA256は記録するだけで重複排除はしない） |
| `sfsp-worker` | zipを上限付きで展開し、ClamAV/YARAでスキャン（Docker Sandbox内） |
| `sfsp-clamav-client` / `sfsp-yara-client` | スキャナイメージのビルド専用（起動後すぐ終了する） |
| `sfsp-docker-proxy` | `sfsp-worker` 用のDocker APIプロキシ。`docker.sock` を直接渡さないための中継 |

### 5.4. インフラ
| 種別 | 内容 |
|---|---|
| **DB (PostgreSQL ×5)** | `auth-db`(ユーザー), `app-db`(コンテンツメタ), `profile-db`(プロフィール), `team-db`(チーム), `sfsp-db`(スキャン結果) |
| **MinIO ×1** | 1インスタンス（`minio:9000`）。バケット: `videos`, `games`, `static-sites`, `user-profiles`, `raw-files`, `clean-files`, `quarantine`。サービスごとに別ユーザー・別ポリシー |
| **Redis** | ジョブキュー＋完了イベント＋JWTブロックリスト＋OAuthの一時コード（`requirepass` あり） |
| **初期化用** | `minio-init`（バケット・ユーザー・ポリシー作成）, `db-role-init`（DBロールのパスワード設定） |
| **monitoring-service** | 運用ダッシュボード（任意。別composeで起動、Basic認証あり） |

⚠️ **数字を覚える理由:** 「5基のDB」「1基のMinIO（バケットとユーザーで分離）」は、このプロジェクトの構成を示す指標です。AIが「DBを増やす」「新規ストレージ追加」を提案・実装する際は、この数字と各サービスの依存関係を正しく理解しているかが問われます。古い資料にある「4基のDB」「6基のMinIO」は誤りです。

---

## 6. 核心ワークフロー（これで全体の8割が分かる）

### 6.1. ゲームアップロード（最も重要・非同期）

🔑 **このフローが理解できれば、動画・静的サイトも同じパターンです。**

```
ユーザー → frontend → game-upload-api
                              │
                              ├─▶ app-db.games 「status=scanning」で先に記録
                              └─▶ sfsp-api  「スキャンして」
                                       └─▶ Redis: sfsp:scan_queue
                                                    │
                                              sfsp-worker
                                                    │  zipを上限付きで展開 → 全体をDocker SandboxでClamAV/YARA
                                                    ▼
                                         cleanなら MinIO clean-files に配置
                                                    │
                                        Redis: sfsp:completed:game
                                                    │
                                              game-worker
                                                    │  展開・解像度抽出・CSS注入
                                                    ▼
                                         MinIO games/<id>/ に配置
                                         app-db.games → status=public
                                                    │
                                              frontendがポーリング
                                              (statusがpublicになるまで)
                                                    │
                                                  表示・プレイ可能
```

🔑 **設計の核心:**
1. **受け付けと処理は分離**: apiは即座に`gameId`を返す（202）。重い処理はworkerが裏で行う。アップロードには上限があり、超えると413（ゲーム1GiB・静的サイト500MiB・動画1GiB・画像10MiBが既定）。
2. **スキャンは必須**: すべてのアップロードはSFSPによりスキャンされる。`clean`以外は公開されない。
3. **ステータス遷移**: `scanning → processing → public`（スキャン結果や処理エラーによっては `quarantined` / `failed` / `invalid`。動画・静的サイトでは `error` もある）。フロントはこの状態をポーリングして表示を切り替える。
4. **Redisが接続役**: apiとworkerはRedisキューで繋がる。これで両者は直接呼び合わず、分離されたまま協調できる。
5. **公開範囲**: 投稿者・管理者以外には `status='public'` のものだけが見える。`team_id` が付いた投稿は、さらにチームの `can_view` を満たす人だけが見られる（一覧の `?team=` も同様）。

### 6.2. 認証フロー（簡易）
```
ユーザー → /api/auth/login → auth-service → auth-db 検証
                                    │ 成功
                                    ▼
                             JWT発行（HS256・有効期限7日・jti付き）→ frontendがlocalStorageに保存
                                    │ 以降のAPIで Authorization: Bearer <token> を付与
                                    ▼
                         各サービスがJWT署名を検証し、Redisのブロックリスト(blocklist:<jti>)も確認
                                    → アクセス許可/拒否
```

- ログアウトすると `jti` がRedisのブロックリストに入り、全サービスでそのトークンが使えなくなる（Redisエラー時は拒否）。
- Googleログインでは、JWTをURLに載せない。auth-serviceが60秒・1回限りのコードを発行し、フロントが `POST /api/auth/oauth/exchange` でJWTに引き換える。
- サービス間の内部API（例: `/internal/create`、`/internal/users/:userId`）は `X-Internal-Key` で守られ、Nginxからは届かない。

### 6.3. SFSPスキャン（セキュリティの中核）
```
upload-api ──POST /api/v1/files（X-SFSP-API-Key 付き）──▶ sfsp-api
        │  multipartをストリーミングして MinIO raw-files に保存（SHA256も計算して記録）
        └─▶ sfsp-db に files / scan_jobs(queued) を記録
                └─▶ Redis: sfsp:scan_queue にジョブID
                                │
                        sfsp-worker が BRPOPLPUSH で取得
                                │  zipは上限付きで展開（Zip Slip・symlink・zip爆弾を拒否）
                                │  展開したディレクトリ全体を Docker Sandbox内で ClamAV + YARA
                                ▼
                     clean → MinIO clean-files
                     malicious / suspicious → MinIO quarantine
                                │
                    Redis: sfsp:completed:<target> にイベント
                    （game / static-site / stream / profile / *-thumbnail など）
```

🔑 **なぜこんなにも分離しているか:** マルウェアを処理するため、スキャンに関わるサービスを専用ネットワークに分けています。`sfsp-isolated-net`・`sfsp-db-network`・`sfsp-docker-net` はすべて `internal: true`（外向き通信なし）で、sfsp-db は sfsp-api と sfsp-worker だけ、Docker APIプロキシは sfsp-worker だけが到達できます。ただし `sfsp-isolated-net` には、スキャン依頼を出すアップロードAPI・workerや Redis・MinIO も参加しています（完全に独立した島ではありません）。スキャナーを実際に動かすサンドボックスのコンテナだけが `NetworkMode: none`（ネットワークなし）です。

---

## 7. このプロジェクト特有の「陥りやすい失敗（トラップ）」

AIが変更を行う際、特に注意すべき**プロジェクト固有の制約**です。これらを理解せずに変更すると、思わぬバグを入れてしまいます。

### ⚠️ Trap 1: Nginxの `proxy_pass` 末尾スラッシュ
- **規約**: `proxy_pass http://xxx;` の**末尾にスラッシュを付けない**。
- **理由**: 付けると、`location`でマッチした部分(`/api/auth`)が削られ、バックエンドに(`/me`)しか届かず、Goのルーティングが壊れる。
- **AIの対応**: Nginxを編集するたびに、すべての `proxy_pass` が末尾スラッシュなしか確認。**編集後は `nginx -t` で構文を確認し、`docker compose restart frontend` を必ず実行。**
- 補足: 正規表現の `location` は書いた順に評価され、最初に一致したものが使われる。また `location` 内で `add_header` を1つでも書くと、serverレベルの `add_header`（メインのCSP）は継承されない。

### ⚠️ Trap 2: Goの `shared` 依存とDockerビルド
- 全Goサービスは `github.com/atmosidea/shared` を共有する。
- `go.mod` 末尾に `replace github.com/atmosidea/shared => ../../shared`（サービスの階層に合わせた相対パス）が必要。**忘れると `exit status 128`（GitHub認証エラー）でビルド失敗。**
- Dockerビルドは **常にルートコンテキスト** で `COPY . .` 後 `WORKDIR /app/<パス>`。多くのDockerfileは `go mod download` だけを行う。ただし `profile-worker`・`game-upload-api`・`monitoring-service/backend` のDockerfileは `RUN go mod tidy` を実行し、SFSPのDockerfileは `go.sum` が無いときだけ実行する。依存を追加したら、ローカルの `go.mod` / `go.sum` も更新すること。
- **AIの対応**: 新Goサービス作成時、`replace` を正しく追記。既存Dockerfileの `COPY . .` / `WORKDIR` / ビルドコンテキストを破壊しない。

### ⚠️ Trap 3: 「正のソース」は実装
- ドキュメント（本ガイド・README・`EditAI.md` など）と実装(`docker-compose.yml`・`backend/`)が**食い違った場合、実装が正**。信頼する順は `docker-compose.yml` → `backend/` と `init.sql` → `EditAI.md` → 本ガイド・README。
- 特に古い版のドキュメントは古い構成（DB 4基・MinIO 6基、`auth-service` が別ディレクトリ等）を記載していることがある。
- **AIの対応**: 不確かなパス・名前は、必ず `docker-compose.yml` と `backend/` の実ディレクトリで確認する。

### ⚠️ Trap 4: サービス間契約
- キュー名(`sfsp:scan_queue`等)、レスポンスJSON、DBスキーマ、内部APIのキー（`X-SFSP-API-Key` / `X-Internal-Key`）は「契約」。
- キュー名やイベントの形は `backend/shared/queue` と SFSP の `internal/queue`（`internal/model`）に同じ定義が二重にある。
- **AIの対応**: 一方だけ変更せず、両方を含む関連全サービスで整合を取る。変更後は関連サービスをすべて再ビルド。

### ⚠️ Trap 5: 静的サイトのサブドメイン分離
- 静的サイト（とゲーム）は `http://{id}.localhost:3001/`（`{id}` は10桁の英数字）のサブドメインで配信。メインオリジンの `/games/<id>/` は404を返す。
- `server_name` の正規表現は**引用符で囲む**（バックスラッシュ含むため）。
- **AIの対応**: Nginx編集後、構文エラーが出ていないか確認。

### ⚠️ Trap 6: 動画HLSの配信元とHLS変換の場所
- 動画HLSは**MinIOの `videos` バケット**に置かれ、Nginxが `/api/videos/<id>/stream/*` を MinIO の `videos/<id>/*` へプロキシ（rewrite）して配信する。ローカルファイルを `alias` で配信しているわけではない（`upload_workdir` は変換中の一時領域にすぎない）。
- HLS変換（FFmpeg）は `video-worker` ではなく、**`video-upload-api` 内のgoroutine**が `sfsp:completed:stream` を処理して行う。
- **AIの対応**: 動画配信関連を編集する際、`location ~ '^/api/videos/([a-z0-9]{10})/stream/(.*)$'` のMinIOプロキシと、変換を行う `video-upload-api` の両方を確認。

### ⚠️ Trap 7: 動画APIが2つのサービスに分かれている
- `/api/videos/:id` は `GET`・`PUT` が video-worker、`DELETE` が video-upload-api と、サービスが分かれている。Nginxの `if ($request_method = ...)` は「予期せぬ挙動の原因（if is evil）」なので使わず、削除専用のURL `/api/videos/delete/:id` を用意し、`/api/videos/:id` に rewrite して video-upload-api に転送している。サムネイル削除（`DELETE /api/videos/:id/thumbnail`）にも専用の `location` がある。
- **AIの対応**: video-upload-api 側に新しいエンドポイントを追加したら、専用の `location` も追加すること（追加しないと `/api/videos` に吸われて video-worker に転送され、404になる）。

---

## 8. セキュリティ（理解すべき3点）

### 8.1. JWT + localStorage のトレードオフ
- **メリット**: CSRF攻撃を原理的に阻止（Cookie自動送信に依存しない）。
- **デメリット**: XSSが1箇所でも成功すると `localStorage` のJWTが一瞬で盗まれる。
- **対策**: Reactの自動エスケープ（`dangerouslySetInnerHTML` は使わない）、ユーザーがアップロードしたHTML/JSをメインオリジンで実行させない（サブドメイン分離、`CSP: sandbox` と `nosniff`）、`npm audit`。なおメインページのCSPは `script-src` に `unsafe-inline` / `unsafe-eval` を含むため、CSPだけに頼れる強さではない。

### 8.2. iframe sandbox
- アップロード静的サイトを `sandbox` 付きiframeで表示。
- ただしカメラ機能等のため `allow-same-origin` を許可（→ サブドメイン分離で補強）。

### 8.3. SFSPサンドボックス
- スキャナーのコンテナは `NetworkMode: none`（ネットワークなし）、`CapDrop: ALL`、`no-new-privileges`、スキャン対象ボリュームは `:ro` マウント、リソース制限（メモリ1.5GB・CPU 1・PID 256・30分のタイムアウト。ZIP爆弾等のDoS対策）。
- `sfsp-worker` はDockerソケットも `.env` も直接マウントせず、`sfsp-docker-proxy` 経由でサンドボックスを起動する。ただしプロキシはリクエスト本文を検査しないため、完全な隔離ではない。

🔑 **AIの対応**: 認証・認可・sandbox関連を変更する際は、上記トレードオフを踏まえた上で。理解せずに変更すると脆弱性を持ち込むためです。

---

## 9. 変更前チェックリスト（AI用）

1. **正のソース確認**: パス・DB名・キュー名・環境変数は `docker-compose.yml` と `backend/` で確認。
2. **影響範囲特定**: 変更が「どのサービスの契約」に関わるか特定。関連全サービスをリストアップ。
3. **Goサービス**: `go.mod` の `replace` を確認。`Dockerfile` のビルド方式を破壊しない。
4. **Nginx編集**: 末尾スラッシュなし `proxy_pass` 規約遵守。編集後 `nginx -t` → `docker compose restart frontend`。
5. **DBスキーマ**: 対応DBの `init.sql` を更新（auth-db/app-db/profile-db/team-db/sfsp-db）。既存ボリュームには反映されないので、必要ならマイグレーションSQLも用意する。
6. **MinIO政策**: バケット/ユーザー/ポリシー変更は `docker-compose.yml` の `minio-init` と `backend/minio-init/*.json` と `.env.example` を対で更新。
7. **契約の整合性**: キュー名・レスポンス形式・スキーマ・内部APIのキー（`X-SFSP-API-Key` / `X-Internal-Key`）を関連全サービスで一致させる。完了イベントは `DequeueReliable` で取り出し、終端状態を記録してから `Ack` する。
8. **大規模な削除**: 必ずユーザーの許可を取得。

---

## 10. 実践演習（理解度チェック）

次の質問に答えられれば、プロジェクトを理解しています:

1. **Q**: ユーザーがゲームをアップロードして「プレイ可能」になるまで、どのサービスを通り、各サービスは何をするか記述せよ。
   - *Aの要点: upload-api(scanning記録+sfsp依頼) → sfsp-api(raw-files保存+キュー登録) → sfsp-worker(上限付き展開+Sandboxスキャン) → completed:game → game-worker(展開+解像度抽出+public) → frontendポーリング → 表示*

2. **Q**: なぜ「upload-api」と「worker」を分離しているか、1文で説明せよ。
   - *A: ユーザーに即座の反応を返すため受け付けと重い裏処理を分離し、Redisキューで非同期に協調するため。*

3. **Q**: Nginxの `proxy_pass` 末尾にスラッシュを付けない理由を説明せよ。
   - *A: 付けるとlocationでマッチした部分が削られ、バックエンドが/apiプレフィックスを前提とするルーティングが壊れるため。*

4. **Q**: SFSPを別ネットワークに隔離する理由を説明せよ。
   - *A: マルウェアを扱う経路を他サービスから切り離し（`internal: true` で外向き通信も遮断）、侵害されても範囲を限定するため。スキャナーを動かすサンドボックス自体もネットワークなし。*

5. **Q**: 新Goサービスを追加するとき、ビルドが `exit status 128` で失敗する原因と解決を説明せよ。
   - *A: sharedへのreplace指定不足でGitHubへ通信しようとするため。`go.mod` に正しいreplaceを追記して解決。*

---

## 11. 用語と概念の深掘り（応用）

### 11.1. なぜ5基のDBなのか
- **分離の原則**: ユーザーデータ(auth-db)、コンテンツメタ(app-db)、プロフィール(profile-db)、チーム(team-db)、スキャン結果(sfsp-db)を分離。
- **メリット**: 片方の負荷が他へ影響しない、バックアップ/監査が簡単、権限分離が容易。
- ネットワークも分けている: アプリ用の4基は `backend-db-network`、sfsp-db は専用の `sfsp-db-network`（どちらも `internal: true`）。

### 11.2. なぜMinIOは1基で、バケットとユーザーを分けるのか
- **最小権限**: サービスごとに別アクセスキーと別ポリシー（例: game-worker は `games` バケットの操作と `clean-files` の読み取りだけ）。侵害範囲を限定。
- **管理の簡素化**: インスタンスは1つ（データは `./backend/storage_data`）で、バックアップ/管理が簡単。物理的な分離ではなく、権限による論理的な分離である点に注意。
- アクセスキーを複数サービスで同じ値にすると MinIO 上では1ユーザーになり、ポリシーが合算されてしまう。

### 11.3. なぜDocker Sandboxでスキャンするか
- **隔離実行**: マルウェアがホストや他サービスに影響しない。ネットワークなし（`NetworkMode: none`）なので外部への通信もできない。
- **権限の削減**: `CapDrop: ALL` と `no-new-privileges`。スキャン対象は `:ro` マウント。
- **リソース制限**: ZIP爆弾等のDoS攻撃から保護（メモリ・CPU・PID数・実行時間）。zip自体も、展開時にエントリ数・展開サイズ・圧縮率・深さの上限を設けている。

### 11.4. なぜサブドメイン分離なのか
- **同一オリジンポリシー**: メインアプリと静的サイトをオリジン分離。
- **sandbox強化**: `allow-same-origin` のリスクを低減。

---

## 12. まとめ（記憶すべき核心）

1. **Atomosidea = 動画・ゲーム・静的サイトを人がアップロードして公開するプラットフォーム。**
2. **マイクロサービス**: 各機能は独立したコンテナ。独立してスケール・隔離できる。
3. **API/worker分離**: 受け付けと重い処理はRedisキューで分離。
4. **SFSP**: すべてのアップロードをスキャン。Docker Sandboxで隔離実行。
5. **5DB・1MinIO**: DBは関心事ごとに分離、MinIOはバケットとユーザーで分離。最小権限。
6. **Nginxが入口**: APIゲートウェイ＋静的配信。末尾スラッシュなし `proxy_pass`。
7. **JWT+localStorage**: CSRF回避とXSSリスクのトレードオフ。
8. **正のソースは実装**: ドキュメントと実装が食い違った場合、実装が正。

---

> **次の一歩**: 本ガイドで概念を理解したら、`EditAI.md` で現行の実装詳細（正確なパス・列・環境変数）を確認し、実際のコード `backend/` と対照しながら読んでください。変更を行う前に必ず「第9章 変更前チェックリスト」を確認してください。