// sfsp-docker-guard は sfsp-worker と sfsp-docker-proxy（tecnativa/docker-socket-proxy）の間に置く、
// Docker API のリクエストの中身まで検査するプロキシ。
//
// なぜ要るか：docker-socket-proxy は URL の先頭（/containers など）と HTTP メソッドでしか絞れない。
// sfsp-worker のサンドボックスにはコンテナの作成（POST /containers/create）が要るので CONTAINERS=1・POST=1 になるが、
// それだと本文に Privileged: true や Binds: ["/:/host"] を書いたコンテナも作れ、/containers/{id}/exec や
// /containers/{id}/archive（ファイルの書き込み）も通る。sfsp-worker（アップロードされたファイルを扱う）が
// 乗っ取られると、ホストの root を取られる。
//
// ここで通すのは、sfsp-worker の internal/sandbox/docker.go（backend/security/sfsp）が実際に送るリクエストだけ：
//
//	HEAD|GET  /_ping                               API バージョンのネゴシエーション
//	GET       /images/{許可したイメージ}/json         ImageInspect
//	POST      /containers/create?name=sfsp-sandbox-…  本文を検査し、許可した項目だけで本文を作り直して送る
//	POST      /containers/{サンドボックス}/start
//	POST      /containers/{サンドボックス}/wait
//	GET       /containers/{サンドボックス}/logs
//	DELETE    /containers/{サンドボックス}
//
// 「サンドボックス」は、名前が sfsp-sandbox- で始まり、ラベル app=sfsp-sandbox を持つコンテナ（ここで作ったものだけ）。
// 操作の前に GET /containers/{ref}/json で確かめ、確かめた ID に対して操作する（ほかのコンテナを止める・消す・ログを読むことはできない）。
// それ以外（exec・archive・build・pull・volumes・networks・一覧など）はすべて 403。
//
// コンテナ作成の本文は「許可リスト」で検査する。許可した項目以外は、値がゼロ（null・false・0・""・空）でなければ拒否し、
// ゼロでも転送しない（Docker に届くのは、このファイルの createRequest に書いた項目だけ）。
// 大文字小文字を変えたキー（"privileged" など。Docker の JSON デコードは大文字小文字を区別しない）や重複キーも、
// 作り直した本文には入らないので効かない。
//
// 資源の上限（メモリ・CPU・プロセス数・/tmp の tmpfs のサイズ）は sandbox/docker.go が送る値ちょうど。
// /tmp の tmpfs は、送られたオプションによらず rw,noexec,nosuid,nodev,size=<上限以下> で作り直す。
//
// サンドボックスの数にも上限がある（DoS 対策）。作成と start の前に、ラベル app=sfsp-sandbox のコンテナを上流で数え、
// 動いている（exited・dead 以外の）ものが SFSP_GUARD_MAX_ACTIVE_SANDBOXES（既定 2）以上、
// または停止済みを含めて SFSP_GUARD_MAX_SANDBOXES（既定 32）以上なら 403 にする。
// sfsp-worker は 1 つだけで、ジョブを 1 つずつ処理し、ClamAV → YARA の順にサンドボックスを 1 つずつ動かして毎回消す
// （internal/worker/worker.go の StartWorker・processJob。同時に動くのは 1 つ）ので、2 は 2 倍の余裕
// （sfsp-worker が落ちて動いたまま残ったものが 1 つあっても止まらない）。
// 数えてから作る（start する）までは、このプロセスの中ではロックで直列にしている。sfsp-docker-proxy に届くのは
// sfsp-docker-guard だけなので普通はこれで足りるが、Docker 側で原子的に確かめているわけではない
// （ほかの経路でラベル app=sfsp-sandbox のコンテナが作られると、数えた後に増えうる。増えた分も次の作成からは数える）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// サンドボックスに許す資源の上限。internal/sandbox/docker.go が送る値ちょうど（docker.go に定数で書いてあり、
// 環境変数などでは変わらない）。docker.go で上げたら、ここも変える（TestLimitsMatchSandboxDockerGo が食い違いを見つける）
const (
	maxMemory     = 1536 << 20    // Resources.Memory: 1536 * 1024 * 1024
	maxNanoCPUs   = 1_000_000_000 // Resources.NanoCPUs
	maxPidsLimit  = 256           // pidsLimit
	maxTmpfsBytes = 512 << 20     // sandboxTmpfsOptions の size=512m
	maxBodyBytes  = 1 << 20
	maxArgs       = 256
	maxArgBytes   = 8192
)

