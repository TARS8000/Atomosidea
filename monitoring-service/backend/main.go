// monitoring-service のバックエンド。Docker ソケット経由でコンテナ一覧・リソース使用量・
// ネットワーク構成・ボリューム・ログを返し、compose 内の MinIO コンテナに対する
// オブジェクトの一覧・アップロード・削除を提供する。全エンドポイントは Basic 認証で保護する。
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ContainerStats は 1 コンテナの CPU 使用率（%）とメモリ使用量・上限（バイト）を表す。
type ContainerStats struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsage   float64 `json:"memUsage"`
	MemLimit   float64 `json:"memLimit"`
}

// ContainerInfo は /api/containers が返すコンテナ 1 件分の情報。
// Health・StartedAt・Uptime・RestartCount は ContainerInspect の結果から埋める。
type ContainerInfo struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Image        string    `json:"image"`
	Status       string    `json:"status"`
	State        string    `json:"state"`
	Health       string    `json:"health"`
	Created      time.Time `json:"created"`
	StartedAt    string    `json:"startedAt"`
	Uptime       string    `json:"uptime"`
	RestartCount int       `json:"restartCount"`
	// compose のラベル由来（compose 外のコンテナは空）。フロントエンドのツリーと依存関係の表示に使う
	Project   string   `json:"project"`
	Service   string   `json:"service"`
	DependsOn []string `json:"dependsOn"`
}

// TopologyNode は /api/topology が返すノード。コンテナと、それが接続している Docker ネットワーク名の一覧を持つ。
type TopologyNode struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Networks []string `json:"networks"`
}

// VolumeInfo は /api/volumes が返す Docker ボリューム 1 件分の情報。
type VolumeInfo struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Labels     map[string]string `json:"labels"`
}

// MinioItem は MinIO ブラウザの 1 行分（バケット・フォルダ・ファイルのいずれか）を表す。
// Size と LastModified はファイルのときだけ設定する。
type MinioItem struct {
	Name         string    `json:"name"`
	Type         string    `json:"type"` // "bucket", "folder", "file"
	Size         int64     `json:"size,omitempty"`
	LastModified time.Time `json:"lastModified,omitempty"`
}

// upgrader はログ配信用の WebSocket アップグレーダー。
// CheckOrigin に isTrustedRequest を使い、クロスサイト WebSocket ハイジャックを防ぐ。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     isTrustedRequest,
}

// allowedHosts はダッシュボードへのアクセスを許可するホスト名。
// DNS リバインディング対策として Host ヘッダーをこの一覧で検証する。
// MONITORING_ALLOWED_HOSTS（カンマ区切り）で追加できる。
var allowedHosts = func() map[string]bool {
	hosts := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, h := range strings.Split(os.Getenv("MONITORING_ALLOWED_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts[strings.ToLower(h)] = true
		}
	}
	return hosts
}()

// hostnameOf は "host:port" 形式からポートと IPv6 の角括弧を除いたホスト名を小文字で返す。
// ポートが無い場合は入力全体をホスト名として扱う。
func hostnameOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(strings.Trim(h, "[]"))
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

