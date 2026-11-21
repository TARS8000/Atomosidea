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
	"time"
)

// サンドボックスに許す資源の上限（internal/sandbox/docker.go の値：メモリ 1.5 GiB・1 CPU・プロセス 256）。
// 上げるときはここも変える
const (
	maxMemory    = 2 << 30 // 2 GiB
	maxNanoCPUs  = 2_000_000_000
	maxPidsLimit = 1024
	maxBodyBytes = 1 << 20
	maxArgs      = 256
	maxArgBytes  = 8192
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
	// /tmp の tmpfs に付けてよいオプション（size= と mode= は別に許す）
	allowedTmpfsOpts = map[string]bool{"rw": true, "noexec": true, "nosuid": true, "nodev": true}
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
		upstream: u,
		images:   map[string]bool{},
		scanBind: scanVolume + ":" + scanTarget + ":ro",
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
	g.forward(w, r, http.MethodPost, version+"/containers/create", url.Values{"name": {q.Get("name")}}, out)
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
			if err := checkTmpfsOpts(opts); err != nil {
				return out, err
			}
			out.Tmpfs[path] = opts
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

func checkTmpfsOpts(opts string) error {
	seen := map[string]bool{}
	for _, o := range strings.Split(opts, ",") {
		switch {
		case allowedTmpfsOpts[o]:
			seen[o] = true
		case strings.HasPrefix(o, "size=") || strings.HasPrefix(o, "mode="):
		default:
			return fmt.Errorf("tmpfs option %q is not allowed", o)
		}
	}
	if !seen["nosuid"] || !seen["nodev"] {
		return errors.New("tmpfs must be nosuid,nodev")
	}
	return nil
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
		log.Printf("ERROR %s %s: inspect: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusBadGateway, "sfsp-docker-guard: inspect failed")
		return
	}
	if status != http.StatusOK {
		// 存在しない（404）などは上流の応答をそのまま返す（docker クライアントが NotFound として扱える）
		log.Printf("%s %s -> inspect %d", r.Method, r.URL.Path, status)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	if id == "" {
		g.deny(w, r, "container is not an sfsp sandbox")
		return
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
		log.Printf("ERROR %s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusBadGateway, "sfsp-docker-guard: upstream error")
		return
	}
	defer resp.Body.Close()
	log.Printf("%s %s -> %d", r.Method, r.URL.Path, resp.StatusCode)

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

func (g *guard) deny(w http.ResponseWriter, r *http.Request, reason string) {
	log.Printf("DENY %s %s?%s: %s", r.Method, r.URL.Path, r.URL.RawQuery, reason)
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
	srv := &http.Server{
		Addr:              listen,
		Handler:           g,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// WriteTimeout は付けない（ContainerWait が最長 30 分かかる）
	}
	log.Printf("sfsp-docker-guard listening on %s -> %s (images=%v, bind=%s)", listen, upstream, keys(g.images), g.scanBind)
	log.Fatal(srv.ListenAndServe())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