// サンドボックスの数の上限の既定値（先頭のコメントを参照）
const (
	defaultMaxActiveSandboxes = 2
	defaultMaxSandboxes       = 32
)

const (
	sandboxNamePrefix = "sfsp-sandbox-"
	sandboxLabelKey   = "app"
	sandboxLabelValue = "sfsp-sandbox"
	scanTarget        = "/scan_target"
)

var (
	// 先頭の /v1.NN は省略可（docker クライアントはネゴシエーションした版を付ける）
	verRe = `(/v1\.[0-9]{1,3})?`

	pingRe      = regexp.MustCompile(`^` + verRe + `/_ping$`)
	imageRe     = regexp.MustCompile(`^` + verRe + `/images/(.+)/json$`)
	createRe    = regexp.MustCompile(`^` + verRe + `/containers/create$`)
	containerRe = regexp.MustCompile(`^` + verRe + `/containers/([A-Za-z0-9][A-Za-z0-9_.-]{0,127})(/start|/wait|/logs)?$`)

	// sandbox/docker.go の containerName（sfsp-sandbox-<clamav|yara|sandbox>-<job の UUID>）
	sandboxNameRe = regexp.MustCompile(`^sfsp-sandbox-[a-z0-9]{1,16}-[A-Za-z0-9-]{1,64}$`)
	containerIDRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// サンドボックスに付けてよいラベル（sandbox/docker.go が付けるもの）
	allowedLabels = map[string]bool{"app": true, "job_id": true, "scanner": true}
	// /tmp の tmpfs で受け付けるが、値は使わずに rw,noexec,nosuid,nodev で上書きするオプション。
	// これ以外（uid=・nr_inodes= など）は拒否する。mode= は落とす（tmpfs の既定の 1777 になる）
	overriddenTmpfsOpts = map[string]bool{"rw": true, "ro": true, "exec": true, "noexec": true, "suid": true,
		"nosuid": true, "dev": true, "nodev": true}
	tmpfsSizeRe = regexp.MustCompile(`^([0-9]{1,12})([kKmMgG]?)$`)
)

// 各操作で転送してよいクエリ（ここに無いクエリが付いていたら拒否する）
var opQueries = map[string]map[string]bool{
	"/start": {},
	"/wait":  {"condition": true},
	"/logs":  {"stdout": true, "stderr": true, "since": true, "until": true, "timestamps": true, "tail": true},
	"":       {"force": true, "v": true}, // DELETE /containers/{id}
}

// createRequest は Docker に転送するコンテナ作成の本文。ここに無い項目は Docker に届かない
type createRequest struct {
	Image      string            `json:"Image"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	Labels     map[string]string `json:"Labels"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	Binds          []string          `json:"Binds"`
	NetworkMode    string            `json:"NetworkMode"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	Memory         int64             `json:"Memory"`
	NanoCPUs       int64             `json:"NanoCpus"`
	PidsLimit      int64             `json:"PidsLimit"`
	AutoRemove     bool              `json:"AutoRemove"`
}

type guard struct {
	upstream *url.URL
	client   *http.Client
	images   map[string]bool // サンドボックスに使ってよいイメージ
	scanBind string          // 唯一許すバインド（"<スキャン用の名前付きボリューム>:/scan_target:ro"）

	maxActive int // 同時に動いて（exited・dead 以外で）よいサンドボックスの数
	maxTotal  int // 停止済みを含めて存在してよいサンドボックスの数
	// 数えてから作る・start するまでを直列にする（先頭のコメントを参照）
	limitMu sync.Mutex
}

func newGuard(upstream string, images []string, scanVolume string) (*guard, error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return nil, fmt.Errorf("upstream must be http://host:port: %q", upstream)
	}
	// 名前付きボリュームの名前だけを許す（"/" で始まるとホストのパスのバインドになる）
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]+$`).MatchString(scanVolume) {
		return nil, fmt.Errorf("scan volume must be a named volume: %q", scanVolume)
	}
	g := &guard{
		upstream:  u,
		images:    map[string]bool{},
		scanBind:  scanVolume + ":" + scanTarget + ":ro",
		maxActive: defaultMaxActiveSandboxes,
		maxTotal:  defaultMaxSandboxes,
		client: &http.Client{
			// ContainerWait はスキャンが終わるまで（最長 30 分）返らないので、全体のタイムアウトは付けない。
			// 呼び出し元が切断すると r.Context() が取り消され、上流へのリクエストも止まる
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}
	for _, img := range images {
		if img = strings.TrimSpace(img); img != "" {
			g.images[img] = true
		}
	}
	if len(g.images) == 0 {
		return nil, errors.New("no allowed images")
	}
	return g, nil
}

