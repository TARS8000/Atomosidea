package main

// testdata/create-sfsp-worker.json と testdata/requests-sfsp-worker.txt は、sfsp の internal/sandbox/docker.go
// （github.com/docker/docker v28.3.3 のクライアント）が実際に送ったリクエストを記録したもの。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sandboxID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	victimID  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sbName    = "sfsp-sandbox-yara-cde771ac-0607-47f0-8004-66295d7b2793"
)

// fakeDocker は上流（docker-socket-proxy）の代わり。届いたリクエストを記録する
type fakeDocker struct {
	mu         sync.Mutex
	reqs       []string
	createBody []byte
	// GET /containers/json が返すコンテナ（作成のたびに State: created で 1 つ足す）
	listed    []fakeContainer
	listError bool          // GET /containers/json を 500 にする
	listDelay time.Duration // GET /containers/json の返事を遅らせる（上流が固まったとき）
	onStart   func(*fakeDocker)
	created   int
}

type fakeContainer struct {
	ID     string            `json:"Id"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
}

func sandboxIn(state string) fakeContainer {
	return fakeContainer{ID: strings.Repeat("c", 64), State: state, Labels: map[string]string{"app": "sfsp-sandbox"}}
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI())
	f.mu.Unlock()
	w.Header().Set("Api-Version", "1.51")
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/_ping"):
		w.Write([]byte("OK"))
	case strings.HasSuffix(p, "/containers/json"):
		if f.listDelay > 0 {
			select {
			case <-time.After(f.listDelay):
			case <-r.Context().Done():
				return
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.listError {
			w.WriteHeader(500)
			return
		}
		b, _ := json.Marshal(append([]fakeContainer{}, f.listed...))
		w.Write(b)
	case strings.HasSuffix(p, "/containers/create"):
		f.mu.Lock()
		f.createBody = body
		f.created++
		f.listed = append(f.listed, fakeContainer{ID: fmt.Sprintf("%064x", f.created), State: "created",
			Labels: map[string]string{"app": "sfsp-sandbox"}})
		f.mu.Unlock()
		w.WriteHeader(201)
		w.Write([]byte(`{"Id":"` + sandboxID + `"}`))
	case strings.HasSuffix(p, "/containers/"+sandboxID+"/json"), strings.HasSuffix(p, "/containers/"+sbName+"/json"):
		w.Write([]byte(`{"Id":"` + sandboxID + `","Name":"/` + sbName + `","Config":{"Labels":{"app":"sfsp-sandbox"}}}`))
	case strings.HasSuffix(p, "/containers/sfsp-db/json"):
		w.Write([]byte(`{"Id":"` + victimID + `","Name":"/sfsp-db","Config":{"Labels":{"app":"sfsp-sandbox"}}}`))
	case strings.HasSuffix(p, "/containers/sfsp-sandbox-fake-1/json"): // 名前は合うがラベルが無い
		w.Write([]byte(`{"Id":"` + victimID + `","Name":"/sfsp-sandbox-fake-1","Config":{"Labels":{}}}`))
	case strings.HasSuffix(p, "/json") && strings.Contains(p, "/containers/"):
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"No such container"}`))
	case strings.HasSuffix(p, "/start"):
		f.mu.Lock()
		if f.onStart != nil {
			f.onStart(f)
		}
		f.mu.Unlock()
		w.WriteHeader(200) // 既存のテストに合わせて 200（本物の Docker は 204）
	case strings.HasSuffix(p, "/wait"):
		w.Write([]byte(`{"StatusCode":0}`))
	default:
		w.WriteHeader(200)
	}
}

func (f *fakeDocker) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

