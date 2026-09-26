package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	dbTeam    *pgxpool.Pool
	dbApp     *pgxpool.Pool
	jwtSecret []byte
)

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func generateToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("Failed to read random bytes: %v", err)
	}
	for i := range b {
		b[i] = tokenAlphabet[int(b[i])%len(tokenAlphabet)]
	}
	return string(b)
}

func connectDB(dsn string) *pgxpool.Pool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("Unable to connect to database %s: %v", dsn, err)
	}
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("Unable to ping database %s: %v", dsn, err)
	}
	return pool
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !verifyToken(c) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
		}
	}
}

// verifyToken runs JWT verification without aborting the request.
// Returns true if the token is valid and sets the "userID" value.
func verifyToken(c *gin.Context) bool {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return false
	}
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return false
	}
	if userID, exists := claims["user_id"]; exists {
		c.Set("userID", userID)
	} else if sub, exists := claims["sub"]; exists {
		c.Set("userID", sub)
	}
	return true
}

// NullableString は NULL 許容の文字列を、JSON ではプレーンな文字列（NULL の場合は空文字列）
// としてシリアライズする型です。sql.NullString が生成する {String, Valid} という
// JSON 形式を回避し、フロントエンド（React）が文字列として直接描画できるようにします。
type NullableString struct {
	sql.NullString
}

func (v NullableString) MarshalJSON() ([]byte, error) {
	if v.Valid {
		return json.Marshal(v.String)
	}
	return json.Marshal("")
}

