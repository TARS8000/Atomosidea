package main

// guard.go：DoS 対策の状態を見張る。
//   - ディスクの空き（mon-backend のコンテナから見た / ＝ Docker のデータ領域。VM のディスク）
//   - 検査待ち・動画の変換待ち（Redis のキューの長さ。backend/shared/uploadguard と同じ数え方）
//   - nginx（atmosidea-frontend）のアクセスログに出た 429 / 413 / 503 / 5xx の件数（直近 5 分・1 時間）
//
// GET /api/guard/status で現在の状態を返し、ALERT_DISCORD_WEBHOOK_URL があれば、しきい値を超えたときと
// 戻ったときに Discord へ知らせる（同じ種類の通知は ALERT_COOLDOWN_MINUTES 分（既定 30）に 1 回まで）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// GuardStatus は /api/guard/status の応答。
type GuardStatus struct {
	CheckedAt        time.Time      `json:"checked_at"`
	DiskTotal        int64          `json:"disk_total"`
	DiskFree         int64          `json:"disk_free"`
	ScanBacklog      int64          `json:"scan_backlog"`      // -1 は読めなかった
	TranscodeBacklog int64          `json:"transcode_backlog"` // -1 は読めなかった
	Last5m           map[string]int `json:"last_5m"`           // "429" / "413" / "503" / "5xx" / "total"
	Last1h           map[string]int `json:"last_1h"`
	Alerts           []string       `json:"alerts"` // いま超えているしきい値（日本語）
}

type alertThresholds struct {
	minFreeDisk      int64
	scanBacklog      int64
	transcodeBacklog int64
	tooMany5m        int
	serverErr5m      int
	cooldown         time.Duration
	webhook          string
}

func envInt64(name string, def int64) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64); err == nil && v > 0 {
		return v
	}
	return def
}

func loadAlertThresholds() alertThresholds {
	return alertThresholds{
		// アップロードを断り始める値（UPLOAD_MIN_FREE_DISK_BYTES、既定 10 GiB）より先に知らせる
		minFreeDisk:      envInt64("ALERT_MIN_FREE_DISK_BYTES", 15<<30),
		scanBacklog:      envInt64("ALERT_SCAN_BACKLOG", 15),
		transcodeBacklog: envInt64("ALERT_TRANSCODE_BACKLOG", 8),
		tooMany5m:        int(envInt64("ALERT_429_PER_5MIN", 300)),
		serverErr5m:      int(envInt64("ALERT_5XX_PER_5MIN", 50)),
		cooldown:         time.Duration(envInt64("ALERT_COOLDOWN_MINUTES", 30)) * time.Minute,
		webhook:          strings.TrimSpace(os.Getenv("ALERT_DISCORD_WEBHOOK_URL")),
	}
}

// guardWatcher は 1 分ごとに状態を集め、最新の結果を持つ。
type guardWatcher struct {
	cli *client.Client
	th  alertThresholds

	mu       sync.Mutex
	status   GuardStatus
	buckets  map[int64]map[string]int // 分（Unix 秒 / 60）→ ステータスごとの件数
	lastRead time.Time
	lastSent map[string]time.Time // 通知の種類 → 最後に送った時刻
	active   map[string]bool      // いま超えているしきい値（戻ったときの通知用）
}

func startGuardWatcher(cli *client.Client) *guardWatcher {
	g := &guardWatcher{
		cli:      cli,
		th:       loadAlertThresholds(),
		buckets:  map[int64]map[string]int{},
		lastSent: map[string]time.Time{},
		active:   map[string]bool{},
	}
	if g.th.webhook == "" {
		log.Println("Guard watcher: ALERT_DISCORD_WEBHOOK_URL is not set; alerts are shown on the dashboard only")
	}
	go func() {
		g.collect()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			g.collect()
		}
	}()
	return g
}

func (g *guardWatcher) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		st := g.status
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	}
}