// isTrustedRequest は Host が許可リストに含まれ、かつ Origin ヘッダーがある場合は
// 同一ホストからのリクエストであることを確認する。
// ダッシュボードは MinIO の root 操作（アップロード・削除）ができるため、他サイトのページからの
// リクエスト（CSRF / クロスサイト WebSocket）を拒否する。
func isTrustedRequest(r *http.Request) bool {
	host := hostnameOf(r.Host)
	if !allowedHosts[host] {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.ToLower(u.Hostname()) == host
}

// authRealm は 401 応答の WWW-Authenticate ヘッダーに設定する値。
// ダッシュボードのログイン（HTTP Basic 認証）。MONITOR_USERNAME / MONITOR_PASSWORD（環境変数）と照合する。
// どちらかが未設定なら全リクエストを拒否する（fail closed）。
// ブラウザは同一オリジンの fetch と WebSocket のハンドシェイクにも Basic 認証の資格情報を自動で付けるため、
// フロントエンド側の変更は不要。
const authRealm = `Basic realm="Atomosidea Monitoring", charset="UTF-8"`

// 起動時に計算した資格情報の SHA-256 ハッシュ。平文は保持しない。
// monitorAuthSet は両方の環境変数が設定済みかどうかを示す。
var (
	monitorUserHash [sha256.Size]byte
	monitorPassHash [sha256.Size]byte
	monitorAuthSet  bool
)

// initMonitorAuth は環境変数からダッシュボードの資格情報を読み、ハッシュ化して保持する。
// どちらかが空なら警告を出して monitorAuthSet を false のままにする。
func initMonitorAuth() {
	user := os.Getenv("MONITOR_USERNAME")
	pass := os.Getenv("MONITOR_PASSWORD")
	if user == "" || pass == "" {
		log.Println("WARNING: MONITOR_USERNAME / MONITOR_PASSWORD is not set. All requests will be rejected.")
		return
	}
	monitorUserHash = sha256.Sum256([]byte(user))
	monitorPassHash = sha256.Sum256([]byte(pass))
	monitorAuthSet = true
}

// isAuthorized は Basic 認証の資格情報を定数時間で比較する（長さの違いも漏らさないようハッシュ同士を比較）。
func isAuthorized(r *http.Request) bool {
	if !monitorAuthSet {
		return false
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	uh := sha256.Sum256([]byte(user))
	ph := sha256.Sum256([]byte(pass))
	userOK := subtle.ConstantTimeCompare(uh[:], monitorUserHash[:])
	passOK := subtle.ConstantTimeCompare(ph[:], monitorPassHash[:])
	return userOK&passOK == 1
}

// main は認証設定と Docker クライアントを初期化し、ルーティングを登録して :8080 で待ち受ける。
func main() {
	initMonitorAuth()

	// 環境変数（DOCKER_HOST など）から Docker クライアントを作り、API バージョンはデーモンに合わせる
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}

	router := mux.NewRouter()

	// 同一オリジン以外からのアクセスを拒否し、Basic 認証を必須にするミドルウェア
	// (フロントエンドは同じ nginx から配信されるため CORS ヘッダーは不要)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isTrustedRequest(r) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			if !monitorAuthSet {
				http.Error(w, "Monitoring login is not configured (set MONITOR_USERNAME / MONITOR_PASSWORD)", http.StatusServiceUnavailable)
				return
			}
			if !isAuthorized(r) {
				w.Header().Set("WWW-Authenticate", authRealm)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	// nginx の auth_request 用（UI の静的ファイルも同じ資格情報で保護する）。認証はミドルウェアで済んでいる
	router.HandleFunc("/auth/check", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}).Methods("GET", "HEAD")

	// コンテナ・統計・接続数・トポロジー・ボリュームの参照 API
	router.HandleFunc("/api/containers", getContainers(cli)).Methods("GET")
	router.HandleFunc("/api/containers/stats", getContainerStats(cli)).Methods("GET")
	router.HandleFunc("/api/connections/count", getActiveUserCount(cli)).Methods("GET")
	router.HandleFunc("/api/topology", getTopology(cli)).Methods("GET")
	router.HandleFunc("/api/volumes", listVolumes(cli)).Methods("GET")

	// DoS 対策の状態（ディスクの空き・処理待ち・429 などの件数）。1 分ごとに集め、しきい値を超えたら Discord に知らせる
	guard := startGuardWatcher(cli)
	router.HandleFunc("/api/guard/status", guard.handler()).Methods("GET")

	// ログのストリーミング（対象は ?container=<名前または ID>）
	router.HandleFunc("/ws/logs", serveWs(cli)).Methods("GET")

	// MinIO API（{containerName} は操作対象の MinIO コンテナ名）
	router.HandleFunc("/api/minio/list/{containerName}", listMinioObjects(cli)).Methods("GET")
	router.HandleFunc("/api/minio/upload/{containerName}", uploadMinioObject(cli)).Methods("POST")
	router.HandleFunc("/api/minio/delete/{containerName}", deleteMinioObject(cli)).Methods("DELETE")

	log.Println("Monitoring backend started on :8080")
	log.Fatal(http.ListenAndServe(":8080", router))
}

// --- 監視対象の compose プロジェクト ---
// ダッシュボードは Atomosidea のコンテナだけを扱う。同じ Docker で動いている別プロジェクトのコンテナは、
// 一覧・統計・トポロジー・ボリュームに含めず、ログと MinIO の操作対象にもさせない。

