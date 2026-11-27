package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nq "github.com/xiecang/node-quality/v2"
	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/probe"
	"golang.org/x/sys/unix"
)

func TestNativeSingBoxDirectShadowTLSAndLargeRulesValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("native")) }))
	defer server.Close()
	node, err := codec.NewNode(map[string]any{"type": "direct", "tag": "test"}, codec.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := engine.OpenBatch(context.Background(), []codec.Node{node}, engine.SingBox)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	dial, err := batch.Dialer(node.Key)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: dial}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil || string(content) != "native" {
		t.Fatalf("content=%q error=%v", content, err)
	}
	if err = batch.Close(); err != nil {
		t.Fatal(err)
	}

	// Dependency cycle & forged identity rejected.
	if _, err := engine.OpenBatch(context.Background(), []codec.Node{{Key: "a", Dependencies: []string{"b"}}, {Key: "b", Dependencies: []string{"a"}}}, engine.SingBox); err == nil {
		t.Fatal("cycle accepted")
	}
	forged, _ := codec.NewNode(map[string]any{"name": "x", "type": "http", "server": "example.invalid", "port": 80}, codec.ClashYAML)
	forged.Config["server"] = "changed.invalid"
	if _, err := engine.OpenBatch(context.Background(), []codec.Node{forged}, engine.Mihomo); err == nil {
		t.Fatal("forged identity accepted")
	}

	// ShadowTLS front-proxy chain preservation & tag collision rejection.
	const shadowTLSSource = `proxies:
  - {name: base, type: socks5, server: 127.0.0.1, port: 1080}
  - name: protected
    type: ss
    server: example.com
    port: 443
    cipher: aes-128-gcm
    password: test
    plugin: shadow-tls
    plugin-opts: {host: www.example.com, password: test, version: 3}
    dialer-proxy: base
`
	decoded, err := codec.Decode([]byte(shadowTLSSource))
	if err != nil {
		t.Fatal(err)
	}
	sbOut, err := codec.Encode(decoded.Nodes, codec.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.CheckSingBox(context.Background(), sbOut); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	_ = json.Unmarshal(sbOut, &doc)
	byTag := map[string]map[string]any{}
	for _, ob := range doc.Outbounds {
		byTag[ob["tag"].(string)] = ob
	}
	if byTag["protected"]["detour"] != "protected-shadowtls" || byTag["protected-shadowtls"]["detour"] != "base" {
		t.Fatalf("unexpected ShadowTLS chain: %+v", byTag)
	}
	collisionDecoded, err := codec.Decode([]byte(strings.ReplaceAll(shadowTLSSource, "base", "protected-shadowtls")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Encode(collisionDecoded.Nodes, codec.SingBoxJSON); err == nil || !strings.Contains(err.Error(), "duplicate sing-box tag") {
		t.Fatalf("expected duplicate sing-box tag error, got %v", err)
	}

	// 2000-rule CheckSingBox validation with JSON comments.
	rules := make([]any, 2000)
	for i := range rules {
		rules[i] = map[string]any{"domain_suffix": fmt.Sprintf("%d.example.invalid", i), "action": "reject"}
	}
	largeDoc := map[string]any{
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"rules": rules, "final": "direct"},
	}
	rawLarge, _ := json.Marshal(largeDoc)
	rawLarge = []byte(strings.Replace(string(rawLarge), "{", "{ // native config\n", 1))
	if err := engine.CheckSingBox(context.Background(), rawLarge); err != nil {
		t.Fatal(err)
	}

	// DisplayNames deterministic disambiguation.
	var dupNodes []codec.Node
	for i, name := range []string{"🇯🇵 自建", "🇯🇵 自建", "DIRECT", "personal group"} {
		n, _ := codec.NewNode(map[string]any{"name": name, "type": "http", "server": "fixture.invalid", "port": 8000 + i}, codec.ClashYAML)
		dupNodes = append(dupNodes, n)
	}
	names := codec.DisplayNames(dupNodes, "personal group")
	used := map[string]bool{"DIRECT": true, "personal group": true}
	for _, n := range dupNodes {
		if names[n.Key] == "" || used[names[n.Key]] {
			t.Fatalf("ambiguous DisplayNames: %v", names)
		}
		used[names[n.Key]] = true
	}
}

func TestAIParallelLongRun(t *testing.T) {
	if os.Getenv("NQ_AI_LONG_TEST") != "1" {
		t.Skip("set NQ_AI_LONG_TEST=1 for the real 180s acceptance")
	}
	var active, peak atomic.Int64
	handler := probe.Handler(probe.Options{})
	probeSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isStream := strings.HasSuffix(r.URL.Path, "/sse") || strings.HasSuffix(r.URL.Path, "/ws")
		if isStream {
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			defer active.Add(-1)
		}
		handler.ServeHTTP(w, r)
	}))
	probeSrv.EnableHTTP2 = true
	probeSrv.StartTLS()
	defer probeSrv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(probeSrv.Certificate())
	for _, concurrency := range []int{5, 10} {
		t.Run(strconv.Itoa(concurrency), func(t *testing.T) {
			var nodes []codec.Node
			for i := 0; i < concurrency; i++ {
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
					if err != nil {
						http.Error(w, "dial", 502)
						return
					}
					defer upstream.Close()
					connection, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						return
					}
					defer connection.Close()
					_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
					_ = buffer.Flush()
					done := make(chan struct{})
					go func() { _, _ = io.Copy(upstream, buffer); upstream.Close(); close(done) }()
					_, _ = io.Copy(connection, upstream)
					connection.Close()
					<-done
				}))
				t.Cleanup(proxy.Close)
				node, err := codec.NewNode(map[string]any{"name": "probe", "type": "http", "server": "127.0.0.1", "port": proxy.Listener.Addr().(*net.TCPAddr).Port}, codec.ClashYAML)
				if err != nil {
					t.Fatal(err)
				}
				nodes = append(nodes, node)
			}
			for _, core := range []engine.Core{engine.Mihomo, engine.SingBox} {
				plan := nq.DefaultPlan()
				plan.Core = core
				plan.Profile = "ai"
				plan.ProbeURL = probeSrv.URL
				plan.ExitIPURL = ""
				plan.ProbeTLSConfig = &tls.Config{RootCAs: roots}
				plan.DownloadBytes = 50 << 20
				plan.AI.Concurrency = concurrency
				peak.Store(0)
				var cpuBefore unix.Rusage
				_ = unix.Getrusage(unix.RUSAGE_SELF, &cpuBefore)
				reports, err := nq.Evaluate(context.Background(), nodes, plan, nq.NewBudget(10<<20))
				if err != nil {
					t.Fatal(err)
				}
				for _, report := range reports {
					if report.Status != nq.Pass {
						t.Fatalf("core=%s report=%+v", core, report)
					}
				}
			}
		})
	}
}