type Team struct {
	ID          string         `json:"id"`
	Token       string         `json:"token"`
	Name        string         `json:"name"`
	Description NullableString `json:"description"`
	IsPublic    bool           `json:"is_public"`
	AutoApprove bool           `json:"auto_approve"`
	AllowMemberInvite bool       `json:"allow_member_invite"`
	CreatedBy   NullableString `json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
}

// createTeamHandler: JWT auth. Create a team and return it with token.
func createTeamHandler(c *gin.Context) {
	userID := c.GetString("userID")
	var req struct {
		Name              string `json:"name" binding:"required"`
		Description       string `json:"description"`
		IsPublic          *bool  `json:"is_public"`
		AutoApprove       *bool  `json:"auto_approve"`
		AllowMemberInvite *bool  `json:"allow_member_invite"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	isPublic := false
	if req.IsPublic != nil {
		isPublic = *req.IsPublic
	}
	autoApprove := false
	if req.AutoApprove != nil {
		autoApprove = *req.AutoApprove
	}
	// 自動認証は公開チームのみ有効。非公開チームでは強制的に false。
	if !isPublic {
		autoApprove = false
	}
	allowMemberInvite := false
	if req.AllowMemberInvite != nil {
		allowMemberInvite = *req.AllowMemberInvite
	}

	token := generateToken()
	var team Team
	for i := 0; i < 5; i++ {
		err := dbTeam.QueryRow(c.Request.Context(),
			"INSERT INTO teams (token, name, description, is_public, auto_approve, allow_member_invite, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, token, name, description, is_public, auto_approve, allow_member_invite, created_by, created_at",
			token, req.Name, req.Description, isPublic, autoApprove, allowMemberInvite, userID).
			Scan(&team.ID, &team.Token, &team.Name, &team.Description, &team.IsPublic, &team.AutoApprove, &team.AllowMemberInvite, &team.CreatedBy, &team.CreatedAt)
		if err != nil {
			if strings.Contains(err.Error(), "unique") {
				token = generateToken()
				continue
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		break
	}

	_, _ = dbTeam.Exec(c.Request.Context(),
		"INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,'owner') ON CONFLICT (team_id, user_id) DO NOTHING",
		team.ID, userID)

	c.JSON(http.StatusCreated, team)
}

// getTeamByTokenHandler: no auth. Get a team by its token.
func getTeamByTokenHandler(c *gin.Context) {
	token := c.Param("token")
	t := &Team{}
	err := dbTeam.QueryRow(c.Request.Context(),
		"SELECT id, token, name, description, is_public, auto_approve, allow_member_invite, created_by, created_at FROM teams WHERE token = $1",
		token).Scan(&t.ID, &t.Token, &t.Name, &t.Description, &t.IsPublic, &t.AutoApprove, &t.AllowMemberInvite, &t.CreatedBy, &t.CreatedAt)
	if err == pgx.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "team not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, t)
}

// listPublicTeamsHandler: no auth. List public teams.
func listPublicTeamsHandler(c *gin.Context) {
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT id, token, name, description, is_public, auto_approve, allow_member_invite, created_by, created_at FROM teams WHERE is_public = true ORDER BY created_at DESC")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var teams []Team
	for rows.Next() {
		t := &Team{}
		if err := rows.Scan(&t.ID, &t.Token, &t.Name, &t.Description, &t.IsPublic, &t.AutoApprove, &t.AllowMemberInvite, &t.CreatedBy, &t.CreatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		teams = append(teams, *t)
	}
	c.JSON(http.StatusOK, teams)
}

// listMyTeamsHandler: JWT auth. List teams the user is a member of.
func listMyTeamsHandler(c *gin.Context) {
	userID := c.GetString("userID")
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT t.id, t.token, t.name, t.description, t.is_public, t.auto_approve, t.allow_member_invite, t.created_by, t.created_at FROM teams t JOIN team_members m ON m.team_id = t.id WHERE m.user_id = $1 ORDER BY t.created_at DESC",
		userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var teams []Team
	for rows.Next() {
		t := &Team{}
		if err := rows.Scan(&t.ID, &t.Token, &t.Name, &t.Description, &t.IsPublic, &t.AutoApprove, &t.AllowMemberInvite, &t.CreatedBy, &t.CreatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		teams = append(teams, *t)
	}
	c.JSON(http.StatusOK, teams)
}

type Member struct {
	UserID   string    `json:"user_id"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

func callerRole(c *gin.Context, token string) (string, bool) {
	userID := c.GetString("userID")
	var role string
	err := dbTeam.QueryRow(c.Request.Context(),
		"SELECT role FROM team_members WHERE team_id = (SELECT id FROM teams WHERE token = $1) AND user_id = $2",
		token, userID).Scan(&role)
	if err == pgx.ErrNoRows {
		return "", false
	}
	if err != nil {
		return "", false
	}
	return role, true
}

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

func requireTeamRole(c *gin.Context, required string) bool {
	token := c.Param("token")
	role, ok := callerRole(c, token)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return false
	}
	if roleRank(role) < roleRank(required) {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permission"})
		return false
	}
	return true
}

// getMembersHandler: JWT auth, member role. List members of a team.
func getMembersHandler(c *gin.Context) {
	if !requireTeamRole(c, "member") {
		return
	}
	token := c.Param("token")
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT user_id, role, joined_at FROM team_members WHERE team_id = (SELECT id FROM teams WHERE token = $1) ORDER BY joined_at",
		token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var members []Member
	for rows.Next() {
		m := &Member{}
		if err := rows.Scan(&m.UserID, &m.Role, &m.JoinedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		members = append(members, *m)
	}
	c.JSON(http.StatusOK, members)
}

// addMemberHandler: JWT auth, admin role. Add a member to a team.
func addMemberHandler(c *gin.Context) {
	if !requireTeamRole(c, "admin") {
		return
	}
	token := c.Param("token")
	var req struct {
		UserID string `json:"user_id" binding:"required"`
		Role   string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id is required"})
		return
	}
	role := "member"
	if req.Role != "" {
		role = req.Role
	}
	_, err := dbTeam.Exec(c.Request.Context(),
		"INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,$3) ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role",
		token, req.UserID, role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user_id": req.UserID, "role": role})
}

// removeMemberHandler: JWT auth, admin role. Remove a member from a team.
func removeMemberHandler(c *gin.Context) {
	if !requireTeamRole(c, "admin") {
		return
	}
	token := c.Param("token")
	userID := c.Param("userId")
	_, err := dbTeam.Exec(c.Request.Context(),
		"DELETE FROM team_members WHERE team_id = (SELECT id FROM teams WHERE token = $1) AND user_id = $2",
		token, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "member removed"})
}

// updateTeamHandler: JWT auth, owner role. Update team metadata.
func updateTeamHandler(c *gin.Context) {
	if !requireTeamRole(c, "owner") {
		return
	}
	token := c.Param("token")
	var req struct {
		Name                string   `json:"name"`
		Description         *string  `json:"description"`
		IsPublic            *bool    `json:"is_public"`
		AutoApprove         *bool    `json:"auto_approve"`
		AllowMemberInvite   *bool    `json:"allow_member_invite"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	var set []string
	var args []interface{}
	args = append(args, token)
	if req.Name != "" {
		set = append(set, "name = $"+strconv.Itoa(len(args)+1))
		args = append(args, req.Name)
	}
	set = append(set, "description = $"+strconv.Itoa(len(args)+1))
	args = append(args, req.Description)
	if req.IsPublic != nil {
		set = append(set, "is_public = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.IsPublic)
	}
	if req.AutoApprove != nil {
		// 自動認証は公開チームのみ有効。非公開に切り替える場合は強制的に false。
		val := *req.AutoApprove
		if req.IsPublic != nil && !*req.IsPublic {
			val = false
		}
		set = append(set, "auto_approve = $"+strconv.Itoa(len(args)+1))
		args = append(args, val)
	}
	if req.AllowMemberInvite != nil {
		set = append(set, "allow_member_invite = $"+strconv.Itoa(len(args)+1))
		args = append(args, *req.AllowMemberInvite)
	}
	if len(set) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}
	query := "UPDATE teams SET " + strings.Join(set, ", ") + " WHERE token = $1"
	_, err := dbTeam.Exec(c.Request.Context(), query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "team updated"})
}

// deleteTeamHandler: JWT auth, owner role. Delete a team and its members.
func deleteTeamHandler(c *gin.Context) {
	if !requireTeamRole(c, "owner") {
		return
	}
	token := c.Param("token")
	_, err := dbTeam.Exec(c.Request.Context(), "DELETE FROM teams WHERE token = $1", token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "team deleted"})
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

// resolveTeam: no auth. Fetch a team by token. Returns nil and writes a response if not found.
func resolveTeam(c *gin.Context, token string) *Team {
	t := &Team{}
	err := dbTeam.QueryRow(c.Request.Context(),
		"SELECT id, token, name, description, is_public, auto_approve, allow_member_invite, created_by, created_at FROM teams WHERE token = $1",
		token).Scan(&t.ID, &t.Token, &t.Name, &t.Description, &t.IsPublic, &t.AutoApprove, &t.AllowMemberInvite, &t.CreatedBy, &t.CreatedAt)
	if err == pgx.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "team not found"})
		return nil
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return nil
	}
	return t
}