func setup(t *testing.T) (*fakeDocker, *httptest.Server) {
	t.Helper()
	f := &fakeDocker{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	g, err := newGuard(up.URL, []string{"sfsp-clamav-client:latest", "sfsp-yara-client:latest"}, "sfsp_temp_scan")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return f, srv
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func realCreate(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/create-sfsp-worker.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func marshal(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sfsp-worker が実際に送るリクエストの並びは、すべて通る
func TestSfspWorkerSequenceAllowed(t *testing.T) {
	b, err := os.ReadFile("testdata/requests-sfsp-worker.txt")
	if err != nil {
		t.Fatal(err)
	}
	create, _ := os.ReadFile("testdata/create-sfsp-worker.json")
	_, srv := setup(t)
	// 行末の \r も落とす（Windows で core.autocrlf=true のまま取り出した場合。.gitattributes で LF に固定もしている）
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		line = strings.TrimSpace(line)
		method, path, _ := strings.Cut(line, " ")
		body := ""
		if strings.Contains(path, "/containers/create") {
			body = string(create)
		}
		resp := do(t, srv, method, path, body)
		if resp.StatusCode >= 300 {
			t.Errorf("%s: got %d", line, resp.StatusCode)
		}
	}
}

// 転送される作成の本文は、許可した項目だけで作り直したもの
func TestCreateForwardsOnlyAllowedFields(t *testing.T) {
	f, srv := setup(t)
	m := realCreate(t)
	// ゼロ値の未知の項目は落とされる（MaskedPaths: [] は Docker では「マスクしない」になるので、転送されてはいけない）
	m["HostConfig"].(map[string]any)["MaskedPaths"] = []any{}
	m["HostConfig"].(map[string]any)["ReadonlyPaths"] = []any{}
	resp := do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, marshal(t, m))
	if resp.StatusCode != 201 {
		t.Fatalf("got %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.Unmarshal(f.createBody, &got); err != nil {
		t.Fatal(err)
	}
	want := `{"Cmd":null,"Entrypoint":["yara","-r","-w","-m","/etc/yara-rules/general.yar","/scan_target/job/file.bin"],` +
		`"HostConfig":{"AutoRemove":false,"Binds":["sfsp_temp_scan:/scan_target:ro"],"CapDrop":["ALL"],"Memory":1610612736,` +
		`"NanoCpus":1000000000,"NetworkMode":"none","PidsLimit":256,"ReadonlyRootfs":true,"SecurityOpt":["no-new-privileges"],` +
		`"Tmpfs":{"/tmp":"rw,noexec,nosuid,nodev,size=512m"}},"Image":"sfsp-yara-client:latest",` +
		`"Labels":{"app":"sfsp-sandbox","job_id":"cde771ac-0607-47f0-8004-66295d7b2793","scanner":"yara"},"WorkingDir":"/scan_target"}`
	got["Cmd"] = nil // omitempty で無い
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != want {
		t.Errorf("forwarded body\n got %s\nwant %s", gotJSON, want)
	}
	if !strings.Contains(strings.Join(f.requests(), "\n"), "POST /v1.51/containers/create?name="+sbName) {
		t.Errorf("not forwarded: %v", f.requests())
	}
}

func TestCreateRejectsDangerousConfig(t *testing.T) {
	hc := func(k string, v any) func(map[string]any) {
		return func(m map[string]any) { m["HostConfig"].(map[string]any)[k] = v }
	}
	top := func(k string, v any) func(map[string]any) {
		return func(m map[string]any) { m[k] = v }
	}
	cases := map[string]func(map[string]any){
		"Privileged":             hc("Privileged", true),
		"privileged (lowercase)": hc("privileged", true),
		"CapAdd":                 hc("CapAdd", []any{"SYS_ADMIN"}),
		"Binds host root":        hc("Binds", []any{"/:/host"}),
		"Binds rw":               hc("Binds", []any{"sfsp_temp_scan:/scan_target"}),
		"Binds extra":            hc("Binds", []any{"sfsp_temp_scan:/scan_target:ro", "/var/run/docker.sock:/s"}),
		"Binds missing":          hc("Binds", nil),
		"Mounts":                 hc("Mounts", []any{map[string]any{"Type": "bind", "Source": "/", "Target": "/host"}}),
		"Devices":                hc("Devices", []any{map[string]any{"PathOnHost": "/dev/sda", "PathInContainer": "/dev/sda"}}),
		"DeviceRequests":         hc("DeviceRequests", []any{map[string]any{"Count": -1}}),
		"DeviceCgroupRules":      hc("DeviceCgroupRules", []any{"a *:* rwm"}),
		"VolumesFrom":            hc("VolumesFrom", []any{"sfsp-worker"}),
		"PidMode host":           hc("PidMode", "host"),
		"PidMode container":      hc("PidMode", "container:sfsp-worker"),
		"IpcMode host":           hc("IpcMode", "host"),
		"UTSMode host":           hc("UTSMode", "host"),
		"UsernsMode host":        hc("UsernsMode", "host"),
		"CgroupnsMode host":      hc("CgroupnsMode", "host"),
		"CgroupParent":           hc("CgroupParent", "/"),
		"NetworkMode host":       hc("NetworkMode", "host"),
		"NetworkMode bridge":     hc("NetworkMode", "bridge"),
		"NetworkMode container":  hc("NetworkMode", "container:sfsp-worker"),
		"SecurityOpt seccomp":    hc("SecurityOpt", []any{"no-new-privileges", "seccomp=unconfined"}),
		"SecurityOpt apparmor":   hc("SecurityOpt", []any{"no-new-privileges", "apparmor=unconfined"}),
		"SecurityOpt label":      hc("SecurityOpt", []any{"no-new-privileges", "label=disable"}),
		"SecurityOpt missing":    hc("SecurityOpt", nil),
		"CapDrop missing":        hc("CapDrop", nil),
		"ReadonlyRootfs false":   hc("ReadonlyRootfs", false),
		"Sysctls":                hc("Sysctls", map[string]any{"kernel.core_pattern": "|/x"}),
		"Runtime":                hc("Runtime", "runc-unsafe"),
		"Tmpfs other path":       hc("Tmpfs", map[string]any{"/etc": "rw"}),
		"Tmpfs size too big":     hc("Tmpfs", map[string]any{"/tmp": "rw,noexec,nosuid,nodev,size=513m"}),
		"Tmpfs size percent":     hc("Tmpfs", map[string]any{"/tmp": "size=50%"}),
		"Tmpfs size garbage":     hc("Tmpfs", map[string]any{"/tmp": "size=1e9"}),
		"Tmpfs uid":              hc("Tmpfs", map[string]any{"/tmp": "rw,noexec,nosuid,nodev,uid=0"}),
		"Tmpfs not string":       hc("Tmpfs", map[string]any{"/tmp": 1}),
		"Tmpfs not object":       hc("Tmpfs", []any{"/tmp"}),
		"Memory 0":               hc("Memory", 0),
		"Memory huge":            hc("Memory", int64(64)<<30),
		"Memory over worker":     hc("Memory", int64(1536<<20)+1),
		"NanoCpus over worker":   hc("NanoCpus", int64(1_000_000_001)),
		"PidsLimit over worker":  hc("PidsLimit", 257),
		"PidsLimit -1":           hc("PidsLimit", -1),
		"PidsLimit missing":      hc("PidsLimit", nil),
		"NanoCpus missing":       hc("NanoCpus", nil),
		"ExtraHosts":             hc("ExtraHosts", []any{"x:1.2.3.4"}),
		"PortBindings":           hc("PortBindings", map[string]any{"22/tcp": []any{map[string]any{"HostPort": "22"}}}),
		"OomKillDisable":         hc("OomKillDisable", true),
		"HostConfig missing":     top("HostConfig", nil),
		"hostconfig (lowercase)": top("hostconfig", map[string]any{"Privileged": true}),
		"Image other":            top("Image", "alpine:latest"),
		"User":                   top("User", "0"),
		"Env":                    top("Env", []any{"LD_PRELOAD=/x"}),
		"Labels missing app":     top("Labels", map[string]any{"scanner": "yara"}),
		"Labels other":           top("Labels", map[string]any{"app": "sfsp-sandbox", "com.docker.compose.project": "atomosidea"}),
		"Entrypoint not strings": top("Entrypoint", []any{1}),
		"WorkingDir relative":    top("WorkingDir", "../"),
		"NetworkingConfig": top("NetworkingConfig", map[string]any{"EndpointsConfig": map[string]any{
			"atmosidea-network": map[string]any{"NetworkID": "atmosidea-network"}}}),
	}

	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f, srv := setup(t)
			m := realCreate(t)
			mut(m)
			resp := do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, marshal(t, m))
			if resp.StatusCode != 403 {
				t.Errorf("got %d, want 403", resp.StatusCode)
			}
			if len(f.requests()) != 0 {
				t.Errorf("forwarded to upstream: %v", f.requests())
			}
		})
	}
}

