// Package chunkupload は大きなファイルを分割して受け取る（分割アップロード）。
//
// Cloudflare の無料プランは 1 リクエスト 100 MB までなので、1 GiB の動画・ゲームはそのままでは通らない。
// ブラウザはファイルを ChunkSize（既定 50 MiB）ごとに送り、最後に通常のアップロード API へ
// ファイルの代わりに upload_id を付けて送る。API は組み立て済みの一時ファイルを、
// multipart で受け取ったファイルと同じように扱う。
//
//	POST /…/upload/chunks              {"filename","size"}      → {"upload_id","chunk_size","chunks"}
//	PUT  /…/upload/chunks/<id>/<index>  本文 = その部分のバイト列 → {"received","chunks"}
//	POST /…/upload                      multipart（upload_id と、title などのフォーム項目）
//
// 一時ファイルはコンテナ内の Dir に置き、Idle（既定 1 時間）使われなかったセッションは消す。
// gin に依存しないよう net/http のハンドラーとして書き、各サービスは gin.WrapF でつなぐ。
// ユーザー ID は WithUser でリクエストのコンテキストに入れてから渡す。
package chunkupload

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultChunkSize は 1 回に送る大きさ。Cloudflare の 100 MB 制限より十分小さくする。
const DefaultChunkSize int64 = 50 << 20

// エラー。ハンドラーは HTTP ステータスに読み替える。
var (
	ErrNotFound        = errors.New("upload session not found")
	ErrTooLarge        = errors.New("file is too large")
	ErrTooManySessions = errors.New("too many upload sessions")
	ErrBadChunk        = errors.New("invalid chunk")
	ErrIncomplete      = errors.New("upload is not complete")
)

// Store は分割アップロードのセッションを保存する場所。
type Store struct {
	Dir                string        // 一時ファイルの置き場所
	Kind               string        // "game" / "static-site" / "video"（別のサービスのセッションを取り違えないため）
	ChunkSize          int64         // 1 回に送る大きさ
	MaxSize            int64         // 1 ファイルの上限
	MaxSessionsPerUser int           // 1 人が同時に開けるセッション数
	Idle               time.Duration // これより長く使われなかったセッションは消す

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Session はセッションの情報（meta.json に保存する）。
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Kind      string    `json:"kind"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	ChunkSize int64     `json:"chunk_size"`
	Received  []bool    `json:"received"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Chunks は分割数を返す。
func (s *Session) Chunks() int { return len(s.Received) }

// Complete はすべての部分を受け取ったかを返す。
func (s *Session) Complete() bool {
	for _, r := range s.Received {
		if !r {
			return false
		}
	}
	return true
}

// New は Store を作り、古いセッションを消す処理を裏で動かす。
func New(dir, kind string, maxSize int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{
		Dir:                dir,
		Kind:               kind,
		ChunkSize:          DefaultChunkSize,
		MaxSize:            maxSize,
		MaxSessionsPerUser: 3,
		Idle:               time.Hour,
		locks:              map[string]*sync.Mutex{},
	}
	go s.janitor()
	return s, nil
}

func (s *Store) lock(id string) func() {
	s.mu.Lock()
	m, ok := s.locks[id]
	if !ok {
		m = &sync.Mutex{}
		s.locks[id] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *Store) sessionDir(id string) string { return filepath.Join(s.Dir, id) }
func (s *Store) dataPath(id string) string   { return filepath.Join(s.Dir, id, "data") }
func (s *Store) metaPath(id string) string   { return filepath.Join(s.Dir, id, "meta.json") }

func (s *Store) load(id string) (*Session, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		return nil, ErrNotFound
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		return nil, ErrNotFound
	}
	return &sess, nil
}

func (s *Store) save(sess *Session) error {
	b, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	tmp := s.metaPath(sess.ID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath(sess.ID))
}

// sessionsOf は userID の（このサービスの）セッションを返す。
func (s *Store) sessionsOf(userID string) []*Session {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []*Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if sess, err := s.load(e.Name()); err == nil && sess.UserID == userID {
			out = append(out, sess)
		}
	}
	return out
}