// composeProjectLabel は compose がコンテナとボリュームに付けるプロジェクト名のラベル。
const composeProjectLabel = "com.docker.compose.project"

// frontendContainerName は、監視対象のプロジェクトを見つけるときの目印にするコンテナ名。
const frontendContainerName = "atmosidea-frontend"

// monitorProject は環境変数 MONITOR_PROJECT で明示された監視対象のプロジェクト名。
// docker-compose.monitoring.yml が .env の COMPOSE_PROJECT_NAME を渡す。空なら resolveProject が自動で決める。
var monitorProject = strings.TrimSpace(os.Getenv("MONITOR_PROJECT"))

// resolveProject は監視対象のプロジェクト名を返す。MONITOR_PROJECT を優先し、未設定なら
// 一覧の中の atmosidea-frontend コンテナのプロジェクトラベルを使う。どちらも無ければ空（絞り込まない）。
func resolveProject(containers []types.Container) string {
	if monitorProject != "" {
		return monitorProject
	}
	for _, c := range containers {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == frontendContainerName {
				return c.Labels[composeProjectLabel]
			}
		}
	}
	return ""
}

// inProject はラベルが監視対象のプロジェクトのものかを返す。project が空なら常に真（絞り込まない）。
func inProject(labels map[string]string, project string) bool {
	return project == "" || labels[composeProjectLabel] == project
}

// listProjectContainers は停止中を含む全コンテナを取得し、監視対象のプロジェクトのものだけを返す。
// プロジェクトの判定に atmosidea-frontend を探すため、絞り込みは取得後に行う。
func listProjectContainers(ctx context.Context, cli *client.Client) ([]types.Container, string, error) {
	all, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, "", err
	}
	project := resolveProject(all)
	filtered := make([]types.Container, 0, len(all))
	for _, c := range all {
		if inProject(c.Labels, project) {
			filtered = append(filtered, c)
		}
	}
	return filtered, project, nil
}

// checkTargetLabels は inspect で得たラベルが監視対象のプロジェクトのものかを確かめ、違えばエラーを返す。
// MONITOR_PROJECT が未設定のときは、コンテナ一覧から監視対象のプロジェクトを求める。
func checkTargetLabels(ctx context.Context, cli *client.Client, labels map[string]string) error {
	project := monitorProject
	if project == "" {
		all, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}
		project = resolveProject(all)
	}
	if !inProject(labels, project) {
		return fmt.Errorf("container is not part of the monitored project %q", project)
	}
	return nil
}

// getContainerStats は監視対象のプロジェクトで稼働中のコンテナの CPU・メモリ使用量を並行して取得し、
// コンテナ名をキーにした JSON オブジェクトで返すハンドラーを作る。
// 取得に失敗したコンテナはログに残して結果から除外する。
func getContainerStats(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		// 停止中も含めて取得し、監視対象のプロジェクトに絞る（稼働中かどうかは下のループで判定する）
		containers, _, err := listProjectContainers(ctx, cli)
		if err != nil {
			http.Error(w, "Failed to list containers", http.StatusInternalServerError)
			return
		}

		statsMap := make(map[string]ContainerStats)
		var wg sync.WaitGroup
		var mu sync.Mutex

		for _, c := range containers {
			if c.State != "running" {
				continue
			}
			// stats の取得は 1 件ごとに時間がかかるため、コンテナごとに goroutine で並行実行する
			wg.Add(1)
			go func(c types.Container) {
				defer wg.Done()
				// stream=false で 1 回分のスナップショットだけを受け取る
				stats, err := cli.ContainerStats(ctx, c.ID, false)
				if err != nil {
					log.Printf("Failed to get stats for container %s: %v", c.ID, err)
					return
				}
				defer stats.Body.Close()

				var v types.StatsJSON
				if err := json.NewDecoder(stats.Body).Decode(&v); err != nil {
					log.Printf("Failed to decode stats for container %s: %v", c.ID, err)
					return
				}

				cpuPercent := calculateCPUPercent(&v)
				memUsage := float64(v.MemoryStats.Usage)
				memLimit := float64(v.MemoryStats.Limit)

				// map への書き込みは goroutine 間で競合するためロックする
				mu.Lock()
				name := strings.TrimPrefix(c.Names[0], "/")
				statsMap[name] = ContainerStats{
					CPUPercent: cpuPercent,
					MemUsage:   memUsage,
					MemLimit:   memLimit,
				}
				mu.Unlock()
			}(c)
		}

		wg.Wait()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statsMap)
	}
}

