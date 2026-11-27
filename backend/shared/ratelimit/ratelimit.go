// Package ratelimit は Redis を使ったユーザー単位のアップロード制限を提供する。
//
// nginx の制限は接続元 IP ごとだが、Tailscale 経由では全員が同じ IP に見えるため、
// 1 人がアップロードを続けると全員が巻き込まれる。ここではログインしたユーザー ID ごとに
//   - 1 時間あたり・1 日あたりの本数
//   - 同時に進めてよい本数
//
// を数え、ゲーム・静的サイト・動画のアップロードを合わせて制限する（キーをサービス間で共有する）。
//
// Redis に届かないときは制限せずに通す（Redis の障害でアップロード全体を止めないため。
// サーバー全体の同時数は nginx 側でも制限している）。
package ratelimit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
)

// UploadLimits はユーザー 1 人あたりのアップロード制限。0 以下の項目は制限しない。
type UploadLimits struct {
	PerHour    int // 1 時間あたりの本数（UPLOAD_LIMIT_PER_HOUR）
	PerDay     int // 1 日あたりの本数（UPLOAD_LIMIT_PER_DAY）
	Concurrent int // 同時に進めてよい本数（UPLOAD_LIMIT_CONCURRENT）

	// 作ってから NewAccountDays 日未満のアカウントには、PerHour / PerDay の代わりに下の値を使う
	// （アカウントを作り直して制限を逃れる使い方を抑える。Google ログインはアカウントをいくらでも作れるため）
	NewAccountDays    int // NEW_ACCOUNT_DAYS
	NewAccountPerHour int // UPLOAD_LIMIT_NEW_ACCOUNT_PER_HOUR
	NewAccountPerDay  int // UPLOAD_LIMIT_NEW_ACCOUNT_PER_DAY
}

// 既定値。普通に使う人は引っかからず、連続投稿で負荷をかける使い方は止まる程度。
const (
	DefaultPerHour           = 20
	DefaultPerDay            = 50
	DefaultConcurrent        = 2
	DefaultNewAccountDays    = 7
	DefaultNewAccountPerHour = 5
	DefaultNewAccountPerDay  = 10
)

// ForAccount はアカウントの新しさに応じた制限を返す（新しいアカウントなら 1 時間・1 日の本数を新規用に替える）。
func (l UploadLimits) ForAccount(isNew bool) UploadLimits {
	if isNew {
		l.PerHour = l.NewAccountPerHour
		l.PerDay = l.NewAccountPerDay
	}
	return l
}

// concurrentTTL は同時数カウンターの有効期限。プロセスが落ちて解放されなかった分は、この時間で消える。
// 最も長いアップロードの読み取り上限（60 分）より長くしておく。
const concurrentTTL = 2 * time.Hour

// LoadUploadLimitsFromEnv は環境変数から制限を読む。未設定や数値でない値は既定値を使う。
func LoadUploadLimitsFromEnv() UploadLimits {
	return UploadLimits{
		PerHour:           envInt("UPLOAD_LIMIT_PER_HOUR", DefaultPerHour),
		PerDay:            envInt("UPLOAD_LIMIT_PER_DAY", DefaultPerDay),
		Concurrent:        envInt("UPLOAD_LIMIT_CONCURRENT", DefaultConcurrent),
		NewAccountDays:    envInt("NEW_ACCOUNT_DAYS", DefaultNewAccountDays),
		NewAccountPerHour: envInt("UPLOAD_LIMIT_NEW_ACCOUNT_PER_HOUR", DefaultNewAccountPerHour),
		NewAccountPerDay:  envInt("UPLOAD_LIMIT_NEW_ACCOUNT_PER_DAY", DefaultNewAccountPerDay),
	}
}

// EnvInt は環境変数を整数として読む（未設定や数値でなければ def）。ほかのパッケージの設定読み込みでも使う。
func EnvInt(name string, def int) int { return envInt(name, def) }

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Decision は判定の結果。
type Decision struct {
	Allowed    bool
	Reason     string        // 断ったときの理由（利用者に見せる文言）
	RetryAfter time.Duration // 断ったとき、次に試せるまでの目安
	// Release は同時数の枠を返す関数。Allowed のときは処理の終わりに必ず呼ぶ（何度呼んでもよい）。
	Release func()
}

// 固定の時間枠で数える。INCR して最初の 1 回だけ期限を付け、現在の数と残り時間を返す
var windowScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then redis.call('PEXPIRE', KEYS[1], ARGV[1]); ttl = tonumber(ARGV[1]) end
return {n, ttl}
`)

// 同時数の枠を 1 つ取る。上限を超えたら戻して 0 を返す
var acquireScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
if n > tonumber(ARGV[1]) then
  redis.call('DECR', KEYS[1])
  return 0
end
return 1
`)

// 同時数の枠を 1 つ返す（0 未満にはしない）
var releaseScript = redis.NewScript(`
local n = redis.call('DECR', KEYS[1])
if n <= 0 then redis.call('DEL', KEYS[1]) end
return n
`)

func noop() {}