// contentAccessAllowed: decides if the caller may view a team's content.
// Public teams: no auth. Private teams: JWT + member role.
// Returns false (after writing a response) if access is denied.
func contentAccessAllowed(c *gin.Context, token string) bool {
	team := resolveTeam(c, token)
	if team == nil {
		return false
	}
	if team.IsPublic {
		return true
	}
	if !verifyToken(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return false
	}
	role, ok := callerRole(c, token)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return false
	}
	if roleRank(role) < roleRank("member") {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permission"})
		return false
	}
	return true
}

// Post is a team-native content item.
type Post struct {
	ID         string    `json:"id"`
	TeamID     string    `json:"team_id"`
	AuthorID   string    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

// fetchUsername: best-effort lookup of a username from app-db. Returns "" if unavailable.
func fetchUsername(c *gin.Context, authorID string) string {
	if dbApp == nil {
		return ""
	}
	var username string
	err := dbApp.QueryRow(c.Request.Context(),
		"SELECT COALESCE(username, '') FROM users WHERE id = $1", authorID).Scan(&username)
	if err != nil {
		return ""
	}
	return username
}

// createPostHandler: JWT auth, member role. Create a post in a team.
func createPostHandler(c *gin.Context) {
	if !requireTeamRole(c, "member") {
		return
	}
	token := c.Param("token")
	userID := c.GetString("userID")
	var req struct {
		Title string `json:"title" binding:"required"`
		Body  string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	var teamID string
	err := dbTeam.QueryRow(c.Request.Context(),
		"SELECT id FROM teams WHERE token = $1", token).Scan(&teamID)
	if err == pgx.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "team not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var post Post
	err = dbTeam.QueryRow(c.Request.Context(),
		"INSERT INTO team_posts (team_id, author_id, title, body) VALUES ($1,$2,$3,$4) RETURNING id, team_id, author_id, title, body, created_at",
		teamID, userID, req.Title, req.Body).
		Scan(&post.ID, &post.TeamID, &post.AuthorID, &post.Title, &post.Body, &post.CreatedAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	post.AuthorName = fetchUsername(c, userID)
	c.JSON(http.StatusCreated, post)
}

// listPostsHandler: auth depends on team.is_public. List posts in a team.
func listPostsHandler(c *gin.Context) {
	token := c.Param("token")
	if !contentAccessAllowed(c, token) {
		return
	}
	userID := c.GetString("userID")
	authed := userID != ""
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT p.id, p.team_id, p.author_id, p.title, p.body, p.created_at FROM team_posts p WHERE p.team_id = (SELECT id FROM teams WHERE token = $1) ORDER BY p.created_at DESC",
		token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		var post Post
		if err := rows.Scan(&post.ID, &post.TeamID, &post.AuthorID, &post.Title, &post.Body, &post.CreatedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if authed {
			post.AuthorName = fetchUsername(c, post.AuthorID)
		}
		posts = append(posts, post)
	}
	c.JSON(http.StatusOK, posts)
}

// getPostHandler: auth depends on team.is_public. Get a single post.
func getPostHandler(c *gin.Context) {
	token := c.Param("token")
	contentID := c.Param("contentID")
	if !contentAccessAllowed(c, token) {
		return
	}
	userID := c.GetString("userID")
	var post Post
	err := dbTeam.QueryRow(c.Request.Context(),
		"SELECT p.id, p.team_id, p.author_id, p.title, p.body, p.created_at FROM team_posts p WHERE p.id = $1 AND p.team_id = (SELECT id FROM teams WHERE token = $2)",
		contentID, token).Scan(&post.ID, &post.TeamID, &post.AuthorID, &post.Title, &post.Body, &post.CreatedAt)
	if err == pgx.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "post not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if userID != "" {
		post.AuthorName = fetchUsername(c, post.AuthorID)
	}
	c.JSON(http.StatusOK, post)
}

// joinRequestHandler: JWT auth. Join a team.
// Public + auto_approve: immediately inserts into team_members.
// Otherwise: inserts a pending join request (or rejects if already member/pending).
func joinRequestHandler(c *gin.Context) {
	token := c.Param("token")
	team := resolveTeam(c, token)
	if team == nil {
		return
	}
	if !verifyToken(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	userID := c.GetString("userID")

	// 既にメンバーか？
	var existingRole string
	err := dbTeam.QueryRow(c.Request.Context(),
		"SELECT role FROM team_members WHERE team_id = $1 AND user_id = $2", team.ID, userID).Scan(&existingRole)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"status": "already_member", "role": existingRole})
		return
	}
	if err != pgx.ErrNoRows {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 自動承認（公開チームかつ auto_approve）
	if team.IsPublic && team.AutoApprove {
		role := "member"
		_, err := dbTeam.Exec(c.Request.Context(),
			"INSERT INTO team_members (team_id, user_id, role) VALUES ($1,$2,$3) ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role",
			team.ID, userID, role)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "approved", "role": role})
		return
	}

	// 非公開チームは加入自体を拒否
	if !team.IsPublic {
		c.JSON(http.StatusForbidden, gin.H{"error": "private team cannot be joined"})
		return
	}

	// 公開チームかつ手動承認: リクエスト作成（重複は unique 制約で防ぐ）
	var reqID string
	err = dbTeam.QueryRow(c.Request.Context(),
		"INSERT INTO team_join_requests (team_id, user_id, status) VALUES ($1,$2,'pending') RETURNING id",
		team.ID, userID).Scan(&reqID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "pending", "request_id": reqID})
}