func TestCreateRejectsBadNameOrQuery(t *testing.T) {
	body, _ := os.ReadFile("testdata/create-sfsp-worker.json")
	for _, q := range []string{
		"", "?name=sfsp-db", "?name=evil", "?name=" + sbName + "&name=x", "?name=" + sbName + "&platform=linux/amd64",
		"?name=" + sbName + "&foo=1",
	} {
		f, srv := setup(t)
		resp := do(t, srv, "POST", "/v1.51/containers/create"+q, string(body))
		if resp.StatusCode != 403 || len(f.requests()) != 0 {
			t.Errorf("%q: got %d, upstream %v", q, resp.StatusCode, f.requests())
		}
	}
	// 不正な JSON・末尾のゴミ・巨大な本文
	for _, b := range []string{"", "null", "[]", "{", string(body) + "{}", strings.Repeat(" ", maxBodyBytes+1)} {
		f, srv := setup(t)
		resp := do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, b)
		if resp.StatusCode != 403 || len(f.requests()) != 0 {
			t.Errorf("body %.20q: got %d", b, resp.StatusCode)
		}
	}
}

func TestEndpointsDenied(t *testing.T) {
	cases := []struct{ method, path string }{
		{"GET", "/version"},
		{"GET", "/v1.51/version"},
		{"GET", "/v1.51/info"},
		{"GET", "/v1.51/containers/json"},
		{"POST", "/v1.51/containers/" + sbName + "/exec"},
		{"POST", "/v1.51/exec/abc/start"},
		{"PUT", "/v1.51/containers/" + sbName + "/archive?path=/"},
		{"GET", "/v1.51/containers/" + sbName + "/archive?path=/"},
		{"POST", "/v1.51/containers/" + sbName + "/kill"},
		{"POST", "/v1.51/containers/" + sbName + "/update"},
		{"POST", "/v1.51/containers/" + sbName + "/rename?name=x"},
		{"POST", "/v1.51/containers/" + sbName + "/attach"},
		{"GET", "/v1.51/containers/" + sbName + "/json"},
		{"POST", "/v1.51/containers/prune"},
		{"POST", "/v1.51/images/create?fromImage=alpine"},
		{"POST", "/v1.51/images/load"},
		{"POST", "/v1.51/images/sfsp-yara-client:latest/tag?repo=x"},
		{"DELETE", "/v1.51/images/sfsp-yara-client:latest"},
		{"GET", "/v1.51/images/json"},
		{"GET", "/v1.51/images/alpine:latest/json"},
		{"POST", "/v1.51/images/sfsp-yara-client:latest/json"},
		{"POST", "/v1.51/build"},
		{"POST", "/v1.51/volumes/create"},
		{"GET", "/v1.51/networks"},
		{"POST", "/v1.51/_ping"},
		{"GET", "/v1.51/containers/create/../../build"},
		{"GET", "/v1.51/containers/x%2F..%2F..%2Fbuild/logs"},
		{"POST", "//v1.51/containers/create"},
		{"GET", "/v1.51/containers/" + sbName + "/start"},
		{"POST", "/v1.51/containers/" + sbName + "/logs"},
		{"POST", "/v1.51/containers/" + sbName + "/start?checkpoint=x"},
		{"POST", "/v1.51/containers/" + sbName + "/wait?condition=evil"},
		{"DELETE", "/v1.51/containers/" + sbName + "?link=1"},
		{"GET", "/v1.51/containers/" + sbName + "/logs?follow=1&details=1"},
	}
	for _, c := range cases {
		f, srv := setup(t)
		resp := do(t, srv, c.method, c.path, "")
		if resp.StatusCode != 403 && resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Errorf("%s %s: got %d", c.method, c.path, resp.StatusCode)
		}
		for _, r := range f.requests() {
			if !strings.HasSuffix(strings.SplitN(r, "?", 2)[0], "/json") || !strings.HasPrefix(r, "GET ") {
				t.Errorf("%s %s: forwarded %q", c.method, c.path, r)
			}
		}
	}
}

