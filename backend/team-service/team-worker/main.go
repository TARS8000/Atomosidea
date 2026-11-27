package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// プロセス全体で共有する接続・設定。main で起動時に初期化する。
//   - dbTeam: チーム DB の接続プール。
//   - redisClient: ログアウト済みトークンのブロックリスト確認用（REDIS_ADDR 未設定なら nil）。
//   - jwtSecret: JWT の HS256 署名検証に使う共有鍵。
//   - internalAPIKey: /internal API の X-Internal-Key と照合する鍵（空なら内部 API は無効）。
//   - profileServiceURL: 表示名の取得先となる profile-service のベース URL。
//   - httpClient: profile-service 呼び出し用（3 秒でタイムアウト）。
var (
	dbTeam            *pgxpool.Pool
	redisClient       *redis.Client
	jwtSecret         []byte
	internalAPIKey    string
	profileServiceURL string
	httpClient        = &http.Client{Timeout: 3 * time.Second}
)

// tokenAlphabet はチームトークンに使う文字（英小文字と数字）。
const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// maxTeamNameLen はチーム名の最大文字数（バイト数ではなくルーン数で数える）。
const maxTeamNameLen = 255

// generateToken はチームを URL で識別するための 24 文字のランダムなトークンを生成する。
// 乱数の取得に失敗した場合はプロセスを終了する。
func generateToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("Failed to read random bytes: %v", err)
	}
	// 各バイトを剰余でアルファベットの文字に変換する
	for i := range b {
		b[i] = tokenAlphabet[int(b[i])%len(tokenAlphabet)]
	}
	return string(b)
}

// connectDB はチーム DB への接続プールを作成し、Ping で疎通を確認する（10 秒でタイムアウト）。
// 接続できなければプロセスを終了する。
func connectDB(dsn string) *pgxpool.Pool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("Unable to connect to team database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Unable to ping team database: %v", err)
	}
	return pool
}

// ------------------------------------------------------------------
// 認証（JWT + ログアウトのブロックリスト）
// ------------------------------------------------------------------

// authResult は verifyToken の判定結果を表す。
type authResult int

const (
	authNone  authResult = iota // ヘッダー無し・不正なトークン・ログアウト済み
	authOK                      // 有効なトークン（"userID" をセット済み）
	authError                   // ブロックリストを確認できなかった（Redis エラー）
)

// errNoRedis は Redis が未設定のときに isTokenBlocked が返すエラー。
// 呼び出し側では authError として扱い、認証付きリクエストを拒否する（fail closed）。
var errNoRedis = errors.New("redis is not configured")

// isTokenBlocked は auth-service の logoutHandler が登録する "blocklist:<jti>" を確認する。
func isTokenBlocked(ctx context.Context, jti string) (bool, error) {
	if redisClient == nil {
		return false, errNoRedis
	}
	n, err := redisClient.Exists(ctx, "blocklist:"+jti).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// verifyToken は JWT を検証する（レスポンスは書かない）。
// 署名（HS256 固定）・有効期限・jti・ブロックリストを確認し、成功時は "userID" をセットする。
// Redis のエラー時は authError を返す（呼び出し側で拒否する = fail closed）。
func verifyToken(c *gin.Context) authResult {
	// 同じリクエスト内で検証済みなら再検証しない
	if uid := c.GetString("userID"); uid != "" {
		return authOK
	}
	// 1. Authorization: Bearer ヘッダーからトークンを取り出す
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		return authNone
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	// 2. 署名と有効期限を検証する（alg を HS256 に限定し、alg すり替え攻撃を防ぐ）
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return authNone
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return authNone
	}
	// 3. jti が無いトークンはブロックリストで失効を確認できないので拒否する
	jti, _ := claims["jti"].(string)
	if jti == "" {
		return authNone
	}
	// 4. ユーザー ID を sub → userID → user_id の順に探し、UUID として解釈できるものだけ受け付ける
	var userIDStr string
	for _, k := range []string{"sub", "userID", "user_id"} {
		if s, ok := claims[k].(string); ok && s != "" {
			userIDStr = s
			break
		}
	}
	parsed, err := uuid.Parse(userIDStr)
	if err != nil {
		return authNone
	}
	// 5. ログアウト済み（ブロックリスト登録済み）でないかを確認する
	blocked, err := isTokenBlocked(c.Request.Context(), jti)
	if err != nil {
		log.Printf("Redis error checking blocklist: %v", err)
		return authError
	}
	if blocked {
		return authNone
	}
	// 正規化した UUID 文字列を後続のハンドラへ渡す
	c.Set("userID", parsed.String())
	return authOK
}

// authMiddleware は JWT 認証を必須にするミドルウェア。
// 無効なトークンは 401、ブロックリストを確認できない場合は 500 で中断する。
func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch verifyToken(c) {
		case authOK:
			c.Next()
		case authError:
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
		default:
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
		}
	}
}

// ------------------------------------------------------------------
// 型
// ------------------------------------------------------------------

// NullableString は NULL 許容の文字列を、JSON ではプレーンな文字列（NULL の場合は空文字列）
// としてシリアライズする型です。sql.NullString が生成する {String, Valid} という
// JSON 形式を回避し、フロントエンド（React）が文字列として直接描画できるようにします。
type NullableString struct {
	sql.NullString
}

// MarshalJSON は値があればその文字列を、NULL なら空文字列を JSON として出力する。
func (v NullableString) MarshalJSON() ([]byte, error) {
	if v.Valid {
		return json.Marshal(v.String)
	}
	return json.Marshal("")
}

// Team は teams テーブルの1行を表す。Token は URL でチームを識別する公開用の文字列。
type Team struct {
	ID                string         `json:"id"`
	Token             string         `json:"token"`
	Name              string         `json:"name"`
	Description       NullableString `json:"description"`
	IsPublic          bool           `json:"is_public"`
	AutoApprove       bool           `json:"auto_approve"`
	AllowMemberInvite bool           `json:"allow_member_invite"`
	CreatedBy         NullableString `json:"created_by"`
	CreatedAt         time.Time      `json:"created_at"`
}