// Start は新しいセッションを作る。
func (s *Store) Start(userID, filename string, size int64) (*Session, error) {
	if size <= 0 {
		return nil, ErrBadChunk
	}
	if s.MaxSize > 0 && size > s.MaxSize {
		return nil, ErrTooLarge
	}
	if s.MaxSessionsPerUser > 0 && len(s.sessionsOf(userID)) >= s.MaxSessionsPerUser {
		return nil, ErrTooManySessions
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(idBytes)
	if err := os.MkdirAll(s.sessionDir(id), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.dataPath(id), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		_ = os.RemoveAll(s.sessionDir(id))
		return nil, err
	}
	// 先に大きさだけ決めておく（中身はまだ書かないので、実際のディスクは書いた分しか使わない）
	if err := f.Truncate(size); err != nil {
		f.Close()
		_ = os.RemoveAll(s.sessionDir(id))
		return nil, err
	}
	f.Close()
	chunks := int((size + s.ChunkSize - 1) / s.ChunkSize)
	now := time.Now().UTC()
	sess := &Session{
		ID: id, UserID: userID, Kind: s.Kind, Filename: filename, Size: size,
		ChunkSize: s.ChunkSize, Received: make([]bool, chunks), CreatedAt: now, UpdatedAt: now,
	}
	if err := s.save(sess); err != nil {
		_ = os.RemoveAll(s.sessionDir(id))
		return nil, err
	}
	return sess, nil
}

// WriteChunk は index 番目の部分を body から書く（同じ部分を送り直してもよい）。
func (s *Store) WriteChunk(userID, id string, index int, body io.Reader) (*Session, error) {
	unlock := s.lock(id)
	defer unlock()
	sess, err := s.load(id)
	if err != nil || sess.UserID != userID || sess.Kind != s.Kind {
		return nil, ErrNotFound
	}
	if index < 0 || index >= sess.Chunks() {
		return nil, ErrBadChunk
	}
	offset := int64(index) * sess.ChunkSize
	want := sess.ChunkSize
	if rest := sess.Size - offset; rest < want {
		want = rest
	}
	f, err := os.OpenFile(s.dataPath(id), os.O_WRONLY, 0o600)
	if err != nil {
		return nil, ErrNotFound
	}
	defer f.Close()
	n, err := io.CopyN(io.NewOffsetWriter(f, offset), body, want)
	if err != nil || n != want {
		return nil, fmt.Errorf("%w: got %d of %d bytes", ErrBadChunk, n, want)
	}
	// 決まった大きさより多く送られてきたら受け付けない
	var extra [1]byte
	if m, _ := body.Read(extra[:]); m > 0 {
		return nil, fmt.Errorf("%w: chunk is larger than %d bytes", ErrBadChunk, want)
	}
	sess.Received[index] = true
	sess.UpdatedAt = time.Now().UTC()
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// File は組み立て済みのファイル。multipart.File と同じように読める（Read / ReadAt / Seek / Close）。
type File struct {
	*os.File
	Session *Session
	store   *Store
}

// Close はファイルを閉じ、セッションを消す。
func (f *File) Close() error {
	err := f.File.Close()
	f.store.Remove(f.Session.ID)
	return err
}

// Open はすべての部分を受け取ったセッションのファイルを開く。Close するとセッションごと消える。
func (s *Store) Open(userID, id string) (*File, error) {
	unlock := s.lock(id)
	defer unlock()
	sess, err := s.load(id)
	if err != nil || sess.UserID != userID || sess.Kind != s.Kind {
		return nil, ErrNotFound
	}
	if !sess.Complete() {
		return nil, ErrIncomplete
	}
	f, err := os.Open(s.dataPath(id))
	if err != nil {
		return nil, ErrNotFound
	}
	return &File{File: f, Session: sess, store: s}, nil
}

// Remove はセッションを消す。
func (s *Store) Remove(id string) {
	if !validID(id) {
		return
	}
	_ = os.RemoveAll(s.sessionDir(id))
	s.mu.Lock()
	delete(s.locks, id)
	s.mu.Unlock()
}

func (s *Store) janitor() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for range t.C {
		s.cleanup()
	}
}

func (s *Store) cleanup() {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sess, err := s.load(e.Name())
		if err != nil {
			// meta.json が無い・壊れているものは、作成直後の可能性があるので古いものだけ消す
			if info, ierr := e.Info(); ierr == nil && time.Since(info.ModTime()) > s.Idle {
				_ = os.RemoveAll(filepath.Join(s.Dir, e.Name()))
			}
			continue
		}
		if time.Since(sess.UpdatedAt) > s.Idle {
			log.Printf("chunkupload: removing idle upload session %s (user %s)", sess.ID, sess.UserID)
			s.Remove(sess.ID)
		}
	}
}