// サンドボックス以外のコンテナ（compose のサービスなど）は start・wait・logs・削除できない
func TestOpsOnNonSandboxDenied(t *testing.T) {
	for _, ref := range []string{"sfsp-db", "sfsp-sandbox-fake-1"} {
		for _, op := range []struct{ method, suffix string }{
			{"POST", "/start"}, {"POST", "/wait"}, {"GET", "/logs?stdout=1"}, {"DELETE", "?force=1"},
		} {
			f, srv := setup(t)
			resp := do(t, srv, op.method, "/v1.51/containers/"+ref+op.suffix, "")
			if resp.StatusCode != 403 {
				t.Errorf("%s %s%s: got %d", op.method, ref, op.suffix, resp.StatusCode)
			}
			for _, r := range f.requests() {
				if r != "GET /v1.51/containers/"+ref+"/json" {
					t.Errorf("%s %s%s: forwarded %q", op.method, ref, op.suffix, r)
				}
			}
		}
	}
}

// サンドボックスへの操作は、確かめた ID に対して転送される。存在しなければ 404 を返す
func TestOpsOnSandboxUseInspectedID(t *testing.T) {
	f, srv := setup(t)
	if resp := do(t, srv, "DELETE", "/v1.51/containers/"+sbName+"?force=1", ""); resp.StatusCode != 200 {
		t.Fatalf("got %d", resp.StatusCode)
	}
	want := []string{"GET /v1.51/containers/" + sbName + "/json", "DELETE /v1.51/containers/" + sandboxID + "?force=1"}
	if got := f.requests(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v want %v", got, want)
	}

	f, srv = setup(t)
	if resp := do(t, srv, "DELETE", "/v1.51/containers/sfsp-sandbox-yara-gone?force=1", ""); resp.StatusCode != 404 {
		t.Errorf("missing container: got %d, want 404", resp.StatusCode)
	}
	if len(f.requests()) != 1 {
		t.Errorf("forwarded more than inspect: %v", f.requests())
	}
}