// teamColumns は Team を取得する SELECT/RETURNING の列リスト。scanTeam の Scan 順と一致させること。
const teamColumns = "id, token, name, description, is_public, auto_approve, allow_member_invite, created_by, created_at"

// scanTeam は teamColumns の順で取得した1行を Team に読み込む。
func scanTeam(row pgx.Row, t *Team) error {
	return row.Scan(&t.ID, &t.Token, &t.Name, &t.Description, &t.IsPublic, &t.AutoApprove, &t.AllowMemberInvite, &t.CreatedBy, &t.CreatedAt)
}

// Member はチームメンバーの1人分（ユーザー ID・ロール・参加日時）を表す。
type Member struct {
	UserID   string    `json:"user_id"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// JoinRequest は承認型加入リクエストを表します。
type JoinRequest struct {
	ID          string         `json:"id"`
	TeamID      string         `json:"team_id"`
	UserID      string         `json:"user_id"`
	Status      string         `json:"status"`
	RequestedAt time.Time      `json:"requested_at"`
	ReviewedAt  NullableString `json:"reviewed_at"`
	ReviewedBy  NullableString `json:"reviewed_by"`
}

// Post はチーム内の投稿（team_posts の1行）を表す。
// AuthorName は DB には無く、profile-service から取得した表示名を後から詰める。
type Post struct {
	ID         string    `json:"id"`
	TeamID     string    `json:"team_id"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

// ------------------------------------------------------------------
// ロール
// ------------------------------------------------------------------

// roleRank はロールの強さを数値で返す（owner=3 > admin=2 > member=1）。未知のロールは 0。
func roleRank(r string) int {
	switch r {
	case "owner":
		return 3
	case "admin":
		return 2
	case "member":
		return 1
	default:
		return 0
	}
}

// isValidRole は r が owner / admin / member のいずれかなら true を返す。
func isValidRole(r string) bool { return roleRank(r) > 0 }

// canGrantRole は callerRole の利用者が role を付与できるかを返す。自分より弱いロールだけを付与できる。
// 例外として owner は owner を付与できる（オーナー権限の移譲・共同オーナー化のため。
// これが無いと「最後のオーナーは退出できない」状態を解消する手段がチーム削除しかなくなる）。
func canGrantRole(callerRole, role string) bool {
	if callerRole == "owner" && role == "owner" {
		return true
	}
	return roleRank(role) < roleRank(callerRole)
}

// querier は QueryRow を持つ型の共通インターフェース。
// プール（*pgxpool.Pool）とトランザクション（pgx.Tx）のどちらでも memberRole を呼べるようにする。
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// memberRole はメンバーのロールを返す。メンバーでなければ ("", false, nil)。
func memberRole(ctx context.Context, q querier, teamID, userID string) (string, bool, error) {
	var role string
	err := q.QueryRow(ctx, "SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2", teamID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return role, true, nil
}

// resolveTeam はトークンでチームを取得する（認証なし）。
// 見つからない場合や DB エラー時はレスポンス（404 / 500）を書いて nil を返す。
func resolveTeam(c *gin.Context, token string) *Team {
	t := &Team{}
	err := scanTeam(dbTeam.QueryRow(c.Request.Context(), "SELECT "+teamColumns+" FROM teams WHERE token = $1", token), t)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "team not found"})
		return nil
	}
	if err != nil {
		internalError(c, err)
		return nil
	}
	return t
}

// requireTeamRole は呼び出し元のロールのランクが required 以上かを確認する。
// 成功時はチーム ID と呼び出し元のロールを返す。失敗時はレスポンスを書いて ok=false。
func requireTeamRole(c *gin.Context, required string) (teamID string, role string, ok bool) {
	team := resolveTeam(c, c.Param("token"))
	if team == nil {
		return "", "", false
	}
	role, isMember, err := memberRole(c.Request.Context(), dbTeam, team.ID, c.GetString("userID"))
	if err != nil {
		internalError(c, err)
		return "", "", false
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return "", "", false
	}
	if roleRank(role) < roleRank(required) {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permission"})
		return "", "", false
	}
	return team.ID, role, true
}

// internalError はエラーの詳細をログにだけ出力し、クライアントには汎用的な 500 を返す
// （DB エラーの内容などを外部に漏らさないため）。
func internalError(c *gin.Context, err error) {
	log.Printf("internal error on %s %s: %v", c.Request.Method, c.FullPath(), err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}

// ------------------------------------------------------------------
// 表示名（profile-service から取得し、短時間キャッシュする）
// ------------------------------------------------------------------

// nameCacheEntry は表示名キャッシュの1件（表示名と有効期限）。
type nameCacheEntry struct {
	name    string
	expires time.Time
}

// 表示名キャッシュ本体（user_id → エントリ）と、それを保護するミューテックス。
var (
	nameCacheMu sync.Mutex
	nameCache   = map[string]nameCacheEntry{}
)

// 表示名キャッシュの設定。
// nameCacheTTL は取得に成功した表示名の保持期間、nameCacheFailTTL は取得に失敗したときの保持期間
// （profile-service への再試行を抑える）、nameCacheMax はキャッシュの最大件数（達したら全件破棄する）。
const (
	nameCacheTTL     = 5 * time.Minute
	nameCacheFailTTL = 30 * time.Second
	nameCacheMax     = 10000
)

// cachedName はキャッシュから期限内の表示名を返す。無いか期限切れなら ok=false。
func cachedName(id string) (string, bool) {
	nameCacheMu.Lock()
	defer nameCacheMu.Unlock()
	e, ok := nameCache[id]
	if !ok || time.Now().After(e.expires) {
		return "", false
	}
	return e.name, true
}

// storeName は表示名を ttl の期間だけキャッシュに保存する。
func storeName(id, name string, ttl time.Duration) {
	nameCacheMu.Lock()
	defer nameCacheMu.Unlock()
	// メモリが無制限に増えないよう、上限に達したらキャッシュ全体を作り直す（簡易的な追い出し）
	if len(nameCache) >= nameCacheMax {
		nameCache = map[string]nameCacheEntry{}
	}
	nameCache[id] = nameCacheEntry{name: name, expires: time.Now().Add(ttl)}
}

// forgetName は指定ユーザーの表示名をキャッシュから削除する（アカウント削除時に使う）。
func forgetName(id string) {
	nameCacheMu.Lock()
	defer nameCacheMu.Unlock()
	delete(nameCache, id)
}

// fetchProfileName は profile-service の GET /api/profile/:userId から username を取得する。
// プロフィールが存在しない（404）場合はエラーにせず空文字列を返す。
func fetchProfileName(ctx context.Context, userID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileServiceURL+"/api/profile/"+url.PathEscape(userID), nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("profile-service returned %d", resp.StatusCode)
	}
	var body struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return strings.TrimSpace(body.Username), nil
}