// ---- HTTP ハンドラー ----

type ctxKey struct{}

// WithUser はリクエストのコンテキストにユーザー ID を入れる（gin のミドルウェアから呼ぶ）。
func WithUser(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, userID)
}

func userFrom(r *http.Request) string {
	v, _ := r.Context().Value(ctxKey{}).(string)
	return v
}

// PreCheck はセッションを作る前の確認（保存量・ディスク・処理待ちなど）。
// 断るなら HTTP ステータスと文言を返す（status 0 なら通す）。
type PreCheck func(r *http.Request, userID string, size int64) (status int, message string)

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "アップロードが見つかりません。最初からやり直してください。"})
	case errors.Is(err, ErrTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "ファイルが大きすぎます。"})
	case errors.Is(err, ErrTooManySessions):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "同時に進められるアップロードの数を超えています。今のアップロードが終わってからお試しください。"})
	case errors.Is(err, ErrBadChunk):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "送られてきたデータが正しくありません。もう一度お試しください。"})
	case errors.Is(err, ErrIncomplete):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ファイルの送信が終わっていません。"})
	default:
		log.Printf("chunkupload: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "アップロードの処理に失敗しました。"})
	}
}

// HandleStart は POST /…/upload/chunks を処理する。
func (s *Store) HandleStart(check PreCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := userFrom(r)
		if userID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください。"})
			return
		}
		var req struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Size <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "filename と size を指定してください。"})
			return
		}
		if s.MaxSize > 0 && req.Size > s.MaxSize {
			writeErr(w, ErrTooLarge)
			return
		}
		if check != nil {
			if status, msg := check(r, userID, req.Size); status != 0 {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}
		}
		sess, err := s.Start(userID, req.Filename, req.Size)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"upload_id": sess.ID, "chunk_size": sess.ChunkSize, "chunks": sess.Chunks(),
		})
	}
}

// HandleChunk は PUT /…/upload/chunks/<id>/<index> を処理する（パスの最後の 2 つを使う）。
func (s *Store) HandleChunk(w http.ResponseWriter, r *http.Request) {
	userID := userFrom(r)
	if userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "ログインしてください。"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		writeErr(w, ErrNotFound)
		return
	}
	id := parts[len(parts)-2]
	index, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		writeErr(w, ErrBadChunk)
		return
	}
	// 1 回分より大きい本文は読まない（WriteChunk でも検査する）
	r.Body = http.MaxBytesReader(w, r.Body, s.ChunkSize+1)
	sess, err := s.WriteChunk(userID, id, index, r.Body)
	if err != nil {
		writeErr(w, err)
		return
	}
	received := 0
	for _, ok := range sess.Received {
		if ok {
			received++
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"received": received, "chunks": sess.Chunks()})
}

// HTTPStatus は Open などのエラーを HTTP ステータスと文言に読み替える（アップロード API の本体から使う）。
func HTTPStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusBadRequest, "アップロードが見つかりません。最初からやり直してください。"
	case errors.Is(err, ErrIncomplete):
		return http.StatusBadRequest, "ファイルの送信が終わっていません。"
	default:
		return http.StatusInternalServerError, "アップロードの処理に失敗しました。"
	}
}