func TestNewGuardRejectsHostPathVolume(t *testing.T) {
	for _, v := range []string{"", "/", "/var/run/docker.sock", "../x"} {
		if _, err := newGuard("http://x:2375", []string{"a"}, v); err == nil {
			t.Errorf("volume %q accepted", v)
		}
	}
	if _, err := newGuard("http://x:2375", []string{" ", ""}, "sfsp_temp_scan"); err == nil {
		t.Error("empty image list accepted")
	}
}

// /tmp の tmpfs は、送られたオプションによらず rw,noexec,nosuid,nodev とサイズ上限付きで作り直して転送する
func TestTmpfsOptionsForced(t *testing.T) {
	for in, want := range map[string]string{
		"rw,noexec,nosuid,nodev,size=512m": "rw,noexec,nosuid,nodev,size=512m", // sandbox/docker.go が送るもの
		"rw,exec,suid,dev":                 "rw,noexec,nosuid,nodev,size=512m",
		"ro,mode=0777":                     "rw,noexec,nosuid,nodev,size=512m",
		"size=64m":                         "rw,noexec,nosuid,nodev,size=64m",
		"size=1048576":                     "rw,noexec,nosuid,nodev,size=1048576",
		"exec,size=524288k":                "rw,noexec,nosuid,nodev,size=524288k",
	} {
		f, srv := setup(t)
		m := realCreate(t)
		m["HostConfig"].(map[string]any)["Tmpfs"] = map[string]any{"/tmp": in}
		if resp := do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, marshal(t, m)); resp.StatusCode != 201 {
			t.Fatalf("%q: got %d", in, resp.StatusCode)
		}
		var got createRequest
		if err := json.Unmarshal(f.createBody, &got); err != nil {
			t.Fatal(err)
		}
		if got.HostConfig.Tmpfs["/tmp"] != want || len(got.HostConfig.Tmpfs) != 1 {
			t.Errorf("%q: forwarded %q, want %q", in, got.HostConfig.Tmpfs, want)
		}
	}
	for _, s := range []string{"1g", "513m", "536870913", "1G", "50%", "", "m", "-1", "999999999999g", "999999999999k", "999999999999"} {
		if _, err := safeTmpfsOpts("size=" + s); err == nil {
			t.Errorf("size=%s accepted", s)
		}
	}
}

