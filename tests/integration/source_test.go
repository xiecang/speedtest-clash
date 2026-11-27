package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/codec"
)

func TestDetailedSourceBoundariesAnnouncementsAndProviders(t *testing.T) {
	// Supported retained, unsupported URI & broken dependency isolated.
	var nodes []codec.Node
	summary, err := codec.LoadSourceDetailed(context.Background(), "inline:http://example.invalid:8080#Tokyo\nunknownproto://example.invalid:9000#bad", nil, func(batch []codec.Node) error {
		nodes = append(nodes, batch...)
		return nil
	})
	if err != nil || len(nodes) != 1 || summary.Unsupported != 1 {
		t.Fatalf("%+v %d %v", summary, len(nodes), err)
	}

	nodes = nil
	summary, err = codec.LoadSourceDetailed(context.Background(), "inline:Proxy: [{name: good, type: http, server: example.invalid, port: 8080}, {name: child, type: http, server: child.invalid, port: 8080, dialer-proxy: missing}]", nil, func(batch []codec.Node) error {
		nodes = append(nodes, batch...)
		return nil
	})
	if err != nil || len(nodes) != 1 || summary.Unsupported != 1 || nodes[0].Name != "good" {
		t.Fatalf("%+v %+v %v", summary, nodes, err)
	}

	//YAML anchors, flow mappings, spaced colons, and >2MiB rules section.
	for _, input := range []string{
		"inline:proxies:\n- &base {name: HK, type: http, server: example.invalid, port: 443}\n- {<<: *base, name: JP, server: other.invalid}\n",
		"inline:{proxies: [{name: flow, type: ss, cipher: aes-128-gcm, password: secret, server: fixture.invalid, port: 80}]}",
		"inline:\"proxies\": [{name: Tokyo, type: http, server: example.invalid, port: 443}]\n",
		"inline:data:text/plain,http%3A%2F%2Fexample.invalid%3A8080%23Tokyo",
	} {
		var emitted []codec.Node
		if _, err := codec.LoadSourceDetailed(context.Background(), input, nil, func(batch []codec.Node) error {
			emitted = append(emitted, batch...)
			return nil
		}); err != nil || len(emitted) == 0 {
			t.Fatalf("valid compatibility source rejected (%s): %v", input, err)
		}
	}

	var large strings.Builder
	large.WriteString("proxies : [{name: spaced, type: http, server: example.invalid, port: 80}]\nrules :\n")
	for large.Len() < (2<<20)+(256<<10) {
		large.WriteString("  - DOMAIN-SUFFIX,very-long-subdomain-entry.example.invalid,DIRECT\n")
	}
	if _, err := codec.LoadSourceDetailed(context.Background(), "inline:"+large.String(), nil, func([]codec.Node) error { return nil }); err != nil {
		t.Fatalf("large rules YAML failed: %v", err)
	}
	if _, err := codec.LoadSourceDetailed(context.Background(), "inline:"+large.String()+"  - [unterminated\n", nil, func([]codec.Node) error { return nil }); err == nil {
		t.Fatal("malformed >2MiB rules section accepted")
	}

	// Announcements preceding URI list.
	const uriLine = "ss://YWVzLTEyOC1nY206Zml4dHVyZS1zZWNyZXQ@example.invalid:443#Tokyo"
	bannerText := "订阅公告：请及时更新\n# comment\n\nunknown://example.invalid:99\n" + uriLine + "\n"
	for _, input := range []string{bannerText, base64.StdEncoding.EncodeToString([]byte(bannerText))} {
		var got []codec.Node
		sum, err := codec.LoadSourceDetailed(context.Background(), "inline:"+input, nil, func(b []codec.Node) error { got = append(got, b...); return nil })
		if err != nil || len(got) != 1 || sum.Unsupported != 2 {
			t.Fatalf("announcement handling failed: got=%d sum=%+v err=%v", len(got), sum, err)
		}
	}

	// Damaged YAML/JSON documents must never fall back to URI scanning.
	for _, badDoc := range []string{
		"proxies: [broken\nhttp://example.invalid:8080#good",
		"{\"outbounds\":[\nhttp://example.invalid:8080#good",
		"\ufeffproxies: [broken\nhttp://example.invalid:8080#good",
	} {
		emitted := false
		if _, err := codec.LoadSourceDetailed(context.Background(), "inline:"+badDoc, nil, func([]codec.Node) error { emitted = true; return nil }); err == nil || emitted {
			t.Fatal("damaged document emitted partial generation")
		}
	}

	// Empty source ErrNoNodes distinction.
	for _, tc := range []struct {
		input string
		empty bool
	}{
		{"proxies: []", true},
		{`{"outbounds":[]}`, true},
		{"proxies: [broken", false},
		{"proxy-providers: {bad: {url: /private}}", false},
	} {
		_, err := codec.LoadSourceRequestDetailed(context.Background(), codec.SourceRequest{Source: "inline:" + tc.input}, nil, func([]codec.Node) error { return nil })
		if err == nil || errors.Is(err, codec.ErrNoNodes) != tc.empty {
			t.Fatalf("%s: err=%v wantEmpty=%t", tc.input, err, tc.empty)
		}
	}
}

