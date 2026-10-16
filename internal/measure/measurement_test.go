// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/engine"
	"github.com/xiecang/node-quality/v2/internal/model"
	"github.com/xiecang/node-quality/v2/internal/probe"
)

func TestShortReadAndPartialErrorAreFailures(t *testing.T) {
	count, _, err := copyDownload(context.Background(), bytes.NewBufferString("abc"), 10, NewBudget(0))
	if count != 3 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("bytes=%d error=%v", count, err)
	}
	count, _, err = copyDownload(context.Background(), bytes.NewBufferString("abc"), 3, NewBudget(0))
	if count != 3 || err != nil {
		t.Fatalf("exact quota failed: bytes=%d error=%v", count, err)
	}
}

func TestNativeSSEAndWebSocketCompleteness(t *testing.T) {
	probe := httptest.NewUnstartedServer(probe.ProbeHandler(probe.ProbeOptions{Token: "test", FrameInterval: 10 * time.Millisecond}))
	probe.EnableHTTP2 = true
	probe.StartTLS()
	defer probe.Close()
	pool := x509.NewCertPool()
	pool.AddCert(probe.Certificate())
	plan := model.DefaultPlan()
	plan.Profile = "ai"
	plan.ProbeURL = probe.URL
	plan.ProbeToken = "test"
	plan.AI.Duration = 1200 * time.Millisecond
	plan.ProbeTLSConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	node, err := codec.NewNode(map[string]any{"type": "direct", "tag": "local"}, model.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := engine.OpenBatch(context.Background(), []model.Node{node}, model.SingBox)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	dial, err := batch.Dialer(node.Key)
	if err != nil {
		t.Fatal(err)
	}
	results := measureStreams(context.Background(), dial, plan)
	for _, result := range results {
		if result.Status != model.Pass || result.Frames == 0 {
			t.Fatalf("stream failed: %+v", result)
		}
	}
}

func TestProbeRejectsBadBudgetsAndAuthentication(t *testing.T) {
	handler := probe.ProbeHandler(probe.ProbeOptions{Token: "test", MaxDownloadBytes: 64})
	for _, test := range []struct {
		path, token string
		status      int
	}{{"/api/v2/probe/download?bytes=-1", "test", 400}, {"/api/v2/probe/download?bytes=65", "test", 400}, {"/api/v2/probe/health", "wrong", 401}, {"/api/v2/probe/health", "test", 204}} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s: got %d expected %d", test.path, response.Code, test.status)
		}
	}
}

func TestEvaluateRunsDownloadAfterLiveness(t *testing.T) {
	probe := httptest.NewServer(probe.ProbeHandler(probe.ProbeOptions{}))
	defer probe.Close()
	node, err := codec.NewNode(map[string]any{"type": "direct", "tag": "local"}, model.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	plan := model.DefaultPlan()
	plan.Core = model.SingBox
	plan.ProbeURL = probe.URL
	plan.DownloadBytes = 4097
	reports, err := Evaluate(context.Background(), []model.Node{node}, plan, NewBudget(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Status != model.Pass || reports[0].Metrics.DownloadBytes != 4097 {
		t.Fatalf("reports=%+v", reports)
	}
}

func TestNativeMihomoHTTPProxy(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("mihomo")) }))
	defer destination.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", 405)
			return
		}
		upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, "dial failed", 502)
			return
		}
		defer upstream.Close()
		connection, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		buffer.Flush()
		done := make(chan struct{})
		go func() { io.Copy(upstream, buffer); upstream.Close(); close(done) }()
		io.Copy(connection, upstream)
		connection.Close()
		<-done
	}))
	defer proxy.Close()
	address := proxy.Listener.Addr().(*net.TCPAddr)
	node, err := codec.NewNode(map[string]any{"name": "local", "type": "http", "server": "127.0.0.1", "port": address.Port}, model.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := engine.OpenBatch(context.Background(), []model.Node{node}, model.Mihomo)
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
	client := http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get(destination.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil || string(content) != "mihomo" {
		t.Fatalf("content=%q error=%v", content, err)
	}
}

func TestRateLimitedSampleIsNormalAndCancelledReadsStayWithinBudget(t *testing.T) {
	probe := httptest.NewServer(probe.ProbeHandler(probe.ProbeOptions{}))
	defer probe.Close()
	p := model.DefaultPlan()
	p.ProbeURL = probe.URL
	p.DownloadBytes = 1 << 20
	p.DownloadConcurrency = 1
	p.SampleWindow = 100 * time.Millisecond
	budget := NewBudget(32 << 10)
	m, err := measureDownload(context.Background(), probe.Client(), p, budget)
	if err != nil || m.DownloadBytes <= 0 || !m.RateLimited {
		t.Fatalf("censored sample misclassified: %+v %v", m, err)
	}
	if float64(m.DownloadBytes) > float64(32<<10)+(32<<10)*m.DownloadDuration.Seconds()+1 {
		t.Fatalf("read exceeded budget: %+v", m)
	}
}