// サンドボックスの数の上限：作成は、動いているものが maxActive 以上か、停止済みを含めて maxTotal 以上なら拒否する
func TestCreateSandboxLimit(t *testing.T) {
	running, exited := sandboxIn("running"), sandboxIn("exited")
	other := fakeContainer{ID: strings.Repeat("d", 64), State: "running", Labels: map[string]string{"app": "sfsp-db"}}
	cases := []struct {
		name   string
		listed []fakeContainer
		want   int
	}{
		{"none", nil, 201},
		{"one orphan running", []fakeContainer{running}, 201},
		{"two running", []fakeContainer{running, running}, 403},
		{"paused and restarting count", []fakeContainer{sandboxIn("paused"), sandboxIn("restarting")}, 403},
		// 作っただけ（created）の残りはスキャンを止めない（start のときに数える）
		{"created leftovers do not count as running", []fakeContainer{sandboxIn("created"), sandboxIn("created"), running}, 201},
		{"exited do not count as running", []fakeContainer{exited, exited, exited, sandboxIn("dead")}, 201},
		{"total limit", repeat(exited, defaultMaxSandboxes), 403},
		{"below total limit", repeat(exited, defaultMaxSandboxes-1), 201},
		{"other labels ignored", []fakeContainer{other, other, other}, 201},
	}
	for _, c := range cases {
		f, srv := setup(t)
		f.listed = c.listed
		resp := do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, marshal(t, realCreate(t)))
		if resp.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.want)
		}
		reqs := f.requests()
		if len(reqs) == 0 || !strings.HasPrefix(reqs[0], "GET /v1.51/containers/json?") {
			t.Fatalf("%s: did not count first: %v", c.name, reqs)
		}
		q, _ := url.ParseQuery(strings.SplitN(reqs[0], "?", 2)[1])
		if q.Get("all") != "1" || q.Get("filters") != `{"label":["app=sfsp-sandbox"]}` {
			t.Errorf("%s: count query %v", c.name, q)
		}
		if forwarded := len(reqs) == 2; forwarded != (c.want == 201) {
			t.Errorf("%s: requests %v", c.name, reqs)
		}
	}

	// 数えられなければ作らない（502）
	f, srv := setup(t)
	f.listError = true
	if resp := do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, marshal(t, realCreate(t))); resp.StatusCode != 502 {
		t.Errorf("list error: got %d, want 502", resp.StatusCode)
	}
	if len(f.requests()) != 1 {
		t.Errorf("list error: forwarded %v", f.requests())
	}
}