// displayNames は user_id → 表示名 を返す。取得できなかったものは空文字列（エラーにはしない）。
func displayNames(ctx context.Context, ids []string) map[string]string {
	result := make(map[string]string, len(ids))
	// 1. 重複と空 ID を除き、キャッシュに無いものだけを取得対象として集める
	var missing []string
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if name, ok := cachedName(id); ok {
			result[id] = name
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 || profileServiceURL == "" {
		return result
	}
	// 2. 未キャッシュの ID を並列に取得する（セマフォで同時実行数を 8 に制限し、profile-service への負荷を抑える）
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, id := range missing {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name, err := fetchProfileName(ctx, id)
			if err != nil {
				log.Printf("display name lookup failed for %s: %v", id, err)
				// 失敗も短時間キャッシュし、障害中に毎回 profile-service を叩かないようにする
				storeName(id, "", nameCacheFailTTL)
				return
			}
			storeName(id, name, nameCacheTTL)
			mu.Lock()
			result[id] = name
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return result
}

// ------------------------------------------------------------------
// チーム
// ------------------------------------------------------------------

// createTeamHandler はチームを作成し、トークンを含むチーム情報を返す（JWT 認証必須）。
// 作成者は同じトランザクションで owner として team_members に登録される。
func createTeamHandler(c *gin.Context) {
	userID := c.GetString("userID")
	var req struct {
		Name              string `json:"name" binding:"required"`
		Description       string `json:"description"`
		IsPublic          *bool  `json:"is_public"`
		AutoApprove       *bool  `json:"auto_approve"`
		AllowMemberInvite *bool  `json:"allow_member_invite"`
	}
	// 1. 入力を検証する（真偽値の項目は省略時 false）
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > maxTeamNameLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must be 1-255 characters"})
		return
	}
	isPublic := req.IsPublic != nil && *req.IsPublic
	// 自動承認は公開チームのみ有効。非公開チームでは強制的に false。
	autoApprove := isPublic && req.AutoApprove != nil && *req.AutoApprove
	allowMemberInvite := req.AllowMemberInvite != nil && *req.AllowMemberInvite

	// 2. チームと owner の登録を1トランザクションで行う。
	//    トークンが既存と衝突した（一意制約違反）場合は新しいトークンで最大 5 回まで作り直す。
	ctx := c.Request.Context()
	var team Team
	created := false
	for i := 0; i < 5 && !created; i++ {
		tx, err := dbTeam.Begin(ctx)
		if err != nil {
			internalError(c, err)
			return
		}
		err = scanTeam(tx.QueryRow(ctx,
			"INSERT INTO teams (token, name, description, is_public, auto_approve, allow_member_invite, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING "+teamColumns,
			generateToken(), req.Name, req.Description, isPublic, autoApprove, allowMemberInvite, userID), &team)
		if err != nil {
			_ = tx.Rollback(ctx)
			if strings.Contains(err.Error(), "unique") {
				continue
			}
			internalError(c, err)
			return
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,'owner')", team.ID, userID); err != nil {
			_ = tx.Rollback(ctx)
			internalError(c, err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			internalError(c, err)
			return
		}
		created = true
	}
	if !created {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate a unique team token"})
		return
	}
	c.JSON(http.StatusCreated, team)
}

// getTeamByTokenHandler はトークンでチーム情報を取得する（認証なし。非公開チームでも返す）。
func getTeamByTokenHandler(c *gin.Context) {
	t := resolveTeam(c, c.Param("token"))
	if t == nil {
		return
	}
	c.JSON(http.StatusOK, t)
}

// listTeams は query を実行し、teamColumns と同じ列順の結果を Team の配列として JSON で返す。
// 0 件のときは null ではなく空配列を返す。
func listTeams(c *gin.Context, query string, args ...any) {
	rows, err := dbTeam.Query(c.Request.Context(), query, args...)
	if err != nil {
		internalError(c, err)
		return
	}
	defer rows.Close()
	teams := []Team{}
	for rows.Next() {
		var t Team
		if err := scanTeam(rows, &t); err != nil {
			internalError(c, err)
			return
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, teams)
}

// listPublicTeamsHandler は公開チームを新しい順に一覧する（認証なし）。
func listPublicTeamsHandler(c *gin.Context) {
	listTeams(c, "SELECT "+teamColumns+" FROM teams WHERE is_public = true ORDER BY created_at DESC")
}

// listMyTeamsHandler は呼び出し元が所属するチームを新しい順に一覧する（JWT 認証必須）。
func listMyTeamsHandler(c *gin.Context) {
	listTeams(c,
		"SELECT t.id, t.token, t.name, t.description, t.is_public, t.auto_approve, t.allow_member_invite, t.created_by, t.created_at FROM teams t JOIN team_members m ON m.team_id = t.id WHERE m.user_id = $1 ORDER BY t.created_at DESC",
		c.GetString("userID"))
}

// updateTeamHandler はチームの名前・説明・公開設定などを更新する（JWT 認証必須、owner のみ）。
// PATCH の意味論: 送られたフィールドだけを更新する（省略したフィールドは変更しない）。
func updateTeamHandler(c *gin.Context) {
	teamID, _, ok := requireTeamRole(c, "owner")
	if !ok {
		return
	}
	var req struct {
		Name              *string `json:"name"`
		Description       *string `json:"description"`
		IsPublic          *bool   `json:"is_public"`
		AutoApprove       *bool   `json:"auto_approve"`
		AllowMemberInvite *bool   `json:"allow_member_invite"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	// SET 句を動的に組み立てる。$1 は WHERE の teamID で、next は値を args に追加して
	// 対応するプレースホルダ（$2, $3, ...）を返す（値は常にバインドし、SQL に埋め込まない）。
	var set []string
	args := []any{teamID}
	next := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || utf8.RuneCountInString(name) > maxTeamNameLen {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name must be 1-255 characters"})
			return
		}
		set = append(set, "name = "+next(name))
	}
	if req.Description != nil {
		set = append(set, "description = "+next(*req.Description))
	}
	if req.IsPublic != nil {
		set = append(set, "is_public = "+next(*req.IsPublic))
	}
	// 自動承認は公開チームのみ有効。
	switch {
	case req.IsPublic != nil && !*req.IsPublic:
		// 非公開に切り替える場合は強制的に false。
		set = append(set, "auto_approve = false")
	case req.AutoApprove != nil && req.IsPublic != nil:
		// ここに来るのは is_public = true を同時に指定した場合。
		set = append(set, "auto_approve = "+next(*req.AutoApprove))
	case req.AutoApprove != nil:
		// is_public を変えない場合は現在の値に従う（UPDATE の右辺は更新前の値）。
		set = append(set, "auto_approve = ("+next(*req.AutoApprove)+"::boolean AND is_public)")
	}
	if req.AllowMemberInvite != nil {
		set = append(set, "allow_member_invite = "+next(*req.AllowMemberInvite))
	}
	if len(set) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}
	query := "UPDATE teams SET " + strings.Join(set, ", ") + " WHERE id = $1"
	if _, err := dbTeam.Exec(c.Request.Context(), query, args...); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "team updated"})
}

// deleteTeamHandler はチームを削除する（JWT 認証必須、owner のみ）。
// メンバー・加入申請・投稿は外部キーの ON DELETE CASCADE で一緒に消える。
func deleteTeamHandler(c *gin.Context) {
	teamID, _, ok := requireTeamRole(c, "owner")
	if !ok {
		return
	}
	if _, err := dbTeam.Exec(c.Request.Context(), "DELETE FROM teams WHERE id = $1", teamID); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "team deleted"})
}

// ------------------------------------------------------------------
// メンバー
// ------------------------------------------------------------------

// getMembersHandler はチームのメンバーを参加順に一覧する（JWT 認証必須、member 以上）。
func getMembersHandler(c *gin.Context) {
	teamID, _, ok := requireTeamRole(c, "member")
	if !ok {
		return
	}
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT user_id, role, joined_at FROM team_members WHERE team_id = $1 ORDER BY joined_at", teamID)
	if err != nil {
		internalError(c, err)
		return
	}
	defer rows.Close()
	members := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Role, &m.JoinedAt); err != nil {
			internalError(c, err)
			return
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, members)
}

// lockTeam はトランザクション内でチームの行をロックし、ID を返す（ロール変更・退出を直列化する）。
func lockTeam(ctx context.Context, tx pgx.Tx, token string) (string, error) {
	var teamID string
	err := tx.QueryRow(ctx, "SELECT id FROM teams WHERE token = $1 FOR UPDATE", token).Scan(&teamID)
	return teamID, err
}

// addMemberHandler はチームにメンバーを追加するか、既存メンバーのロールを変更する（JWT 認証必須、admin 以上）。
// 追加時は 201、ロール変更時は 200 を返す。
// - role は owner / admin / member のいずれか（省略時 member）。
// - 付与できるのは自分より弱いロールだけ（owner は owner の付与＝オーナー権限の移譲も可）。
// - 自分と同じか強いロールの既存メンバーのロールは変更できない。自分自身のロールも変更できない。
func addMemberHandler(c *gin.Context) {
	callerID := c.GetString("userID")
	var req struct {
		UserID string `json:"user_id" binding:"required"`
		Role   string `json:"role"`
	}
	// 1. 入力を検証する（user_id は UUID として正規化し、自分自身の変更は拒否する）
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id is required"})
		return
	}
	target, err := uuid.Parse(req.UserID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id must be a UUID"})
		return
	}
	targetID := target.String()
	role := req.Role
	if role == "" {
		role = "member"
	}
	if !isValidRole(role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be one of owner, admin, member"})
		return
	}
	if targetID == callerID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "you cannot change your own role"})
		return
	}

	// 2. トランザクションを開始してチーム行をロックする（同時のロール変更・退出と競合させない）
	ctx := c.Request.Context()
	tx, err := dbTeam.Begin(ctx)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(ctx)

	teamID, err := lockTeam(ctx, tx, c.Param("token"))
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "team not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	// 3. 呼び出し元が admin 以上で、指定ロールを付与できるかを確認する
	callerRole, isMember, err := memberRole(ctx, tx, teamID, callerID)
	if err != nil {
		internalError(c, err)
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return
	}
	if roleRank(callerRole) < roleRank("admin") {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permission"})
		return
	}
	if !canGrantRole(callerRole, role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "you can only grant roles lower than your own"})
		return
	}
	// 4. 対象が既存メンバーなら、自分より弱いロールの場合だけ変更を許す
	existingRole, exists, err := memberRole(ctx, tx, teamID, targetID)
	if err != nil {
		internalError(c, err)
		return
	}
	if exists && roleRank(existingRole) >= roleRank(callerRole) {
		c.JSON(http.StatusForbidden, gin.H{"error": "you cannot change the role of a member whose role is equal to or higher than yours"})
		return
	}
	// 5. 既存メンバーならロールを更新し、そうでなければメンバーとして追加する
	if exists {
		_, err = tx.Exec(ctx, "UPDATE team_members SET role = $1 WHERE team_id = $2 AND user_id = $3", role, teamID, targetID)
	} else {
		_, err = tx.Exec(ctx, "INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,$3)", teamID, targetID, role)
		if err == nil {
			// 保留中の加入申請があれば承認済みにする
			_, err = tx.Exec(ctx,
				"UPDATE team_join_requests SET status = 'approved', reviewed_at = NOW(), reviewed_by = $1 WHERE team_id = $2 AND user_id = $3 AND status = 'pending'",
				callerID, teamID, targetID)
		}
	}
	if err != nil {
		internalError(c, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(c, err)
		return
	}
	status := http.StatusCreated
	if exists {
		status = http.StatusOK
	}
	c.JSON(status, gin.H{"user_id": targetID, "role": role})
}

// removeMemberHandler はチームからメンバーを削除する、または自分が退出する（JWT 認証必須）。
// - 自分自身: 誰でも退出できる。ただし最後の owner は 409（移譲かチーム削除が先）。
// - 他人: admin 以上で、対象のロールが自分より弱い場合だけ削除できる。
func removeMemberHandler(c *gin.Context) {
	callerID := c.GetString("userID")
	target, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userId must be a UUID"})
		return
	}
	targetID := target.String()

	// 1. トランザクションを開始してチーム行をロックする
	//    （最後の owner の判定と削除の間に、別リクエストで owner が減る競合を防ぐ）
	ctx := c.Request.Context()
	tx, err := dbTeam.Begin(ctx)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(ctx)

	teamID, err := lockTeam(ctx, tx, c.Param("token"))
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "team not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	// 2. 呼び出し元と対象のロールを取得する
	callerRole, isMember, err := memberRole(ctx, tx, teamID, callerID)
	if err != nil {
		internalError(c, err)
		return
	}
	if !isMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return
	}
	targetRole, targetExists, err := memberRole(ctx, tx, teamID, targetID)
	if err != nil {
		internalError(c, err)
		return
	}
	if !targetExists {
		c.JSON(http.StatusNotFound, gin.H{"error": "member not found"})
		return
	}

	// 3. 権限を確認する（自分の退出と他人の削除で条件が異なる）
	if targetID == callerID {
		// 自分の退出: 最後の owner だとチームの管理者がいなくなるので拒否する
		if targetRole == "owner" {
			var owners int
			if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM team_members WHERE team_id = $1 AND role = 'owner'", teamID).Scan(&owners); err != nil {
				internalError(c, err)
				return
			}
			if owners <= 1 {
				c.JSON(http.StatusConflict, gin.H{
					"error": "the last owner cannot leave the team; grant the owner role to another member or delete the team first",
					"code":  "last_owner",
				})
				return
			}
		}
	} else {
		// 他人の削除: admin 以上で、対象が自分より弱いロールの場合のみ
		if roleRank(callerRole) < roleRank("admin") {
			c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permission"})
			return
		}
		if roleRank(targetRole) >= roleRank(callerRole) {
			c.JSON(http.StatusForbidden, gin.H{"error": "you cannot remove a member whose role is equal to or higher than yours"})
			return
		}
	}

	// 4. メンバーを削除してコミットする
	if _, err := tx.Exec(ctx, "DELETE FROM team_members WHERE team_id = $1 AND user_id = $2", teamID, targetID); err != nil {
		internalError(c, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "member removed"})
}

// ------------------------------------------------------------------
// 投稿
// ------------------------------------------------------------------

// contentAccessAllowed は呼び出し元がチームのコンテンツを閲覧できるかを判定する。
// 公開チームは認証なしで閲覧できる。非公開チームは JWT 認証と member 以上のロールが必要。
// アクセスを拒否する場合はレスポンスを書いてから nil を返す。
func contentAccessAllowed(c *gin.Context, token string) *Team {
	team := resolveTeam(c, token)
	if team == nil {
		return nil
	}
	if team.IsPublic {
		return team
	}
	// 非公開チーム: ルートに authMiddleware が無いので、ここで任意認証のトークンを検証する
	switch verifyToken(c) {
	case authError:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
		return nil
	case authNone:
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return nil
	}
	role, isMember, err := memberRole(c.Request.Context(), dbTeam, team.ID, c.GetString("userID"))
	if err != nil {
		internalError(c, err)
		return nil
	}
	if !isMember || roleRank(role) < roleRank("member") {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return nil
	}
	return team
}

// createPostHandler はチームに投稿を作成する（JWT 認証必須、member 以上）。
// レスポンスには投稿者の表示名も含める。
func createPostHandler(c *gin.Context) {
	teamID, _, ok := requireTeamRole(c, "member")
	if !ok {
		return
	}
	userID := c.GetString("userID")
	var req struct {
		Title string `json:"title" binding:"required"`
		Body  string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	if utf8.RuneCountInString(req.Title) > 255 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title must be at most 255 characters"})
		return
	}
	var post Post
	err := dbTeam.QueryRow(c.Request.Context(),
		"INSERT INTO team_posts (team_id, author_id, title, body) VALUES ($1,$2,$3,$4) RETURNING id, team_id, author_id, title, body, created_at",
		teamID, userID, req.Title, req.Body).
		Scan(&post.ID, &post.TeamID, &post.AuthorID, &post.Title, &post.Body, &post.CreatedAt)
	if err != nil {
		internalError(c, err)
		return
	}
	post.AuthorName = displayNames(c.Request.Context(), []string{userID})[userID]
	c.JSON(http.StatusCreated, post)
}

// listPostsHandler はチームの投稿を新しい順に一覧する（認証の要否は team.is_public による）。
func listPostsHandler(c *gin.Context) {
	team := contentAccessAllowed(c, c.Param("token"))
	if team == nil {
		return
	}
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT id, team_id, author_id, title, body, created_at FROM team_posts WHERE team_id = $1 ORDER BY created_at DESC",
		team.ID)
	if err != nil {
		internalError(c, err)
		return
	}
	defer rows.Close()
	posts := []Post{}
	var authorIDs []string
	for rows.Next() {
		var post Post
		if err := rows.Scan(&post.ID, &post.TeamID, &post.AuthorID, &post.Title, &post.Body, &post.CreatedAt); err != nil {
			internalError(c, err)
			return
		}
		posts = append(posts, post)
		authorIDs = append(authorIDs, post.AuthorID)
	}
	if err := rows.Err(); err != nil {
		internalError(c, err)
		return
	}
	// 投稿者の表示名をまとめて取得して埋める（重複 ID は displayNames 側で1回にまとめる）
	names := displayNames(c.Request.Context(), authorIDs)
	for i := range posts {
		posts[i].AuthorName = names[posts[i].AuthorID]
	}
	c.JSON(http.StatusOK, posts)
}

// getPostHandler はチームの投稿を1件取得する（認証の要否は team.is_public による）。
func getPostHandler(c *gin.Context) {
	team := contentAccessAllowed(c, c.Param("token"))
	if team == nil {
		return
	}
	contentID := c.Param("contentID")
	// UUID でない ID は DB の型エラー（500）にせず、404 として扱う
	if _, err := uuid.Parse(contentID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}
	var post Post
	err := dbTeam.QueryRow(c.Request.Context(),
		"SELECT id, team_id, author_id, title, body, created_at FROM team_posts WHERE id = $1 AND team_id = $2",
		contentID, team.ID).Scan(&post.ID, &post.TeamID, &post.AuthorID, &post.Title, &post.Body, &post.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	post.AuthorName = displayNames(c.Request.Context(), []string{post.AuthorID})[post.AuthorID]
	c.JSON(http.StatusOK, post)
}

// ------------------------------------------------------------------
// 加入申請
// ------------------------------------------------------------------

// joinRequestHandler はチームへの加入を申し込む（JWT 認証必須）。
// 既にメンバーなら already_member を返す。非公開チームは 403。
// 公開かつ auto_approve のチーム: 即座に team_members へ追加する。
// それ以外の公開チーム: 保留中（pending）の加入申請を作成する。却下・取り消し済み（または承認後に脱退した）
// 申請は pending に戻して再申請できる。保留中の申請があれば 409。
func joinRequestHandler(c *gin.Context) {
	team := resolveTeam(c, c.Param("token"))
	if team == nil {
		return
	}
	userID := c.GetString("userID")
	ctx := c.Request.Context()

	existingRole, isMember, err := memberRole(ctx, dbTeam, team.ID, userID)
	if err != nil {
		internalError(c, err)
		return
	}
	if isMember {
		c.JSON(http.StatusOK, gin.H{"status": "already_member", "role": existingRole})
		return
	}

	// 非公開チームは加入自体を拒否
	if !team.IsPublic {
		c.JSON(http.StatusForbidden, gin.H{"error": "private team cannot be joined"})
		return
	}

	// 自動承認（公開チームかつ auto_approve）。既存メンバーのロールは変えない。
	if team.AutoApprove {
		if _, err := dbTeam.Exec(ctx,
			"INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,'member') ON CONFLICT (team_id, user_id) DO NOTHING",
			team.ID, userID); err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "approved", "role": "member"})
		return
	}

	// 公開チームかつ手動承認: 申請を作成、または終了済みの申請を pending に戻す。
	// メンバーでないことは確認済みなので、approved の行は「承認後に脱退・削除された」ケース。
	// 既存の行が pending の場合は DO UPDATE の WHERE に一致せず行が返らないため、ErrNoRows を 409 にする。
	var reqID string
	err = dbTeam.QueryRow(ctx,
		`INSERT INTO team_join_requests (team_id, user_id, status) VALUES ($1,$2,'pending')
		 ON CONFLICT (team_id, user_id) DO UPDATE
		   SET status = 'pending', requested_at = NOW(), reviewed_at = NULL, reviewed_by = NULL
		   WHERE team_join_requests.status IN ('rejected', 'cancelled', 'approved')
		 RETURNING id`,
		team.ID, userID).Scan(&reqID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusConflict, gin.H{"error": "a join request is already pending", "status": "pending"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "pending", "request_id": reqID})
}

// listJoinRequestsHandler はチームの加入申請を全ステータス分、新しい順に一覧する（JWT 認証必須、owner/admin のみ）。
func listJoinRequestsHandler(c *gin.Context) {
	teamID, _, ok := requireTeamRole(c, "admin")
	if !ok {
		return
	}
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT id, team_id, user_id, status, requested_at, reviewed_at, reviewed_by FROM team_join_requests WHERE team_id = $1 ORDER BY requested_at DESC",
		teamID)
	if err != nil {
		internalError(c, err)
		return
	}
	defer rows.Close()
	reqs := []JoinRequest{}
	for rows.Next() {
		var r JoinRequest
		if err := rows.Scan(&r.ID, &r.TeamID, &r.UserID, &r.Status, &r.RequestedAt, &r.ReviewedAt, &r.ReviewedBy); err != nil {
			internalError(c, err)
			return
		}
		reqs = append(reqs, r)
	}
	if err := rows.Err(); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, reqs)
}

// reviewJoinRequestHandler は加入申請を承認または却下する（JWT 認証必須、owner/admin のみ）。
// クエリパラメータ action=approve|reject で操作を指定する。
func reviewJoinRequestHandler(c *gin.Context) {
	teamID, _, ok := requireTeamRole(c, "admin")
	if !ok {
		return
	}
	// 1. 入力を検証する（UUID でない ID は 404 として扱う）
	requestID := c.Param("requestId")
	if _, err := uuid.Parse(requestID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pending join request not found"})
		return
	}
	action := c.Query("action") // "approve" or "reject"
	if action != "approve" && action != "reject" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action=approve or action=reject is required"})
		return
	}
	reviewerID := c.GetString("userID")
	newStatus := "approved"
	if action == "reject" {
		newStatus = "rejected"
	}

	ctx := c.Request.Context()
	tx, err := dbTeam.Begin(ctx)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(ctx)

	// 2. 保留中の申請のみを対象にし、追加するのは申請者（requester）本人
	//    （status = 'pending' を条件に UPDATE するので、同じ申請を二重に処理しない）
	var requesterID string
	err = tx.QueryRow(ctx,
		"UPDATE team_join_requests SET status = $1, reviewed_at = NOW(), reviewed_by = $2 WHERE id = $3 AND status = 'pending' AND team_id = $4 RETURNING user_id",
		newStatus, reviewerID, requestID, teamID).Scan(&requesterID)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "pending join request not found"})
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	// 3. 承認ならメンバーに追加する
	if action == "approve" {
		// 既にメンバーの場合は既存ロールを維持する（降格させない）
		if _, err = tx.Exec(ctx,
			"INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT (team_id, user_id) DO NOTHING",
			teamID, requesterID); err != nil {
			internalError(c, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": newStatus, "user_id": requesterID})
}

// cancelJoinRequestHandler は自分の保留中の加入申請を取り消す（JWT 認証必須）。
// 該当する申請やチームが無くても 200 を返す。
func cancelJoinRequestHandler(c *gin.Context) {
	userID := c.GetString("userID")
	_, err := dbTeam.Exec(c.Request.Context(),
		"UPDATE team_join_requests SET status = 'cancelled' WHERE team_id = (SELECT id FROM teams WHERE token = $1) AND user_id = $2 AND status = 'pending'",
		c.Param("token"), userID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "join request cancelled"})
}

// ------------------------------------------------------------------
// 権限の問い合わせ（フロントエンドと各アップロード API が使う）
// ------------------------------------------------------------------

// teamPermissionHandler は呼び出し元がチームに対してできること（閲覧・投稿）とロールを返す。
// 認証は任意。JWT を先に評価してから判定する:
//   - can_view: 公開チームは誰でも true。非公開チームはメンバーだけ true。
//   - can_post: メンバー（member 以上）だけ true。
//
// ブロックリストを確認できない（Redis エラー）場合は 500（fail closed）。
func teamPermissionHandler(c *gin.Context) {
	team := resolveTeam(c, c.Param("token"))
	if team == nil {
		return
	}
	role := ""
	switch verifyToken(c) {
	case authError:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify token"})
		return
	case authOK:
		r, isMember, err := memberRole(c.Request.Context(), dbTeam, team.ID, c.GetString("userID"))
		if err != nil {
			internalError(c, err)
			return
		}
		if isMember {
			role = r
		}
	}
	isMember := roleRank(role) >= roleRank("member")
	c.JSON(http.StatusOK, gin.H{
		"is_public": team.IsPublic,
		"can_view":  team.IsPublic || isMember,
		"can_post":  isMember,
		"role":      role,
	})
}

// ------------------------------------------------------------------
// 内部 API（auth-service のアカウント削除から呼ばれる。/api/teams の外なので Nginx からは届かない）
// ------------------------------------------------------------------

// internalKeyMiddleware は X-Internal-Key ヘッダーを INTERNAL_API_KEY と照合し、一致しなければ 403 で中断する。
// 鍵が未設定なら内部 API 全体を無効にする。
func internalKeyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if internalAPIKey == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "internal API is disabled"})
			return
		}
		// 両方を SHA-256 で同じ長さにしてから定数時間で比較する（タイミング攻撃と長さの漏洩を防ぐ）
		got := sha256.Sum256([]byte(c.GetHeader("X-Internal-Key")))
		want := sha256.Sum256([]byte(internalAPIKey))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Next()
	}
}

// deleteUserDataHandler は DELETE /internal/users/:userId のハンドラ（X-Internal-Key による内部認証）。
// ユーザーのメンバー情報・加入申請・チーム投稿を削除する。
// そのユーザーが唯一の owner のチームは、残りのメンバーのうちロールが最も強く、
// 最も早く参加したメンバーに owner を移譲する。メンバーがいなければチームごと削除する
// （team_posts などは CASCADE）。冪等で、常に 204 を返す。
func deleteUserDataHandler(c *gin.Context) {
	u, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "userId must be a UUID"})
		return
	}
	userID := u.String()
	ctx := c.Request.Context()

	tx, err := dbTeam.Begin(ctx)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(ctx)

	// owner になっているチームをロック（removeMember / addMember と直列化する）
	rows, err := tx.Query(ctx,
		"SELECT t.id FROM teams t JOIN team_members m ON m.team_id = t.id WHERE m.user_id = $1 AND m.role = 'owner' ORDER BY t.id FOR UPDATE OF t",
		userID)
	if err != nil {
		internalError(c, err)
		return
	}
	// 同じトランザクションで次のクエリを発行する前に、結果を読み切ってから rows を閉じる
	var ownedTeams []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			internalError(c, err)
			return
		}
		ownedTeams = append(ownedTeams, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		internalError(c, err)
		return
	}

	// 唯一の owner になっているチームごとに、owner の移譲またはチームの削除を行う
	transferred, deleted := 0, 0
	for _, teamID := range ownedTeams {
		var otherOwners int
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM team_members WHERE team_id = $1 AND role = 'owner' AND user_id <> $2",
			teamID, userID).Scan(&otherOwners); err != nil {
			internalError(c, err)
			return
		}
		// 他にも owner がいるチームは何もしなくてよい
		if otherOwners > 0 {
			continue
		}
		// 後継者を選ぶ: ロールが強い順 → 参加が早い順（同時刻なら id 順で決定的にする）
		var successor string
		err := tx.QueryRow(ctx,
			`SELECT user_id FROM team_members WHERE team_id = $1 AND user_id <> $2
			 ORDER BY CASE role WHEN 'owner' THEN 3 WHEN 'admin' THEN 2 WHEN 'member' THEN 1 ELSE 0 END DESC, joined_at ASC, id ASC
			 LIMIT 1`,
			teamID, userID).Scan(&successor)
		if errors.Is(err, pgx.ErrNoRows) {
			// 他にメンバーがいなければチームごと削除する
			if _, err := tx.Exec(ctx, "DELETE FROM teams WHERE id = $1", teamID); err != nil {
				internalError(c, err)
				return
			}
			deleted++
			continue
		}
		if err != nil {
			internalError(c, err)
			return
		}
		if _, err := tx.Exec(ctx,
			"UPDATE team_members SET role = 'owner' WHERE team_id = $1 AND user_id = $2", teamID, successor); err != nil {
			internalError(c, err)
			return
		}
		transferred++
	}

	// ユーザー本人のデータ（加入申請・メンバー情報・投稿）を全チームから削除する
	for _, q := range []string{
		"DELETE FROM team_join_requests WHERE user_id = $1",
		"DELETE FROM team_members WHERE user_id = $1",
		"DELETE FROM team_posts WHERE author_id = $1",
	} {
		if _, err := tx.Exec(ctx, q, userID); err != nil {
			internalError(c, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		internalError(c, err)
		return
	}
	// 削除済みユーザーの表示名が残らないよう、キャッシュからも消す
	forgetName(userID)
	log.Printf("deleted team data for user %s (ownership transferred: %d, teams deleted: %d)", userID, transferred, deleted)
	c.Status(http.StatusNoContent)
}

// ------------------------------------------------------------------

// connectRedis は REDIS_ADDR の Redis に接続し、3 秒間隔で最大 5 回 Ping を試みる。
// 未設定・接続不能でも起動は止めない（その間、認証付きリクエストは拒否される）。
func connectRedis() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		log.Println("WARNING: REDIS_ADDR is not set; all authenticated requests will be rejected (fail closed)")
		return
	}
	redisClient = redis.NewClient(&redis.Options{Addr: addr, Password: os.Getenv("REDIS_PASSWORD")})
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := redisClient.Ping(ctx).Err()
		cancel()
		if err == nil {
			log.Println("Successfully connected to Redis")
			return
		}
		log.Printf("Failed to connect to Redis (%d/5): %v", i+1, err)
		time.Sleep(3 * time.Second)
	}
	// 起動は続ける。認証付きのリクエストはブロックリストを確認できるまで 500 で拒否される。
	log.Println("WARNING: Redis is unreachable; authenticated requests will fail until it recovers")
}

// main は環境変数から設定を読み込んで DB・Redis に接続し、ルーティングを登録して :8080 で待ち受ける。
func main() {
	// 1. 接続と設定を初期化する（JWT_SECRET が無ければ起動しない）
	dbTeam = connectDB(os.Getenv("TEAM_DATABASE_URL"))
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))
	if len(jwtSecret) == 0 {
		log.Fatal("JWT_SECRET is not set")
	}
	internalAPIKey = os.Getenv("INTERNAL_API_KEY")
	if internalAPIKey == "" {
		log.Println("WARNING: INTERNAL_API_KEY is not set; /internal endpoints will reject all requests")
	}
	profileServiceURL = strings.TrimRight(os.Getenv("PROFILE_SERVICE_URL"), "/")
	if profileServiceURL == "" {
		profileServiceURL = "http://profile-service:8084"
	}
	connectRedis()

	// 2. 公開 API のルーティングを登録する。
	//    authMiddleware の無いルートは認証不要か、ハンドラ内で任意認証（verifyToken）を行う。
	r := gin.Default()
	api := r.Group("/api/teams")
	{
		api.GET("", listPublicTeamsHandler)
		api.GET("/mine", authMiddleware(), listMyTeamsHandler)
		api.POST("", authMiddleware(), createTeamHandler)
		api.GET("/:token", getTeamByTokenHandler)
		api.GET("/:token/permission", teamPermissionHandler)
		api.GET("/:token/members", authMiddleware(), getMembersHandler)
		api.POST("/:token/members", authMiddleware(), addMemberHandler)
		api.DELETE("/:token/members/:userId", authMiddleware(), removeMemberHandler)
		api.PATCH("/:token", authMiddleware(), updateTeamHandler)
		api.DELETE("/:token", authMiddleware(), deleteTeamHandler)
		api.POST("/:token/content", authMiddleware(), createPostHandler)
		api.GET("/:token/content", listPostsHandler)
		api.GET("/:token/content/:contentID", getPostHandler)
		api.POST("/:token/join", authMiddleware(), joinRequestHandler)
		api.GET("/:token/join-requests", authMiddleware(), listJoinRequestsHandler)
		api.PATCH("/:token/join-requests/:requestId", authMiddleware(), reviewJoinRequestHandler)
		api.DELETE("/:token/join-requests", authMiddleware(), cancelJoinRequestHandler)
	}
	// 内部専用（Nginx は /api/teams だけを転送するので外部からは届かない）
	internal := r.Group("/internal", internalKeyMiddleware())
	{
		internal.DELETE("/users/:userId", deleteUserDataHandler)
	}

	log.Println("team-service listening on :8080")
	// DoS 対策：r.Run はタイムアウト無しで待ち続けるため、http.Server で上限を付ける。
	// ヘッダーをゆっくり送る接続・つないだまま放置する接続を切る（本文は画像程度までなので読み取りは短め）。
	// WriteTimeout は付けない（アップロード後の処理が長いと、応答を返す前に切れてしまうため）
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