// CheckUpload は userID のアップロードを始めてよいかを判定する。
// err が nil でないのは Redis に届かなかったときで、そのとき Decision は「通す」になっている（呼び出し側はログだけ出す）。
func CheckUpload(ctx context.Context, rdb *redis.Client, limits UploadLimits, userID string) (Decision, error) {
	allow := Decision{Allowed: true, Release: noop}
	if rdb == nil || userID == "" {
		return allow, nil
	}

	// 1. 同時数（先に枠を取り、回数で断るときは返す）
	release := noop
	if limits.Concurrent > 0 {
		key := "ratelimit:upload:" + userID + ":concurrent"
		ok, err := acquireScript.Run(ctx, rdb, []string{key}, limits.Concurrent, concurrentTTL.Milliseconds()).Int()
		if err != nil {
			return allow, fmt.Errorf("acquire concurrent slot: %w", err)
		}
		if ok == 0 {
			return Decision{
				Allowed:    false,
				Reason:     fmt.Sprintf("同時に進められるアップロードは %d 件までです。今のアップロードが終わってから、もう一度お試しください。", limits.Concurrent),
				RetryAfter: time.Minute,
				Release:    noop,
			}, nil
		}
		var released bool
		release = func() {
			if released {
				return
			}
			released = true
			// リクエストが中断されていても枠は返したいので、元の ctx は使わない
			c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = releaseScript.Run(c, rdb, []string{key}).Err()
		}
	}

	// 2. 回数（1 時間・1 日）
	windows := []struct {
		limit  int
		window time.Duration
		suffix string
		label  string
	}{
		{limits.PerHour, time.Hour, "hour", "1 時間"},
		{limits.PerDay, 24 * time.Hour, "day", "1 日"},
	}
	for _, w := range windows {
		if w.limit <= 0 {
			continue
		}
		key := "ratelimit:upload:" + userID + ":" + w.suffix
		res, err := windowScript.Run(ctx, rdb, []string{key}, w.window.Milliseconds()).Int64Slice()
		if err != nil || len(res) != 2 {
			release()
			return allow, fmt.Errorf("count uploads per %s: %v", w.suffix, err)
		}
		if int(res[0]) > w.limit {
			release()
			retry := time.Duration(res[1]) * time.Millisecond
			return Decision{
				Allowed:    false,
				Reason:     fmt.Sprintf("アップロードは %sに %d 件までです。あと %sほどで、もう一度アップロードできます。", w.label, w.limit, humanize(retry)),
				RetryAfter: retry,
				Release:    noop,
			}, nil
		}
	}

	return Decision{Allowed: true, Release: release}, nil
}

// CheckAction は userID の action（画像の差し替えなど）を window あたり limit 回までに制限する。
// limit が 0 以下なら制限しない。Redis に届かないときは通し、err を返す（呼び出し側はログだけ出す）。
func CheckAction(ctx context.Context, rdb *redis.Client, userID, action string, limit int, window time.Duration, label string) (Decision, error) {
	allow := Decision{Allowed: true, Release: noop}
	if rdb == nil || userID == "" || limit <= 0 {
		return allow, nil
	}
	key := "ratelimit:action:" + action + ":" + userID
	res, err := windowScript.Run(ctx, rdb, []string{key}, window.Milliseconds()).Int64Slice()
	if err != nil || len(res) != 2 {
		return allow, fmt.Errorf("count action %s: %v", action, err)
	}
	if int(res[0]) > limit {
		retry := time.Duration(res[1]) * time.Millisecond
		return Decision{
			Allowed:    false,
			Reason:     fmt.Sprintf("%sは %sに %d 回までです。あと %sほどで、もう一度できます。", label, humanizeWindow(window), limit, humanize(retry)),
			RetryAfter: retry,
			Release:    noop,
		}, nil
	}
	return allow, nil
}

func humanizeWindow(d time.Duration) string {
	switch d {
	case time.Hour:
		return "1 時間"
	case 24 * time.Hour:
		return "1 日"
	}
	return humanize(d)
}

// IsNewAccount は Authorization ヘッダーの JWT の acctCreated（アカウントを作った時刻、Unix 秒）を見て、
// 作ってから days 日未満なら true を返す。
// 署名はこの前の認証ミドルウェアで検証済みである前提で、ここでは中身を読むだけにする。
// acctCreated の無いトークン（この仕組みより前に発行されたもの）は、既存のアカウントとして false を返す。
func IsNewAccount(authorization string, days int) bool {
	if days <= 0 {
		return false
	}
	created, ok := AccountCreatedAt(authorization)
	if !ok {
		return false
	}
	return time.Since(created) < time.Duration(days)*24*time.Hour
}

// AccountCreatedAt は Authorization ヘッダー（"Bearer <JWT>"）の JWT から acctCreated を取り出す（署名は検証しない）。
func AccountCreatedAt(authorization string) (time.Time, bool) {
	claims, ok := tokenClaims(authorization)
	if !ok {
		return time.Time{}, false
	}
	v, ok := claims["acctCreated"].(float64)
	if !ok || v <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(v), 0), true
}

// TokenBool は（検証済みの）JWT の真偽値の項目を読む。無ければ false。
func TokenBool(authorization, name string) bool {
	claims, ok := tokenClaims(authorization)
	if !ok {
		return false
	}
	v, _ := claims[name].(bool)
	return v
}

// tokenClaims は JWT の中身（2 つ目の部分）を読む。署名は検証しないので、検証済みのトークンにだけ使う。
func tokenClaims(authorization string) (map[string]interface{}, bool) {
	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authorization), "Bearer"))
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

// humanize は待ち時間を「◯時間◯分」「◯分」の形にする（1 分未満は 1 分）。
func humanize(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m < 1 {
		m = 1
	}
	if m < 60 {
		return fmt.Sprintf("%d 分", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d 時間", m/60)
	}
	return fmt.Sprintf("%d 時間 %d 分", m/60, m%60)
}