// 同時に来た作成も、数えてから作るまでを直列にするので上限（停止済みを含めた数）を超えない。
// created は「動いている」に数えないので、作成を止めるのは maxTotal（動かすのを止めるのは start の検査）
func TestCreateSandboxLimitConcurrent(t *testing.T) {
	f, srv := setup(t)
	var wg sync.WaitGroup
	n := defaultMaxSandboxes + 8
	codes := make(chan int, n)
	body := marshal(t, realCreate(t))
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, body).StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == 201 {
			ok++
		} else if c != 403 {
			t.Errorf("got %d", c)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if ok != defaultMaxSandboxes || f.created != defaultMaxSandboxes {
		t.Errorf("created %d (forwarded %d), want %d", ok, f.created, defaultMaxSandboxes)
	}
}

// 同時に来た start も直列にするので、動いているものは maxActive を超えない
func TestStartSandboxLimitConcurrent(t *testing.T) {
	f, srv := setup(t)
	// 作っただけのサンドボックスが 10 個。start されたら running にする
	for i := 0; i < 10; i++ {
		f.listed = append(f.listed, fakeContainer{ID: sandboxID, State: "created", Labels: map[string]string{"app": "sfsp-sandbox"}})
	}
	f.onStart = func(f *fakeDocker) { // f.mu を持って呼ばれる
		for i := range f.listed {
			if f.listed[i].State == "created" {
				f.listed[i].State = "running"
				f.listed[i].ID = fmt.Sprintf("%064x", i+100) // start した当のコンテナとは別のものとして数えさせる
				return
			}
		}
	}
	var wg sync.WaitGroup
	codes := make(chan int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- do(t, srv, "POST", "/v1.51/containers/"+sandboxID+"/start", "").StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == 200 {
			ok++
		} else if c != 403 {
			t.Errorf("got %d", c)
		}
	}
	if ok != defaultMaxActiveSandboxes {
		t.Errorf("started %d, want %d", ok, defaultMaxActiveSandboxes)
	}
}

// 上流が固まっても、数える問い合わせは countTimeout で諦めて 502 を返し、ロックを持ち続けない
func TestCountSandboxesTimeout(t *testing.T) {
	f := &fakeDocker{listDelay: 2 * time.Second}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	g, err := newGuard(up.URL, []string{"sfsp-clamav-client:latest", "sfsp-yara-client:latest"}, "sfsp_temp_scan")
	if err != nil {
		t.Fatal(err)
	}
	g.countTimeout = 100 * time.Millisecond
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	start := time.Now()
	if resp := do(t, srv, "POST", "/v1.51/containers/create?name="+sbName, marshal(t, realCreate(t))); resp.StatusCode != 502 {
		t.Errorf("got %d, want 502", resp.StatusCode)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v, want about countTimeout", d)
	}
	f.mu.Lock()
	created := f.created
	f.mu.Unlock()
	if created != 0 {
		t.Errorf("forwarded create after timeout")
	}
}

// 停止済みのサンドボックスを start し直して、上限を超えて動かすことはできない（start する当のコンテナは数えない）
func TestStartSandboxLimit(t *testing.T) {
	self := fakeContainer{ID: sandboxID, State: "created", Labels: map[string]string{"app": "sfsp-sandbox"}}
	for _, c := range []struct {
		name   string
		listed []fakeContainer
		want   int
	}{
		{"only itself", []fakeContainer{self}, 200},
		{"itself and one orphan", []fakeContainer{self, sandboxIn("running")}, 200},
		{"two others running", []fakeContainer{self, sandboxIn("running"), sandboxIn("running")}, 403},
	} {
		f, srv := setup(t)
		f.listed = c.listed
		if resp := do(t, srv, "POST", "/v1.51/containers/"+sandboxID+"/start", ""); resp.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, resp.StatusCode, c.want)
		}
		started := false
		for _, r := range f.requests() {
			started = started || r == "POST /v1.51/containers/"+sandboxID+"/start"
		}
		if started != (c.want == 200) {
			t.Errorf("%s: requests %v", c.name, f.requests())
		}
	}
	f, srv := setup(t)
	f.listError = true
	if resp := do(t, srv, "POST", "/v1.51/containers/"+sandboxID+"/start", ""); resp.StatusCode != 502 {
		t.Errorf("list error: got %d, want 502", resp.StatusCode)
	}
}