func (g *guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// %2F などのエスケープを含むパスは拒否する（上流の解釈とずれないように）
	if r.URL.RawPath != "" || strings.Contains(r.URL.Path, "..") {
		g.deny(w, r, "escaped or relative path")
		return
	}
	p := r.URL.Path

	if m := pingRe.FindStringSubmatch(p); m != nil {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			g.deny(w, r, "method not allowed")
			return
		}
		g.forward(w, r, r.Method, m[1]+"/_ping", nil, nil)
		return
	}

	if m := imageRe.FindStringSubmatch(p); m != nil {
		if r.Method != http.MethodGet || !g.images[m[2]] {
			g.deny(w, r, "image not allowed")
			return
		}
		g.forward(w, r, http.MethodGet, m[1]+"/images/"+m[2]+"/json", nil, nil)
		return
	}

	if m := createRe.FindStringSubmatch(p); m != nil {
		if r.Method != http.MethodPost {
			g.deny(w, r, "method not allowed")
			return
		}
		g.handleCreate(w, r, m[1])
		return
	}

	if m := containerRe.FindStringSubmatch(p); m != nil {
		g.handleContainerOp(w, r, m[1], m[2], m[3])
		return
	}

	g.deny(w, r, "endpoint not allowed")
}

func (g *guard) handleCreate(w http.ResponseWriter, r *http.Request, version string) {
	q := r.URL.Query()
	for k, v := range q {
		// platform は docker クライアントが空で付けることがある
		if k != "name" && !(k == "platform" && len(v) == 1 && v[0] == "") {
			g.deny(w, r, "query not allowed: "+k)
			return
		}
	}
	if len(q["name"]) != 1 || !sandboxNameRe.MatchString(q.Get("name")) {
		g.deny(w, r, "container name must be sfsp-sandbox-<scanner>-<job id>")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		g.deny(w, r, "cannot read body: "+err.Error())
		return
	}
	out, err := g.checkCreate(body)
	if err != nil {
		g.deny(w, r, "container config not allowed: "+err.Error())
		return
	}
	g.limitMu.Lock()
	defer g.limitMu.Unlock()
	if !g.checkSandboxCount(w, r, version, "") {
		return
	}
	g.forward(w, r, http.MethodPost, version+"/containers/create", url.Values{"name": {q.Get("name")}}, out)
}