func TestOrderedSourceRequestsProxiesAndProviderIsolation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "custom-client ClashMeta" {
			t.Error("custom UA lost")
		}
		switch r.URL.Path {
		case "/broken":
			fmt.Fprint(w, "proxies: [{name: partial, type: ss, server: bad, port: 1, cipher: aes-128-gcm, password: p}]\nproxy-providers: {bad: {url: /missing}}")
		case "/missing":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/malformed":
			fmt.Fprint(w, "proxies: [broken")
		default:
			w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=3")
			fmt.Fprintf(w, "proxies: [{name: %s, type: ss, server: %s.example, port: 1, cipher: aes-128-gcm, password: p}]", r.URL.Path[1:], r.URL.Path[1:])
		}
	}))
	defer server.Close()

	var nodes []codec.Node
	summary, err := codec.LoadSourcesDetailed(context.Background(), []codec.SourceRequest{
		{Source: server.URL + "/one", UserAgent: "custom-client"},
		{Source: server.URL + "/broken", UserAgent: "custom-client"},
		{Source: server.URL + "/malformed", UserAgent: "custom-client"},
		{Source: server.URL + "/two", UserAgent: "custom-client"},
	}, nil, func(batch []codec.Node) error {
		nodes = append(nodes, batch...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 || nodes[0].Name != "one" || nodes[1].Name != "partial" || nodes[2].Name != "two" {
		t.Fatalf("order/rollback: %+v", nodes)
	}
	if !summary.Partial || len(summary.Sources) != 4 || summary.Sources[1].Error != "" || summary.Sources[2].Error == "" || summary.Sources[0].Headers.Get("Subscription-Userinfo") == "" {
		t.Fatalf("metadata: %+v", summary)
	}

	// Strict LoadSource rejects partial provider failure without emitting.
	strictSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "ClashMeta" {
			t.Errorf("unexpected default UA: %q", r.UserAgent())
		}
		if r.URL.Path == "/broken" {
			fmt.Fprint(w, "proxies: [{name: ok, type: http, server: ok.invalid, port: 80}]\nproxy-providers: {bad: {url: /missing}}")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer strictSrv.Close()
	emitted := false
	if err := codec.LoadSource(context.Background(), strictSrv.URL+"/broken", strictSrv.Client(), func([]codec.Node) error {
		emitted = true
		return nil
	}); err == nil || emitted {
		t.Fatalf("strict LoadSource allowed partial provider: emitted=%v err=%v", emitted, err)
	}

	// Local relative provider & cross-provider dialer-proxy.
	dir := t.TempDir()
	subDir := filepath.Join(dir, "configs", "nested")
	provDir := filepath.Join(dir, "configs", "providers")
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.MkdirAll(provDir, 0o755)
	_ = os.WriteFile(filepath.Join(provDir, "upstream.yaml"), []byte("proxies:\n  - {name: entry, type: http, server: one.invalid, port: 80}\n"), 0o644)
	rootFile := filepath.Join(subDir, "main.yaml")
	_ = os.WriteFile(rootFile, []byte("proxies:\n  - {name: exit, type: http, server: two.invalid, port: 80, dialer-proxy: entry}\nproxy-providers:\n  p1:\n    url: ../providers/upstream.yaml\n"), 0o644)

	var strictNodes, detailedNodes []codec.Node
	if err := codec.LoadSource(context.Background(), rootFile, nil, func(b []codec.Node) error { strictNodes = append(strictNodes, b...); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.LoadSourceDetailed(context.Background(), rootFile, nil, func(b []codec.Node) error { detailedNodes = append(detailedNodes, b...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(strictNodes) != 2 || !reflect.DeepEqual(strictNodes, detailedNodes) {
		t.Fatalf("strict vs detailed mismatch: %+v vs %+v", strictNodes, detailedNodes)
	}

	// SOCKS5 proxy support for subscription fetches.
	const payload = "proxies: [{name: proxied, type: http, server: node.example, port: 443}]"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	completed := make(chan error, 1)
	go func() {
		completed <- func() error {
			conn, err := listener.Accept()
			if err != nil {
				return err
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReader(conn)
			header := make([]byte, 2)
			_, _ = io.ReadFull(reader, header)
			methods := make([]byte, int(header[1]))
			_, _ = io.ReadFull(reader, methods)
			_, _ = conn.Write([]byte{5, 0})
			req := make([]byte, 5)
			_, _ = io.ReadFull(reader, req)
			target := make([]byte, int(req[4])+2)
			_, _ = io.ReadFull(reader, target)
			_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
			httpReq, err := http.ReadRequest(reader)
			if err != nil {
				return err
			}
			defer httpReq.Body.Close()
			_, err = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(payload), payload)
			return err
		}()
	}()
	sumProxy, err := codec.LoadSourcesDetailed(context.Background(), []codec.SourceRequest{{Source: "http://source.invalid/sub", Proxy: "socks5://" + listener.Addr().String(), UserAgent: "proxy-test"}}, nil, func([]codec.Node) error { return nil })
	if err != nil || sumProxy.Supported != 1 || <-completed != nil {
		t.Fatalf("SOCKS5 source proxy failed: sum=%+v err=%v", sumProxy, err)
	}

	// Legacy URI hints & parameter aliases.
	const vlessBase = "vless://00000000-0000-4000-8000-000000000001@example.invalid:443?security=tls&type=ws&path=%2Fws"
	var hinted []codec.Node
	hintSum, err := codec.LoadSourceDetailed(context.Background(), "inline:"+vlessBase+"&spx=%2F&ech=fixture-hint#fixture", nil, func(n []codec.Node) error { hinted = append(hinted, n...); return nil })
	canonDec, _ := codec.Decode([]byte(vlessBase + "#fixture"))
	aJSON, _ := json.Marshal(hinted)
	bJSON, _ := json.Marshal(canonDec.Nodes)
	if err != nil || !bytes.Equal(aJSON, bJSON) || len(hintSum.Diagnostics) != 1 || hintSum.Diagnostics[0].Code != "legacy_uri_metadata" {
		t.Fatalf("legacy URI hint mismatch: err=%v sum=%+v", err, hintSum)
	}
}
