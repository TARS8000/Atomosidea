# DoS 対策

Atomosidea に入れてある DoS 対策と、Cloudflare を前に置くときの手順をまとめる。
設定の値は `.env`（未設定なら `docker-compose.yml` の既定値）で変えられる。一覧は `.env.example`。

## 入れてあるもの

| 層 | 何を防ぐか | どこで |
|---|---|---|
| nginx の回数・同時接続数の制限 | 連打・大量の接続（超えたら 429） | `frontend/nginx.conf` |
| nginx のタイムアウト | ゆっくり送り続けて接続を握る攻撃（Slowloris など） | `frontend/nginx.conf` |
| nginx のキャッシュ | ゲーム・静的サイト・動画・画像へのアクセス集中で MinIO が詰まる | `frontend/nginx.conf`（`proxy_cache`） |
| Go のサーバーのタイムアウト | nginx を通り抜けた遅い接続 | 各サービスの `http.Server` |
| ユーザーごとの本数・同時数 | 1 人がアップロードを続ける | `backend/shared/ratelimit` |
| 作ったばかりのアカウントの制限 | アカウントを作り直して制限を逃れる | 同上（JWT の `acctCreated`） |
| ユーザーごとの保存量 | ディスクを埋める | `backend/shared/uploadguard` |
| ディスクの空き・処理待ちの確認 | ディスクが埋まる・検査や動画の変換が際限なく溜まる | 同上 |
| 画像の差し替えの回数 | アイコン・背景・サムネイルの連続変更 | 各サービスの `imageLimitMiddleware` |
| ログインのロック | パスワードの総当たり（IP を変えながらでも） | `backend/auth/auth-worker`（`login_fail:*`） |
| コンテナの CPU・メモリの上限 | 1 つのサービスが VM 全体を止める | `docker-compose.yml`（`deploy.resources`） |
| 分割アップロード | Cloudflare の 1 リクエスト 100 MB 制限 | `backend/shared/chunkupload`・`frontend/src/api/chunkedUpload.ts` |
| 見張り・通知 | 攻撃や異常に気づけない | `monitoring-service/backend/guard.go`（Discord） |

### nginx の制限（既定値）

| 対象 | 数える単位 | 回数 | 同時接続 |
|---|---|---|---|
| API 全般 | ログイン中はトークン、未ログインは IP | 30/秒（burst 100） | IP ごと 50 |
| すべての API（安全弁） | IP | 200/秒（burst 400） | ― |
| ログイン・登録 | IP | 20/分（burst 20） | ― |
| 大きなアップロード（完了の送信） | IP | 10/分（burst 5） | IP ごと 3・全体 6 |
| 分割アップロードの各部分 | トークン | 120/分（burst 30） | IP ごと 8 |
| ゲーム・静的サイト・動画・画像の配信 | IP | 100/秒（burst 500） | IP ごと 100 |

Tailscale 経由では deskmini が送信元を書き換えるので、nginx からは全員が `172.16.2.1` に見える。
そのため「IP ごと」の制限は、いまはサイト全体の上限として働く（値はそれを前提に高めにしてある）。
ログインしている人はトークンごとに数えるので、ほかの人が叩き続けても巻き込まれない。

### アップロードの判定（既定値）

- 本数：ゲーム・静的サイト・動画の合計で 1 時間 20 本・1 日 50 本・同時 2 本（作って 7 日未満のアカウントは 1 時間 5 本・1 日 10 本）
- 保存量：1 人 5 GiB（作って 7 日未満は 1 GiB）。アップロードした本体の大きさ（`upload_bytes`）の合計で数える。
  この仕組みより前の投稿は 0 として数える。削除すれば減る
- ディスク：空きが 10 GiB ＋ 今回のファイルの 2 倍を下回ったら断る（503）
- 処理待ち：検査待ち 20 件以上・動画の変換待ち 10 件以上で断る（503）
- 管理者は本数・保存量の対象外（ディスク・処理待ちは管理者にも効く）
- Redis・DB に届かず判定できないときは通す（この仕組みの障害でアップロード全体を止めないため）

### 見張り

監視ダッシュボード（`http://<VM の IP>:8090/`）の上に Disk（空き）・Queue（検査待ち / 変換待ち）・429（直近 5 分の件数）を出す。
しきい値を超えると赤い帯で知らせ、`.env` に `ALERT_DISCORD_WEBHOOK_URL` があれば Discord にも送る（戻ったときも送る）。

| しきい値 | 既定値 |
|---|---|
| ディスクの空き | 15 GiB 未満（アップロードを断り始める 10 GiB より先に知らせる） |
| 検査待ち / 変換待ち | 15 件 / 8 件以上 |
| 429 / 5xx | 直近 5 分で 300 件 / 50 件以上 |

## Cloudflare を前に置くとき

回線を埋めるほどの大量アクセス（本物の DDoS）は、自宅のサーバーでは受けきれない。公開するなら Cloudflare を前に置く。

1. **Cloudflare Tunnel（cloudflared）でつなぐ**：ルーターのポートは開けない。cloudflared は VM の中（compose に足す）か deskmini で動かし、
   `http://<VM の IP>:3001` に転送する。ゲーム・静的サイトは `<ID>.<ドメイン>` のサブドメインで配信するので、
   公開ホスト名にワイルドカード（`*.<ドメイン>`）も足す。`.env` の `APP_URL`・`STATIC_SITE_DOMAIN` をそのドメインに替える（https になるので、Unity の Brotli もそのまま読める）
2. **nginx の接続元 IP**：`frontend/nginx.conf` は、プライベート IP からのリクエストの `CF-Connecting-IP` を接続元として使う設定になっている。
   cloudflared の IP（compose に足したならそのネットワーク、deskmini なら `172.16.2.1`）だけに `set_real_ip_from` を絞ると、なりすましができなくなる
3. **nginx の値を下げる**：1 人ずつの制限として働くようになるので、`req_ip`（200/秒）・`req_static`（100/秒）・`conn_ip` などを、実際のアクセスを見ながら下げる
4. **Cloudflare 側の設定**（無料プランでできるもの）
   - Security → WAF → Rate limiting rules：`/api/auth/login` に 1 分 10 回など（無料は 1 ルール）
   - Security → Bots：Bot Fight Mode を有効にする
   - Security → Settings：Security Level を Medium 以上。攻撃を受けているときは「Under Attack Mode」
   - Caching → Cache Rules：サブドメイン（ゲーム・静的サイト）と `/api/videos/*/stream/*` を短い時間キャッシュする
   - SSL/TLS：Full。HTTP → HTTPS のリダイレクトを有効にする
5. **アップロード**：1 リクエスト 100 MB までなので、画面からのアップロードは 50 MiB ずつの分割で送る（もう入れてある）。
   画面を通さず API に 100 MB を超えるファイルを直接送ると Cloudflare に 413 で断られる
6. **Google ログイン**：https のドメインになるので、Google Cloud の OAuth クライアントにリダイレクト先（`https://<ドメイン>/api/auth/google/callback`）を登録すれば使えるようになる。
   アカウントはいくらでも作れるので、作ったばかりのアカウントの制限（`NEW_ACCOUNT_DAYS` など）を必要に応じて厳しくする
