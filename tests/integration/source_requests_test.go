package integration_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	nq "github.com/xiecang/node-quality/v2"
)

func TestOrderedSourceRequestsAndRollback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "custom-client" {
			t.Error("custom UA lost")
		}
		switch r.URL.Path {
		case "/broken":
			fmt.Fprint(w, "proxies: [{name: partial, type: ss, server: bad, port: 1, cipher: aes-128-gcm, password: p}]\nproxy-providers: {bad: {url: /missing}}")
		case "/missing":
			w.WriteHeader(503)
		case "/malformed":
			fmt.Fprint(w, "proxies: [broken")
		default:
			w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=3")
			fmt.Fprintf(w, "proxies: [{name: %s, type: ss, server: %s.example, port: 1, cipher: aes-128-gcm, password: p}]", r.URL.Path[1:], r.URL.Path[1:])
		}
	}))
	defer server.Close()
	var nodes []nq.Node
	summary, err := nq.LoadSourcesDetailed(context.Background(), []nq.SourceRequest{{Source: server.URL + "/one", UserAgent: "custom-client"}, {Source: server.URL + "/broken", UserAgent: "custom-client"}, {Source: server.URL + "/malformed", UserAgent: "custom-client"}, {Source: server.URL + "/two", UserAgent: "custom-client"}}, nil, func(batch []nq.Node) error { nodes = append(nodes, batch...); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 || nodes[0].Name != "one" || nodes[1].Name != "partial" || nodes[2].Name != "two" {
		t.Fatalf("order/rollback: %+v", nodes)
	}
	if !summary.Partial || len(summary.Sources) != 4 || summary.Sources[1].Error != "" || summary.Sources[2].Error == "" || summary.Sources[0].Headers.Get("Subscription-Userinfo") == "" {
		t.Fatalf("metadata: %+v", summary)
	}
}

func TestSourceProxyAndEmpty(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.RequestURI, "http://source.invalid/") {
			t.Error("not a proxy request")
		}
		fmt.Fprint(w, "Proxy: [{name: proxied, type: masque, server: test, port: 443}]")
	}))
	defer proxy.Close()
	summary, err := nq.LoadSourcesDetailed(context.Background(), []nq.SourceRequest{{Source: "http://source.invalid/a", Proxy: proxy.URL}}, nil, func(nodes []nq.Node) error {
		if len(nodes) != 1 {
			t.Error("missing node")
		}
		return nil
	})
	if err != nil || summary.Supported != 1 {
		t.Fatalf("proxy: %+v %v", summary, err)
	}
	summary, err = nq.LoadSourcesDetailed(context.Background(), []nq.SourceRequest{{Source: "empty"}}, nil, func([]nq.Node) error { return nil })
	if err != nil || summary.Total != 0 {
		t.Fatalf("empty: %+v %v", summary, err)
	}
}

func TestMalformedEntriesStillConsumeGlobalBudget(t *testing.T) {
	for _, data := range []string{`{"outbounds":[` + strings.Repeat("null,", 50000) + `null]}`, "proxies: [" + strings.Repeat("null,", 50000) + "null]"} {
		emitted := 0
		_, err := nq.LoadSourcesDetailed(context.Background(), []nq.SourceRequest{{Source: "inline:proxies: [{name: good, type: http, server: good, port: 443}]"}, {Source: "inline:" + data}}, nil, func(batch []nq.Node) error { emitted += len(batch); return nil })
		if err == nil || emitted != 0 || !strings.Contains(err.Error(), "50000") {
			t.Fatal("budget bypass", emitted, err)
		}
	}
}

func TestHTTPSAndSOCKS5SourceProxies(t *testing.T) {
	const payload = "proxies: [{name: proxied, type: http, server: node.example, port: 443}]"
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "source.invalid" || r.UserAgent() != "proxy-test" {
			t.Error("HTTPS proxy request lost target or UA")
		}
		fmt.Fprint(w, payload)
	}))
	defer proxy.Close()
	check := func(proxyURL string, client *http.Client) {
		t.Helper()
		summary, err := nq.LoadSourcesDetailed(context.Background(), []nq.SourceRequest{{Source: "http://source.invalid/sub", Proxy: proxyURL, UserAgent: "proxy-test"}}, client, func([]nq.Node) error { return nil })
		if err != nil || summary.Supported != 1 {
			t.Fatal(proxyURL, summary.Supported, err)
		}
	}
	check(proxy.URL, proxy.Client())
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
			if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			reader := bufio.NewReader(conn)
			header := make([]byte, 2)
			if _, err = io.ReadFull(reader, header); err != nil {
				return err
			}
			methods := make([]byte, int(header[1]))
			if _, err = io.ReadFull(reader, methods); err != nil {
				return err
			}
			if _, err = conn.Write([]byte{5, 0}); err != nil {
				return err
			}
			request := make([]byte, 5)
			if _, err = io.ReadFull(reader, request); err != nil {
				return err
			}
			if request[0] != 5 || request[1] != 1 || request[3] != 3 {
				return fmt.Errorf("unexpected SOCKS request")
			}
			target := make([]byte, int(request[4])+2)
			if _, err = io.ReadFull(reader, target); err != nil {
				return err
			}
			if string(target[:len(target)-2]) != "source.invalid" {
				return fmt.Errorf("SOCKS target lost")
			}
			if _, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
				return err
			}
			httpRequest, err := http.ReadRequest(reader)
			if err != nil {
				return err
			}
			defer httpRequest.Body.Close()
			if httpRequest.UserAgent() != "proxy-test" {
				return fmt.Errorf("SOCKS request lost UA")
			}
			_, err = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(payload), payload)
			return err
		}()
	}()
	check("socks5://"+listener.Addr().String(), nil)
	if err = <-completed; err != nil {
		t.Fatal(err)
	}
}