// checkSandboxCount は上流のサンドボックス（ラベル app=sfsp-sandbox のコンテナ。exclude の ID は除く）を数え、
// 上限以上なら 403、数えられなければ 502 を返して false を返す。呼び出し側は limitMu を持っていること
func (g *guard) checkSandboxCount(w http.ResponseWriter, r *http.Request, version, exclude string) bool {
	active, total, err := g.countSandboxes(r.Context(), version, exclude)
	if err != nil {
		log.Printf("ERROR %s %q: count sandboxes: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusBadGateway, "sfsp-docker-guard: cannot count sandboxes")
		return false
	}
	if active >= g.maxActive || total >= g.maxTotal {
		g.deny(w, r, fmt.Sprintf("too many sandboxes (running %d, max %d; total %d, max %d)", active, g.maxActive, total, g.maxTotal))
		return false
	}
	return true
}

// countSandboxes は上流の GET /containers/json?all=1&filters={"label":["app=sfsp-sandbox"]} でサンドボックスを数え、
// 動いている（exited・dead 以外の）数と、停止済みを含めた数を返す
func (g *guard) countSandboxes(ctx context.Context, version, exclude string) (active, total int, err error) {
	filters, _ := json.Marshal(map[string][]string{"label": {sandboxLabelKey + "=" + sandboxLabelValue}})
	u := *g.upstream
	u.Path = version + "/containers/json"
	u.RawQuery = url.Values{"all": {"1"}, "filters": {string(filters)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	var list []struct {
		ID     string            `json:"Id"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&list); err != nil {
		return 0, 0, err
	}
	for _, c := range list {
		// フィルターが効かなかったときのために、ラベルもここで確かめる
		if c.ID == exclude || c.Labels[sandboxLabelKey] != sandboxLabelValue {
			continue
		}
		total++
		if c.State != "exited" && c.State != "dead" {
			active++
		}
	}
	return active, total, nil
}

// checkCreate はコンテナ作成の本文を検査し、許可した項目だけで作り直した本文を返す
func (g *guard) checkCreate(body []byte) ([]byte, error) {
	top, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	var req createRequest

	img, ok := top["Image"].(string)
	if !ok || !g.images[img] {
		return nil, fmt.Errorf("Image %v is not allowed", top["Image"])
	}
	req.Image = img
	if req.Entrypoint, err = stringList(top["Entrypoint"], "Entrypoint"); err != nil {
		return nil, err
	}
	if req.Cmd, err = stringList(top["Cmd"], "Cmd"); err != nil {
		return nil, err
	}
	if v, ok := top["WorkingDir"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || (s != "" && !strings.HasPrefix(s, "/")) {
			return nil, errors.New("WorkingDir must be an absolute path")
		}
		req.WorkingDir = s
	}
	if req.Labels, err = sandboxLabels(top["Labels"]); err != nil {
		return nil, err
	}

	hcRaw, ok := top["HostConfig"].(map[string]any)
	if !ok {
		return nil, errors.New("HostConfig is required")
	}
	if req.HostConfig, err = g.checkHostConfig(hcRaw); err != nil {
		return nil, err
	}

	// 残りの項目（Tty・Env・User・NetworkingConfig など）はゼロでなければ拒否し、転送しない
	if err := rejectNonZero(top, "", "Image", "Entrypoint", "Cmd", "WorkingDir", "Labels", "HostConfig"); err != nil {
		return nil, err
	}
	return json.Marshal(req)
}

func (g *guard) checkHostConfig(hc map[string]any) (hostConfig, error) {
	var out hostConfig

	// バインドはスキャン用の名前付きボリュームを読み取り専用で 1 つだけ（ホストのパスは不可）
	binds, err := stringList(hc["Binds"], "HostConfig.Binds")
	if err != nil {
		return out, err
	}
	if len(binds) != 1 || binds[0] != g.scanBind {
		return out, fmt.Errorf("HostConfig.Binds must be exactly [%q], got %q", g.scanBind, binds)
	}
	out.Binds = binds

	if hc["NetworkMode"] != "none" {
		return out, fmt.Errorf("HostConfig.NetworkMode must be \"none\", got %v", hc["NetworkMode"])
	}
	out.NetworkMode = "none"

	capDrop, err := stringList(hc["CapDrop"], "HostConfig.CapDrop")
	if err != nil {
		return out, err
	}
	dropAll := false
	for _, c := range capDrop {
		if strings.EqualFold(c, "ALL") {
			dropAll = true
		}
	}
	if !dropAll {
		return out, errors.New("HostConfig.CapDrop must contain ALL")
	}
	out.CapDrop = []string{"ALL"}

	// SecurityOpt は no-new-privileges だけ（seccomp=unconfined・apparmor=unconfined・label=disable などは不可）
	secOpt, err := stringList(hc["SecurityOpt"], "HostConfig.SecurityOpt")
	if err != nil {
		return out, err
	}
	if len(secOpt) == 0 {
		return out, errors.New("HostConfig.SecurityOpt must contain no-new-privileges")
	}
	for _, s := range secOpt {
		if s != "no-new-privileges" && s != "no-new-privileges:true" && s != "no-new-privileges=true" {
			return out, fmt.Errorf("HostConfig.SecurityOpt %q is not allowed", s)
		}
	}
	out.SecurityOpt = secOpt

	if hc["ReadonlyRootfs"] != true {
		return out, errors.New("HostConfig.ReadonlyRootfs must be true")
	}
	out.ReadonlyRootfs = true

	if v, ok := hc["Tmpfs"]; ok && !isZero(v) {
		tm, ok := v.(map[string]any)
		if !ok {
			return out, errors.New("HostConfig.Tmpfs must be an object")
		}
		out.Tmpfs = map[string]string{}
		for path, optsV := range tm {
			opts, ok := optsV.(string)
			if path != "/tmp" || !ok {
				return out, fmt.Errorf("HostConfig.Tmpfs %q is not allowed (only /tmp)", path)
			}
			safe, err := safeTmpfsOpts(opts)
			if err != nil {
				return out, err
			}
			out.Tmpfs[path] = safe
		}
	}

	// メモリ・プロセス数は必須（0 は無制限になる）、CPU も上限付き
	if out.Memory, err = intIn(hc["Memory"], "HostConfig.Memory", 1, maxMemory); err != nil {
		return out, err
	}
	if out.NanoCPUs, err = intIn(hc["NanoCpus"], "HostConfig.NanoCpus", 1, maxNanoCPUs); err != nil {
		return out, err
	}
	if out.PidsLimit, err = intIn(hc["PidsLimit"], "HostConfig.PidsLimit", 1, maxPidsLimit); err != nil {
		return out, err
	}

	if v, ok := hc["AutoRemove"]; ok && v != nil {
		b, ok := v.(bool)
		if !ok {
			return out, errors.New("HostConfig.AutoRemove must be a boolean")
		}
		out.AutoRemove = b
	}

	// Privileged・CapAdd・Devices・DeviceRequests・Mounts・VolumesFrom・PidMode・IpcMode・UTSMode・UsernsMode・
	// CgroupnsMode・Cgroup・CgroupParent・Sysctls・Runtime・MaskedPaths・ReadonlyPaths・PortBindings・ExtraHosts など、
	// 上で扱わなかった項目はゼロでなければ拒否し、転送しない（Docker の既定値＝安全な側になる）
	return out, rejectNonZero(hc, "HostConfig.", "Binds", "NetworkMode", "CapDrop", "SecurityOpt", "ReadonlyRootfs",
		"Tmpfs", "Memory", "NanoCpus", "PidsLimit", "AutoRemove")
}

// safeTmpfsOpts は /tmp の tmpfs のオプションを、送られた値によらず rw,noexec,nosuid,nodev,size=<上限以下> にして返す。
// size が無ければ上限（512m）にする（無いと tmpfs の既定の「メモリの半分」になる）。上限を超える size と、
// 知らないオプションは拒否する。スキャナー（clamscan・yara）は /tmp に一時ファイルを書くだけで /tmp から何も実行しない
// （sandbox/docker.go ももともと noexec を付けて送っている）
func safeTmpfsOpts(opts string) (string, error) {
	size := "512m"
	for _, o := range strings.Split(opts, ",") {
		k, v, hasValue := strings.Cut(o, "=")
		switch {
		case !hasValue && overriddenTmpfsOpts[k]:
		case hasValue && k == "mode":
		case hasValue && k == "size":
			n, err := parseTmpfsSize(v)
			if err != nil {
				return "", err
			}
			if n < 1 || n > maxTmpfsBytes {
				return "", fmt.Errorf("tmpfs size must be between 1 and %d bytes, got %q", maxTmpfsBytes, v)
			}
			size = v
		default:
			return "", fmt.Errorf("tmpfs option %q is not allowed", o)
		}
	}
	return "rw,noexec,nosuid,nodev,size=" + size, nil
}

// parseTmpfsSize は tmpfs の size=（バイト数、または k・m・g 付き。% は不可）をバイト数にする
func parseTmpfsSize(v string) (int64, error) {
	m := tmpfsSizeRe.FindStringSubmatch(v)
	if m == nil {
		return 0, fmt.Errorf("tmpfs size %q is not allowed", v)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch strings.ToLower(m[2]) {
	case "k":
		n <<= 10
	case "m":
		n <<= 20
	case "g":
		n <<= 30
	}
	return n, nil
}

func sandboxLabels(v any) (map[string]string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("Labels are required")
	}
	out := map[string]string{}
	for k, val := range m {
		s, ok := val.(string)
		if !ok || !allowedLabels[k] || len(s) > 256 {
			return nil, fmt.Errorf("label %q is not allowed", k)
		}
		out[k] = s
	}
	if out[sandboxLabelKey] != sandboxLabelValue {
		return nil, fmt.Errorf("label %s=%s is required", sandboxLabelKey, sandboxLabelValue)
	}
	return out, nil
}

func (g *guard) handleContainerOp(w http.ResponseWriter, r *http.Request, version, ref, op string) {
	want := map[string]string{"/start": http.MethodPost, "/wait": http.MethodPost, "/logs": http.MethodGet, "": http.MethodDelete}[op]
	if r.Method != want {
		g.deny(w, r, "method not allowed")
		return
	}
	q := url.Values{}
	for k, v := range r.URL.Query() {
		if !opQueries[op][k] {
			g.deny(w, r, "query not allowed: "+k)
			return
		}
		q[k] = v
	}
	if op == "/wait" && q.Get("condition") != "" {
		switch q.Get("condition") {
		case "not-running", "next-exit", "removed":
		default:
			g.deny(w, r, "wait condition not allowed")
			return
		}
	}

	// 操作の前に、相手がサンドボックスかを上流に問い合わせて確かめる
	id, status, body, err := g.sandboxID(r.Context(), version, ref)
	if err != nil {
		log.Printf("ERROR %s %q: inspect: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusBadGateway, "sfsp-docker-guard: inspect failed")
		return
	}
	if status != http.StatusOK {
		// 存在しない（404）などは上流の応答をそのまま返す（docker クライアントが NotFound として扱える）
		log.Printf("%s %q -> inspect %d", r.Method, r.URL.Path, status)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	if id == "" {
		g.deny(w, r, "container is not an sfsp sandbox")
		return
	}
	if op == "/start" {
		// 停止済みのサンドボックスを start し直して、上限を超えて同時に動かすことはできない
		g.limitMu.Lock()
		defer g.limitMu.Unlock()
		if !g.checkSandboxCount(w, r, version, id) {
			return
		}
	}
	g.forward(w, r, r.Method, version+"/containers/"+id+op, q, nil)
}

// sandboxID は ref のコンテナを上流で調べ、サンドボックスならその ID を返す（サンドボックスでなければ ""）。
// 200 以外の応答はステータスと本文を返す
func (g *guard) sandboxID(ctx context.Context, version, ref string) (string, int, []byte, error) {
	u := *g.upstream
	u.Path = version + "/containers/" + ref + "/json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", 0, nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return "", 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", 0, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, body, nil
	}
	var info struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", 0, nil, err
	}
	if !containerIDRe.MatchString(info.ID) ||
		!sandboxNameRe.MatchString(strings.TrimPrefix(info.Name, "/")) ||
		info.Config.Labels[sandboxLabelKey] != sandboxLabelValue {
		return "", http.StatusOK, nil, nil
	}
	return info.ID, http.StatusOK, nil, nil
}

// forward は検査済みのリクエストを上流に送り、応答をそのまま（逐次）返す
func (g *guard) forward(w http.ResponseWriter, r *http.Request, method, path string, q url.Values, body []byte) {
	u := *g.upstream
	u.Path = path
	u.RawQuery = q.Encode()
	var rd io.Reader = http.NoBody
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, u.String(), rd)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "sfsp-docker-guard: "+err.Error())
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		log.Printf("ERROR %s %q: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusBadGateway, "sfsp-docker-guard: upstream error")
		return
	}
	defer resp.Body.Close()
	log.Printf("%s %q -> %d", r.Method, r.URL.Path, resp.StatusCode)

	for k, vs := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Te", "Trailer", "Proxy-Connection":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// wait はヘッダーを先に返し、本文はコンテナの終了後に届くので、届いた分ずつ送る
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// deny は 403 を返す。パス（%0A などをデコードしたもの）・クエリ・理由（キー名などを含む）は信頼できないので、
// ログの行を偽造されないよう %q で書く（メソッドは net/http がトークン文字だけか確かめている）
func (g *guard) deny(w http.ResponseWriter, r *http.Request, reason string) {
	log.Printf("DENY %s %q?%q: %q", r.Method, r.URL.Path, r.URL.RawQuery, reason)
	writeError(w, http.StatusForbidden, "sfsp-docker-guard: "+reason)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": msg})
}

// ---- JSON の検査 ----

func decodeObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON")
	}
	if m == nil {
		return nil, errors.New("body must be a JSON object")
	}
	return m, nil
}

// isZero は null・false・0・""、要素がすべてゼロの配列・オブジェクト（空を含む）なら true
func isZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case json.Number:
		f, err := x.Float64()
		return err == nil && f == 0
	case []any:
		for _, e := range x {
			if !isZero(e) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range x {
			if !isZero(e) {
				return false
			}
		}
		return true
	}
	return false
}

func rejectNonZero(m map[string]any, prefix string, handled ...string) error {
	skip := map[string]bool{}
	for _, k := range handled {
		skip[k] = true
	}
	for k, v := range m {
		if !skip[k] && !isZero(v) {
			return fmt.Errorf("%s%s is not allowed", prefix, k)
		}
	}
	return nil
}

func stringList(v any, name string) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok || len(arr) > maxArgs {
		return nil, fmt.Errorf("%s must be a list of strings", name)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok || len(s) > maxArgBytes {
			return nil, fmt.Errorf("%s must be a list of strings", name)
		}
		out = append(out, s)
	}
	return out, nil
}

func intIn(v any, name string, min, max int64) (int64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s is required", name)
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	if err != nil || i < min || i > max {
		return 0, fmt.Errorf("%s must be between %d and %d, got %s", name, min, max, n)
	}
	return i, nil
}

func main() {
	upstream := envOr("SFSP_GUARD_UPSTREAM", "http://sfsp-docker-proxy:2375")
	listen := envOr("SFSP_GUARD_LISTEN", ":2375")
	g, err := newGuard(upstream, strings.Split(os.Getenv("SFSP_GUARD_ALLOWED_IMAGES"), ","), os.Getenv("SFSP_GUARD_SCAN_VOLUME"))
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if g.maxActive, err = envInt("SFSP_GUARD_MAX_ACTIVE_SANDBOXES", defaultMaxActiveSandboxes); err != nil {
		log.Fatalf("config: %v", err)
	}
	if g.maxTotal, err = envInt("SFSP_GUARD_MAX_SANDBOXES", defaultMaxSandboxes); err != nil {
		log.Fatalf("config: %v", err)
	}
	srv := &http.Server{
		Addr:              listen,
		Handler:           g,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// WriteTimeout は付けない（ContainerWait が最長 30 分かかる）
	}
	log.Printf("sfsp-docker-guard listening on %s -> %s (images=%v, bind=%s, max sandboxes: running %d, total %d)",
		listen, upstream, keys(g.images), g.scanBind, g.maxActive, g.maxTotal)
	log.Fatal(srv.ListenAndServe())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envInt は環境変数 k を 1 以上の整数として読む（未設定なら def）
func envInt(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer: %q", k, v)
	}
	return n, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