func (g *guardWatcher) collect() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	st := GuardStatus{CheckedAt: time.Now().UTC(), ScanBacklog: -1, TranscodeBacklog: -1}

	// 1. ディスク
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/", &fs); err == nil {
		st.DiskTotal = int64(fs.Blocks) * int64(fs.Bsize)
		st.DiskFree = int64(fs.Bavail) * int64(fs.Bsize)
	} else {
		log.Printf("Guard watcher: statfs failed: %v", err)
	}

	// 2. 処理待ち
	if scan, tr, err := readBacklog(ctx); err == nil {
		st.ScanBacklog, st.TranscodeBacklog = scan, tr
	} else {
		log.Printf("Guard watcher: read backlog failed: %v", err)
	}

	// 3. nginx のアクセスログ（前回読んだところから）
	g.readAccessLog(ctx)
	g.mu.Lock()
	st.Last5m = g.sumBuckets(5)
	st.Last1h = g.sumBuckets(60)
	g.mu.Unlock()

	// 4. しきい値
	alerts := map[string]string{}
	if st.DiskTotal > 0 && st.DiskFree < g.th.minFreeDisk {
		alerts["disk"] = fmt.Sprintf("ディスクの空きが少なくなっています（残り %s）", fmtBytes(st.DiskFree))
	}
	if st.ScanBacklog >= g.th.scanBacklog {
		alerts["scan"] = fmt.Sprintf("検査待ちが溜まっています（%d 件）", st.ScanBacklog)
	}
	if st.TranscodeBacklog >= g.th.transcodeBacklog {
		alerts["transcode"] = fmt.Sprintf("動画の変換待ちが溜まっています（%d 件）", st.TranscodeBacklog)
	}
	if st.Last5m["429"] >= g.th.tooMany5m {
		alerts["429"] = fmt.Sprintf("回数制限（429）が多く出ています（直近 5 分で %d 件）。攻撃か、制限が厳しすぎる可能性があります", st.Last5m["429"])
	}
	if st.Last5m["5xx"] >= g.th.serverErr5m {
		alerts["5xx"] = fmt.Sprintf("サーバーエラー（5xx）が多く出ています（直近 5 分で %d 件）", st.Last5m["5xx"])
	}
	keys := make([]string, 0, len(alerts))
	for k := range alerts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st.Alerts = append(st.Alerts, alerts[k])
	}

	g.mu.Lock()
	g.status = st
	g.mu.Unlock()

	g.notify(alerts)
}

// notify はしきい値を超えた・戻ったことを Discord に知らせる。
func (g *guardWatcher) notify(alerts map[string]string) {
	now := time.Now()
	var lines []string
	for k, msg := range alerts {
		if last, ok := g.lastSent[k]; ok && now.Sub(last) < g.th.cooldown {
			g.active[k] = true
			continue
		}
		lines = append(lines, "⚠️ "+msg)
		g.lastSent[k] = now
		g.active[k] = true
	}
	for k := range g.active {
		if _, still := alerts[k]; !still {
			lines = append(lines, "✅ 戻りました："+alertLabel(k))
			delete(g.active, k)
			delete(g.lastSent, k)
		}
	}
	if len(lines) == 0 {
		return
	}
	sort.Strings(lines)
	msg := "【Atomosidea 監視】\n" + strings.Join(lines, "\n")
	log.Printf("Guard watcher alert: %s", strings.ReplaceAll(msg, "\n", " / "))
	if g.th.webhook == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"content": msg})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.th.webhook, bytes.NewReader(body))
	if err != nil {
		log.Printf("Guard watcher: bad webhook URL: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Guard watcher: webhook failed: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("Guard watcher: webhook returned %s", resp.Status)
	}
}

func alertLabel(k string) string {
	switch k {
	case "disk":
		return "ディスクの空き"
	case "scan":
		return "検査待ち"
	case "transcode":
		return "動画の変換待ち"
	case "429":
		return "回数制限（429）の件数"
	case "5xx":
		return "サーバーエラー（5xx）の件数"
	}
	return k
}

// nginx の combined 形式：... "GET /path HTTP/1.1" 429 123 "-" "UA"
var accessStatus = regexp.MustCompile(`" (\d{3}) (\d+|-) "`)