// 拒否のログは %q で書くので、パスやキーに改行（%0A）を入れてもログの行を偽造できない
func TestDenyLogEscapesUntrustedInput(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	_, srv := setup(t)
	do(t, srv, "GET", "/v1.51/x%0A2026/01/01%2000:00:00%20POST%20%22/v1.51/containers/create%22%20-%3E%20201", "")
	do(t, srv, "POST", "/v1.51/containers/create?name="+sbName+"&a%0Ab=1", "")
	out := buf.String()
	if n := strings.Count(out, "\n"); n != 2 {
		t.Errorf("want 2 log lines, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, `\n2026/01/01 00:00:00 POST`) || !strings.Contains(out, `query not allowed: a\nb`) {
		t.Errorf("newline not escaped:\n%s", out)
	}
}

// 資源の上限と tmpfs が sfsp の internal/sandbox/docker.go（../sfsp）の値と揃っているか（リポジトリを取り出した手元の go test で確かめる。
// Docker のビルドではコンテキストに無いので飛ばす）
func TestLimitsMatchSandboxDockerGo(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "sfsp", "internal", "sandbox", "docker.go"))
	if err != nil {
		t.Skip("sandbox/docker.go not found:", err)
	}
	find := func(re string) string {
		m := regexp.MustCompile(re).FindSubmatch(src)
		if m == nil {
			t.Fatalf("%s not found in docker.go; update this test and the limits in main.go", re)
		}
		return string(m[1])
	}
	mem, _ := strconv.ParseInt(find(`Memory:\s+([0-9]+) \* 1024 \* 1024,`), 10, 64)
	cpus, _ := strconv.ParseInt(strings.ReplaceAll(find(`NanoCPUs:\s+([0-9_]+),`), "_", ""), 10, 64)
	pids, _ := strconv.ParseInt(find(`pidsLimit := int64\(([0-9]+)\)`), 10, 64)
	tmpfs := find(`sandboxTmpfsOptions = "([^"]+)"`)
	if mem<<20 != maxMemory || cpus != maxNanoCPUs || pids != maxPidsLimit {
		t.Errorf("docker.go sends Memory=%dMiB NanoCPUs=%d PidsLimit=%d; guard allows %d / %d / %d",
			mem, cpus, pids, maxMemory, maxNanoCPUs, maxPidsLimit)
	}
	if got, err := safeTmpfsOpts(tmpfs); err != nil || got != tmpfs {
		t.Errorf("docker.go tmpfs %q -> %q, %v", tmpfs, got, err)
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("SFSP_GUARD_TEST_N", "")
	if n, err := envInt("SFSP_GUARD_TEST_N", 7); n != 7 || err != nil {
		t.Errorf("unset: %d %v", n, err)
	}
	t.Setenv("SFSP_GUARD_TEST_N", "3")
	if n, err := envInt("SFSP_GUARD_TEST_N", 7); n != 3 || err != nil {
		t.Errorf("3: %d %v", n, err)
	}
	for _, v := range []string{"0", "-1", "x"} {
		t.Setenv("SFSP_GUARD_TEST_N", v)
		if _, err := envInt("SFSP_GUARD_TEST_N", 7); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}

func repeat(c fakeContainer, n int) []fakeContainer {
	out := make([]fakeContainer, n)
	for i := range out {
		out[i] = c
	}
	return out
}