// listJoinRequestsHandler: JWT auth, owner/admin only.
func listJoinRequestsHandler(c *gin.Context) {
	token := c.Param("token")
	if !requireTeamRole(c, "admin") {
		return
	}
	rows, err := dbTeam.Query(c.Request.Context(),
		"SELECT id, team_id, user_id, status, requested_at, reviewed_at, reviewed_by FROM team_join_requests WHERE team_id = (SELECT id FROM teams WHERE token = $1) ORDER BY requested_at DESC",
		token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var reqs []JoinRequest
	for rows.Next() {
		var r JoinRequest
		if err := rows.Scan(&r.ID, &r.TeamID, &r.UserID, &r.Status, &r.RequestedAt, &r.ReviewedAt, &r.ReviewedBy); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		reqs = append(reqs, r)
	}
	c.JSON(http.StatusOK, reqs)
}

// reviewJoinRequestHandler: JWT auth, owner/admin only. Approve or reject a join request.
func reviewJoinRequestHandler(c *gin.Context) {
	token := c.Param("token")
	if !requireTeamRole(c, "admin") {
		return
	}
	requestID := c.Param("requestId")
	action := c.Query("action") // "approve" or "reject"
	if action != "approve" && action != "reject" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action=approve or action=reject is required"})
		return
	}
	userID := c.GetString("userID")
	newStatus := "approved"
	if action == "reject" {
		newStatus = "rejected"
	}
	_, err := dbTeam.Exec(c.Request.Context(),
		"UPDATE team_join_requests SET status = $1, reviewed_at = NOW(), reviewed_by = $2 WHERE id = $3 AND team_id = (SELECT id FROM teams WHERE token = $4)",
		newStatus, userID, requestID, token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if action == "approve" {
		_, err = dbTeam.Exec(c.Request.Context(),
			"INSERT INTO team_members (team_id, user_id, role) VALUES ((SELECT id FROM teams WHERE token = $1), $2, 'member') ON CONFLICT (team_id, user_id) DO UPDATE SET role = EXCLUDED.role",
			token, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": newStatus})
}

// cancelJoinRequestHandler: JWT auth. Cancel own pending join request.
func cancelJoinRequestHandler(c *gin.Context) {
	token := c.Param("token")
	if !verifyToken(c) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	userID := c.GetString("userID")
	_, err := dbTeam.Exec(c.Request.Context(),
		"UPDATE team_join_requests SET status = 'cancelled' WHERE team_id = (SELECT id FROM teams WHERE token = $1) AND user_id = $2 AND status = 'pending'",
		token, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "join request cancelled"})
}

// teamPermissionHandler: returns what the caller can do with a team.
// No auth required: public teams are viewable by anyone.
// Posting requires membership (member+).
func teamPermissionHandler(c *gin.Context) {
	token := c.Param("token")
	team := resolveTeam(c, token)
	if team == nil {
		return
	}
	resp := gin.H{"is_public": team.IsPublic, "can_view": true, "can_post": false, "role": ""}
	if !team.IsPublic {
		resp["can_view"] = false
		c.JSON(http.StatusOK, resp)
		return
	}
	if !verifyToken(c) {
		c.JSON(http.StatusOK, resp)
		return
	}
	role, ok := callerRole(c, token)
	if ok {
		resp["role"] = role
		if roleRank(role) >= roleRank("member") {
			resp["can_post"] = true
		}
	}
	c.JSON(http.StatusOK, resp)
}

func main() {
	dbTeam = connectDB(os.Getenv("TEAM_DATABASE_URL"))
	if os.Getenv("APP_DATABASE_URL") != "" {
		dbApp = connectDB(os.Getenv("APP_DATABASE_URL"))
	}
	jwtSecret = []byte(os.Getenv("JWT_SECRET"))

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

	log.Println("team-service listening on :8080")
	log.Fatal(r.Run(":8080"))
}