// readAccessLog は atmosidea-frontend のログを前回の続きから読み、分ごとの件数に足す。
func (g *guardWatcher) readAccessLog(ctx context.Context) {
	since := g.lastRead
	if since.IsZero() {
		since = time.Now().Add(-time.Hour)
	}
	until := time.Now()
	rc, err := g.cli.ContainerLogs(ctx, frontendContainerName, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: false,
		Timestamps: true,
		Since:      strconv.FormatInt(since.Unix(), 10),
		Until:      strconv.FormatInt(until.Unix(), 10),
	})
	if err != nil {
		log.Printf("Guard watcher: read frontend logs failed: %v", err)
		return
	}
	defer rc.Close()

	counts := map[int64]map[string]int{}
	sc := NewDockerLogScanner(rc)
	for sc.Scan() {
		line := sc.Text()
		// 先頭は Docker が付けた時刻（RFC3339Nano）
		sp := strings.IndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, line[:sp])
		if err != nil {
			continue
		}
		m := accessStatus.FindStringSubmatch(line[sp+1:])
		if m == nil {
			continue
		}
		minute := ts.Unix() / 60
		b := counts[minute]
		if b == nil {
			b = map[string]int{}
			counts[minute] = b
		}
		b["total"]++
		code := m[1]
		switch {
		case code == "429" || code == "413" || code == "503":
			b[code]++
		}
		if code[0] == '5' {
			b["5xx"]++
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	for minute, b := range counts {
		dst := g.buckets[minute]
		if dst == nil {
			dst = map[string]int{}
			g.buckets[minute] = dst
		}
		for k, v := range b {
			dst[k] += v
		}
	}
	// 1 時間より古い分は捨てる
	oldest := time.Now().Add(-61*time.Minute).Unix() / 60
	for minute := range g.buckets {
		if minute < oldest {
			delete(g.buckets, minute)
		}
	}
	g.lastRead = until
}

// sumBuckets は直近 minutes 分の件数を足す（mu を持って呼ぶ）。
func (g *guardWatcher) sumBuckets(minutes int64) map[string]int {
	out := map[string]int{"429": 0, "413": 0, "503": 0, "5xx": 0, "total": 0}
	from := time.Now().Unix()/60 - minutes + 1
	for minute, b := range g.buckets {
		if minute >= from {
			for k, v := range b {
				out[k] += v
			}
		}
	}
	return out
}

// ---- Redis（キューの長さを読むだけなので、依存を増やさず最小限の RESP で話す） ----

func readBacklog(ctx context.Context) (scan, transcode int64, err error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return 0, 0, fmt.Errorf("REDIS_ADDR is not set")
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	rd := bufio.NewReader(conn)

	if pw := os.Getenv("REDIS_PASSWORD"); pw != "" {
		if _, err := redisCmd(conn, rd, "AUTH", pw); err != nil {
			return 0, 0, fmt.Errorf("auth: %w", err)
		}
	}
	// backend/shared/queue と同じ名前（処理中の一覧は <名前>:processing）
	names := []string{"sfsp:scan_queue", "sfsp:scan_queue:processing", "sfsp:completed:stream", "sfsp:completed:stream:processing"}
	vals := make([]int64, len(names))
	for i, n := range names {
		v, err := redisCmd(conn, rd, "LLEN", n)
		if err != nil {
			return 0, 0, fmt.Errorf("llen %s: %w", n, err)
		}
		vals[i] = v
	}
	return vals[0] + vals[1], vals[2] + vals[3], nil
}

// redisCmd はコマンドを送り、整数か +OK の応答を読む。
func redisCmd(w io.Writer, rd *bufio.Reader, args ...string) (int64, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return 0, err
	}
	line, err := rd.ReadString('\n')
	if err != nil {
		return 0, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return 0, fmt.Errorf("empty reply")
	}
	switch line[0] {
	case '+':
		return 0, nil
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '-':
		return 0, fmt.Errorf("redis: %s", line[1:])
	}
	return 0, fmt.Errorf("unexpected reply %q", line)
}

func fmtBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	}
	return fmt.Sprintf("%d MB", n>>20)
}
