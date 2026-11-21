package main

// testdata/create-sfsp-worker.json と testdata/requests-sfsp-worker.txt は、sfsp の internal/sandbox/docker.go
// （github.com/docker/docker v28.3.3 のクライアント）が実際に送ったリクエストを記録したもの。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
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
	case strings.HasSuffix(p, "/containers/create"):
		f.mu.Lock()
		f.createBody = body
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
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
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
		"Tmpfs suid":             hc("Tmpfs", map[string]any{"/tmp": "rw,exec,suid,dev"}),
		"Memory 0":               hc("Memory", 0),
		"Memory huge":            hc("Memory", int64(64)<<30),
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