// calculateCPUPercent は docker stats と同じ式で CPU 使用率を求める。
// cgroup v2 では PercpuUsage が空になるため、OnlineCPUs を優先し、無ければ PercpuUsage の数、それも無ければ 1 とする。
func calculateCPUPercent(v *types.StatsJSON) float64 {
	cpuDelta := float64(v.CPUStats.CPUUsage.TotalUsage) - float64(v.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(v.CPUStats.SystemUsage) - float64(v.PreCPUStats.SystemUsage)

	onlineCPUs := float64(v.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(v.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		return (cpuDelta / systemDelta) * onlineCPUs * 100.0
	}
	return 0.0
}

// getActiveUserCount は atmosidea-frontend コンテナの直近 5 分間のログから IPv4 アドレスを抜き出し、
// 重複を除いた件数を「アクティブユーザー数」の目安として返すハンドラーを作る。
func getActiveUserCount(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		options := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Since:      "5m", // 直近5分間のログを取得
		}

		// 名前に "atmosidea-frontend" を含むコンテナを探す（停止中も含む）
		containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			http.Error(w, "Failed to list containers", http.StatusInternalServerError)
			return
		}

		var frontendContainerID string
		for _, c := range containers {
			for _, name := range c.Names {
				if strings.Contains(name, "atmosidea-frontend") {
					frontendContainerID = c.ID
					break
				}
			}
			if frontendContainerID != "" {
				break
			}
		}

		if frontendContainerID == "" {
			http.Error(w, "atmosidea-frontend container not found", http.StatusNotFound)
			return
		}

		reader, err := cli.ContainerLogs(ctx, frontendContainerID, options)
		if err != nil {
			http.Error(w, "Failed to get logs for frontend container", http.StatusInternalServerError)
			return
		}
		defer reader.Close()

		// 各行で最初に現れる IPv4 形式の文字列をアクセス元として数える
		ipRegex := regexp.MustCompile(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})`)
		uniqueIPs := make(map[string]bool)

		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			// Dockerログのヘッダー（8バイト）をスキップ
			if len(line) > 8 {
				line = line[8:]
			}
			matches := ipRegex.FindStringSubmatch(line)
			if len(matches) > 1 {
				uniqueIPs[matches[1]] = true
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"count": len(uniqueIPs)})
	}
}

// getContainers は監視対象のプロジェクトの、停止中を含む全コンテナの情報を返すハンドラーを作る。
// 各コンテナの inspect は goroutine で並行して行うため、返す配列の順序は一定ではない。
func getContainers(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		containers, _, err := listProjectContainers(context.Background(), cli)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list containers: %v", err), http.StatusInternalServerError)
			return
		}

		// 0 件でも null ではなく [] を返す
		containerInfos := make([]ContainerInfo, 0, len(containers))
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, c := range containers {
			wg.Add(1)
			go func(c types.Container) {
				defer wg.Done()
				name := strings.TrimPrefix(c.Names[0], "/")
				createdTime := time.Unix(c.Created, 0)

				// 一覧 API に含まれない再起動回数・ヘルス・起動時刻は inspect で補う
				meta := inspectContainerMeta(cli, c.ID)
				info := ContainerInfo{
					ID:           c.ID[:12],
					Name:         name,
					Image:        c.Image,
					Status:       c.Status,
					State:        c.State,
					RestartCount: meta.restartCount,
					Created:      createdTime,
					Health:       meta.health,
					StartedAt:    meta.startedAt,
					Uptime:       getContainerUptime(meta.startedAt),
					Project:      c.Labels[composeProjectLabel],
					Service:      c.Labels["com.docker.compose.service"],
					DependsOn:    parseDependsOn(c.Labels["com.docker.compose.depends_on"]),
				}
				mu.Lock()
				containerInfos = append(containerInfos, info)
				mu.Unlock()
			}(c)
		}
		wg.Wait()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(containerInfos)
	}
}

// parseDependsOn は compose が付ける depends_on ラベル（"redis:service_started:false,minio:service_healthy:false" の形式）から
// 依存先のサービス名だけを取り出す。ラベルが空なら空のスライスを返す。
func parseDependsOn(label string) []string {
	deps := []string{}
	for _, entry := range strings.Split(label, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(entry), ":")
		if name != "" {
			deps = append(deps, name)
		}
	}
	return deps
}

// getContainerNetworks はコンテナが接続している Docker ネットワーク名の一覧を返す。
// inspect は 3 秒でタイムアウトし、失敗時は nil を返す。
func getContainerNetworks(cli *client.Client, id string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil
	}
	networks := make([]string, 0, len(info.NetworkSettings.Networks))
	for name := range info.NetworkSettings.Networks {
		networks = append(networks, name)
	}
	return networks
}

// containerMeta は ContainerInspect から取り出す補足情報（再起動回数・ヘルス状態・起動時刻）。
type containerMeta struct {
	restartCount int
	health       string
	startedAt    string
}

// inspectContainerMeta はコンテナを inspect して containerMeta を返す。
// 3 秒でタイムアウトし、失敗時はゼロ値を返す。ヘルスチェック未定義のコンテナでは health は空になる。
func inspectContainerMeta(cli *client.Client, id string) containerMeta {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil || info.State == nil {
		return containerMeta{}
	}
	var health string
	if info.State.Health != nil {
		health = info.State.Health.Status
	}
	var startedAt string
	if info.State.StartedAt != "" {
		startedAt = info.State.StartedAt
	}
	return containerMeta{
		restartCount: int(info.RestartCount),
		health:       health,
		startedAt:    startedAt,
	}
}

// getContainerUptime は起動時刻（RFC3339Nano）から現在までの経過時間を秒単位に丸めた文字列（例: "1h2m3s"）で返す。
// 空文字や解析できない値の場合は空文字を返す。停止中のコンテナでも最後の起動時刻からの経過時間になる。
func getContainerUptime(startedAt string) string {
	if startedAt == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
		return time.Since(t).Round(time.Second).String()
	}
	return ""
}

// getTopology は監視対象のプロジェクトの全コンテナと、それぞれが接続しているネットワークの一覧を返すハンドラーを作る。
// ネットワーク情報の inspect はコンテナごとに順番に行う。
func getTopology(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		containers, _, err := listProjectContainers(context.Background(), cli)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list containers: %v", err), http.StatusInternalServerError)
			return
		}

		nodes := make([]TopologyNode, 0, len(containers))
		for _, c := range containers {
			name := strings.TrimPrefix(c.Names[0], "/")
			nodes = append(nodes, TopologyNode{
				ID:       c.ID[:12],
				Name:     name,
				Networks: getContainerNetworks(cli, c.ID),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodes)
	}
}

// listVolumes は監視対象のプロジェクトの Docker ボリューム（compose のプロジェクトラベルで判定）の一覧を返すハンドラーを作る。
func listVolumes(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		resp, err := cli.VolumeList(ctx, volume.ListOptions{})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list volumes: %v", err), http.StatusInternalServerError)
			return
		}
		// 監視対象のプロジェクトを求める（MONITOR_PROJECT が未設定ならコンテナ一覧から）
		_, project, err := listProjectContainers(ctx, cli)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list containers: %v", err), http.StatusInternalServerError)
			return
		}

		vols := make([]VolumeInfo, 0, len(resp.Volumes))
		for _, v := range resp.Volumes {
			if !inProject(v.Labels, project) {
				continue
			}
			vols = append(vols, VolumeInfo{
				Name:       v.Name,
				Driver:     v.Driver,
				Mountpoint: v.Mountpoint,
				Labels:     v.Labels,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vols)
	}
}

// serveWs は ?container= で指定したコンテナのログを WebSocket で配信するハンドラーを作る。
// 直近 100 行を送った後は追従（follow）し、新しい行を 1 行ずつテキストメッセージで送る。
// 送信に失敗したら（クライアントの切断など）終了する。
func serveWs(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. 対象コンテナの指定を確認する
		containerTarget := r.URL.Query().Get("container")
		if containerTarget == "" {
			http.Error(w, "Container ID or Name is required", http.StatusBadRequest)
			return
		}

		// 2. WebSocket にアップグレードする（Origin の検証は upgrader.CheckOrigin で行う）
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Failed to upgrade to websocket: %v", err)
			return
		}
		defer conn.Close()

		// 3. コンテナが存在し、監視対象のプロジェクトのものか確認する。違えばエラー文をクライアントへ送って終了する
		info, err := cli.ContainerInspect(context.Background(), containerTarget)
		if err != nil {
			log.Printf("Failed to inspect container %s: %v", containerTarget, err)
			conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error inspecting container: %v", err)))
			return
		}
		if err := checkTargetLabels(context.Background(), cli, info.Config.Labels); err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error: %v", err)))
			return
		}
		// 4. 直近 100 行から追従モードでログを取得する
		options := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Timestamps: true,
			Tail:       "100",
		}

		reader, err := cli.ContainerLogs(context.Background(), containerTarget, options)
		if err != nil {
			log.Printf("Failed to get logs for container %s: %v", containerTarget, err)
			return
		}
		defer reader.Close()

		// 5. 多重化ストリームをフレーム単位で読み、1 フレームずつクライアントへ送る
		scanner := NewDockerLogScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			messageContent := line

			// 先頭のタイムスタンプ（Timestamps: true で付与される）を取り除く
			// 形式は "2006-01-02T15:04:05.000000000Z <本文>"。解析できたときだけ外す
			if i := strings.IndexByte(line, ' '); i > 0 {
				if _, err := time.Parse(time.RFC3339Nano, line[:i]); err == nil {
					messageContent = line[i+1:]
				}
			}

			if err := conn.WriteMessage(websocket.TextMessage, []byte(messageContent)); err != nil {
				break
			}
		}
	}
}

// getMinioClient は指定コンテナの環境変数から MinIO の root 資格情報を読み取り、
// "<コンテナ名>:9000" へ HTTP で接続する MinIO クライアントを作る。
// 資格情報は MINIO_ROOT_USER / MINIO_ROOT_PASSWORD を優先し、無ければ旧名の MINIO_ACCESS_KEY / MINIO_SECRET_KEY を使う。
// コンテナ名で名前解決するため、このサービスが対象コンテナと同じ Docker ネットワークにいることが前提。
func getMinioClient(cli *client.Client, containerName string) (*minio.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	containerJSON, err := cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("container inspect failed: %w", err)
	}
	// 監視対象のプロジェクト以外のコンテナには接続しない
	if err := checkTargetLabels(ctx, cli, containerJSON.Config.Labels); err != nil {
		return nil, err
	}

	// "KEY=VALUE" 形式の環境変数を map に変換する（値に含まれる "=" は保持する）
	envMap := make(map[string]string)
	for _, env := range containerJSON.Config.Env {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	accessKeyID := envMap["MINIO_ROOT_USER"]
	if accessKeyID == "" {
		accessKeyID = envMap["MINIO_ACCESS_KEY"]
	}
	secretAccessKey := envMap["MINIO_ROOT_PASSWORD"]
	if secretAccessKey == "" {
		secretAccessKey = envMap["MINIO_SECRET_KEY"]
	}

	endpoint := containerName + ":9000"

	return minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: false,
	})
}

// listMinioObjects は MinIO の中身を 1 階層分だけ返すハンドラーを作る。
// ?bucket が空ならバケット一覧を、指定があれば ?prefix 直下のフォルダとファイルを返す。
// MinIO への接続や一覧取得に失敗した場合はエラーにせず空配列を返す。
func listMinioObjects(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		vars := mux.Vars(r)
		containerName := vars["containerName"]
		bucketName := r.URL.Query().Get("bucket")
		prefix := r.URL.Query().Get("prefix")

		minioClient, err := getMinioClient(cli, containerName)
		if err != nil {
			json.NewEncoder(w).Encode([]MinioItem{})
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		var minioItems []MinioItem

		if bucketName == "" {
			// バケット未指定: バケット一覧を返す
			buckets, err := minioClient.ListBuckets(ctx)
			if err != nil {
				json.NewEncoder(w).Encode([]MinioItem{})
				return
			}
			for _, bucket := range buckets {
				minioItems = append(minioItems, MinioItem{
					Name: bucket.Name,
					Type: "bucket",
				})
			}
		} else {
			// バケット指定あり: prefix 直下だけを非再帰で列挙する（サブフォルダは "xxx/" の共通接頭辞として返る）
			objectCh := minioClient.ListObjects(ctx, bucketName, minio.ListObjectsOptions{
				Prefix:    prefix,
				Recursive: false,
			})

			// 同じフォルダを重複して返さないための集合
			folders := make(map[string]struct{})

			for object := range objectCh {
				// 個々の要素のエラーは無視して続ける
				if object.Err != nil {
					continue
				}

				// prefix 自身（フォルダのマーカーオブジェクト）は一覧に含めない
				if object.Key == prefix {
					continue
				}

				relKey := strings.TrimPrefix(object.Key, prefix)
				if relKey == "" {
					continue
				}

				// "/" を含むキーはフォルダとして先頭の要素名だけを返し、それ以外はファイルとして返す
				if strings.HasSuffix(object.Key, "/") || strings.Contains(relKey, "/") {
					parts := strings.Split(relKey, "/")
					folderName := parts[0]
					if folderName != "" {
						if _, exists := folders[folderName]; !exists {
							minioItems = append(minioItems, MinioItem{
								Name: folderName,
								Type: "folder",
							})
							folders[folderName] = struct{}{}
						}
					}
				} else {
					minioItems = append(minioItems, MinioItem{
						Name:         relKey,
						Type:         "file",
						Size:         object.Size,
						LastModified: object.LastModified,
					})
				}
			}
		}

		json.NewEncoder(w).Encode(minioItems)
	}
}

// uploadMinioObject は multipart の "file" フィールドで受け取ったファイルを、
// ?bucket のバケットに "<prefix><ファイル名>" のキーでアップロードするハンドラーを作る。
func uploadMinioObject(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		containerName := vars["containerName"]
		bucketName := r.URL.Query().Get("bucket")
		prefix := r.URL.Query().Get("prefix")

		minioClient, err := getMinioClient(cli, containerName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// 32MiB まではメモリに保持し、それを超える分は一時ファイルに書き出される
		err = r.ParseMultipartForm(32 << 20)
		if err != nil {
			http.Error(w, "File too large", http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "Missing file param", http.StatusBadRequest)
			return
		}
		defer file.Close()

		objectKey := prefix + header.Filename
		_, err = minioClient.PutObject(context.Background(), bucketName, objectKey, file, header.Size, minio.PutObjectOptions{
			ContentType: header.Header.Get("Content-Type"),
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to upload: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "Uploaded successfully"})
	}
}

// deleteMinioObject は ?bucket のバケットから ?key のオブジェクトを削除するハンドラーを作る。
func deleteMinioObject(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		containerName := vars["containerName"]
		bucketName := r.URL.Query().Get("bucket")
		objectKey := r.URL.Query().Get("key")

		minioClient, err := getMinioClient(cli, containerName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		err = minioClient.RemoveObject(context.Background(), bucketName, objectKey, minio.RemoveObjectOptions{})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to delete: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "Deleted successfully"})
	}
}

// DockerLogScanner は Docker の多重化ログストリーム（TTY なしのコンテナ）をフレーム単位で読み出す。
// 各フレームは 8 バイトのヘッダー（先頭 1 バイトが stdout/stderr の種別、末尾 4 バイトがビッグエンディアンの本文長）と本文からなる。
type DockerLogScanner struct {
	reader *bufio.Reader
	text   string
}

// NewDockerLogScanner は reader をバッファ付きで包んだ DockerLogScanner を返す。
func NewDockerLogScanner(reader io.Reader) *DockerLogScanner {
	return &DockerLogScanner{reader: bufio.NewReader(reader)}
}

// Scan は次のフレームを 1 つ読み、末尾の改行を除いた本文を Text で取れるようにする。
// ストリームの終端や読み取りエラーでは false を返す。stdout と stderr の区別はしない。
func (s *DockerLogScanner) Scan() bool {
	header := make([]byte, 8)
	if _, err := io.ReadFull(s.reader, header); err != nil {
		return false
	}

	payloadSize := binary.BigEndian.Uint32(header[4:8])
	payload := make([]byte, payloadSize)
	if _, err := io.ReadFull(s.reader, payload); err != nil {
		return false
	}

	s.text = strings.TrimSuffix(string(payload), "\n")
	return true
}

// Text は直前の Scan で読み取ったフレームの本文を返す。
func (s *DockerLogScanner) Text() string { return s.text }
