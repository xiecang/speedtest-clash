package nodequality

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	msocks "github.com/metacubex/mihomo/transport/socks5"
	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/probe"
)

func auditSetup(t *testing.T, core engine.Core, handler http.Handler) (Plan, codec.Node, *httptest.Server) {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	p := DefaultPlan()
	p.Core, p.ProbeURL, p.ProbeTimeout = core, s.URL, time.Second
	p.ProbeTLSConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	p.DownloadBytes, p.SampleWindow, p.AI.Duration = 32<<10, 350*time.Millisecond, 600*time.Millisecond
	p.ExitIPURL = ""
	n, err := codec.NewNode(map[string]any{"type": "direct", "tag": "audit"}, codec.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	return p, n, s
}

func TestPreparePlanTracksFilterMutationsAndProfileValidation(t *testing.T) {
	p := DefaultPlan()
	p.Profile = "services"
	p.ProbeURL = ""
	p.DownloadBytes = 0
	p.Filter = `name == "alpha"`
	prepared, err := PreparePlan(p)
	if err != nil {
		t.Fatalf("services profile should not require probe_url or download_bytes: %v", err)
	}

	n1, err := codec.NewNode(map[string]any{"name": "alpha", "type": "tailscale", "server": "192.0.2.1", "port": 443}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := codec.NewNode(map[string]any{"name": "beta", "type": "tailscale", "server": "192.0.2.2", "port": 443}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}

	// Mutating Filter (including clearing or changing it) after PreparePlan must recompile!
	prepared.ServiceChecks = nil
	prepared.Filter = `name == "beta"`
	reports, err := Evaluate(context.Background(), []codec.Node{n1, n2}, prepared, NewBudget(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].NodeKey != n2.Key {
		t.Fatalf("expected mutated filter to select only beta, got %+v", reports)
	}
	if reports[0].Status != Unknown || reports[0].GeneralStatus != Unknown || reports[0].FailureCode != "unsupported_core" {
		t.Fatalf("unexpected capability failure report: %+v", reports[0])
	}

	// Clearing Filter after PreparePlan must select both nodes.
	prepared.Filter = ""
	reports, err = Evaluate(context.Background(), []codec.Node{n1, n2}, prepared, NewBudget(0))
	if err != nil || len(reports) != 2 {
		t.Fatalf("clearing prepared filter failed to recompile: count=%d err=%v", len(reports), err)
	}
}

func TestSelectionOnlyMeasuresRootsAndPreservesRevision(t *testing.T) {
	var calls atomic.Int64
	handler := probe.Handler(probe.Options{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := net.Dial("tcp", r.Host)
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
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(upstream, buffer); upstream.Close(); close(done) }()
		_, _ = io.Copy(connection, upstream)
		connection.Close()
		<-done
	}))
	defer proxy.Close()
	address, _ := url.Parse(proxy.URL)
	host, port, _ := net.SplitHostPort(address.Host)
	portNum, _ := strconv.Atoi(port)
	decoded, err := codec.Decode([]byte(fmt.Sprintf(`{"outbounds":[{"type":"http","tag":"entry","server":%q,"server_port":%d},{"type":"http","tag":"exit","server":%q,"server_port":%d,"username":"exit","detour":"entry"}]}`, host, portNum, host, portNum)))
	if err != nil {
		t.Fatal(err)
	}
	plan := DefaultPlan()
	plan.Profile = "availability"
	plan.ProbeURL = server.URL
	for _, core := range []engine.Core{engine.Mihomo, engine.SingBox} {
		plan.Core = core
		revision := PlanRevision(plan)
		plan.Filter = `name == "exit"`
		if PlanRevision(plan) != revision {
			t.Fatal("selection invalidated measurement revision")
		}
		reports, err := Evaluate(context.Background(), decoded.Nodes, plan, NewBudget(0))
		if err != nil || len(reports) != 1 || reports[0].Status != Pass {
			t.Fatalf("root or dependency lost: %v %v", reports, err)
		}
		previous := calls.Load()
		plan.Filter = `name == "absent"`
		reports, err = Evaluate(context.Background(), decoded.Nodes, plan, NewBudget(0))
		if err != nil || len(reports) != 0 || calls.Load() != previous {
			t.Fatalf("excluded target used network: %v %v", reports, err)
		}
	}
}

func TestGeneralEvidenceReuseRetainsDownloadAndIP(t *testing.T) {
	now := time.Now()
	n, _ := codec.NewNode(map[string]any{"type": "http", "server": "example.invalid", "port": 443}, codec.ClashYAML)
	p := DefaultPlan()
	p.Profile = "ai"
	p.ProbeURL = "https://probe.example"
	p.WorkerID = "worker"
	p.Environment = "env"
	general := p
	general.Profile = "general"
	r := newReport(n, general)
	r.Status = Pass
	r.GeneralMeasuredAt = now.Add(-time.Hour)
	r.FreshUntil = now.Add(time.Hour)
	r.Metrics = Metrics{DownloadBytes: 100, DownloadDuration: time.Second}
	r.IP = []IPResult{{Provider: "p1", IP: "8.8.8.8", Family: 4, Status: Pass, Country: "US"}}
	p.GeneralEvidence = map[string]Report{n.Key: r}

	got, ok := reusableGeneral(n, p)
	if !ok || !got.GeneralMeasuredAt.Equal(r.GeneralMeasuredAt) || len(got.IP) != 1 {
		t.Fatal("general evidence not retained")
	}
	p.Environment = "other"
	if _, ok = reusableGeneral(n, p); ok {
		t.Fatal("foreign environment reused")
	}
	p.Environment = "env"
	p.DownloadBytes++
	if _, ok = reusableGeneral(n, p); ok {
		t.Fatal("different measurement definition reused")
	}
}

func TestPartialHealthFailureCannotReuseOldGeneralTime(t *testing.T) {
	var count atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 2 {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("X-Node-Quality-Probe", "2")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	n, _ := codec.NewNode(map[string]any{"type": "http", "server": "example.invalid", "port": 443}, codec.ClashYAML)
	p := DefaultPlan()
	p.Profile = "ai"
	p.ProbeURL = server.URL
	p.WorkerID = "worker"
	p.Environment = "env"
	general := p
	general.Profile = "general"
	old := newReport(n, general)
	old.Status = Pass
	old.GeneralMeasuredAt = time.Now().Add(-time.Hour)
	old.FreshUntil = time.Now().Add(time.Hour)
	old.Metrics = Metrics{DownloadBytes: 100, DownloadDuration: time.Second}
	p.GeneralEvidence = map[string]Report{n.Key: old}

	var calls atomic.Int64
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if calls.Add(1) > 1 {
			return nil, errors.New("path failed")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	start := time.Now()
	r := evaluateNode(context.Background(), n, dial, p, NewBudget(0), true)
	if r.Status != Unknown || r.GeneralReused || r.GeneralStatus != Unknown || r.GeneralMeasuredAt.Before(start) {
		t.Fatalf("stale or rejected failure: %+v", r)
	}
}

func TestOriginAttributionAndProbeStageFailures(t *testing.T) {
	// 1. Local/ambiguous failure with healthy control stays Unknown.
	for _, tc := range []struct {
		name string
		err  error
		want FailureOrigin
	}{
		{"local_fd_exhaustion", syscall.EMFILE, FailureOriginEnvironment},
		{"local_network_unreachable", syscall.ENETUNREACH, FailureOriginEnvironment},
		{"ambiguous_path_timeout", context.DeadlineExceeded, FailureOriginUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Node-Quality-Probe", "2")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			plan := DefaultPlan()
			plan.Profile = "availability"
			plan.ProbeURL = server.URL
			plan.ProbeTimeout = time.Second
			node, _ := codec.NewNode(map[string]any{"name": "i19", "type": "direct"}, codec.ClashYAML)
			dial := func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: tc.err}
			}
			control := probeControl(context.Background(), plan)
			report := evaluateNode(context.Background(), node, dial, plan, NewBudget(0), control)
			if report.Status != Unknown || report.FailureOrigin != tc.want {
				t.Fatalf("status=%s origin=%s want Unknown/%s", report.Status, report.FailureOrigin, tc.want)
			}
		})
	}

	// 2. Native SOCKS5 auth required remains Fail (FailureOriginNode).
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var greeting [3]byte
			if _, e = io.ReadFull(conn, greeting[:]); e == nil && greeting == [3]byte{5, 1, 0} {
				_, _ = conn.Write([]byte{5, 2})
			}
			conn.Close()
		}
	}()
	defer func() { listener.Close(); <-done }()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	socksNode, _ := codec.NewNode(map[string]any{"name": "auth-required", "type": "socks5", "server": host, "port": port}, codec.ClashYAML)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	batch, err := engine.OpenBatch(ctx, []codec.Node{socksNode}, engine.Mihomo)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	dial, _ := batch.Dialer(socksNode.Key)
	if conn, dialErr := dial(ctx, "tcp", "example.invalid:443"); conn != nil || !errors.Is(dialErr, msocks.ErrAuth) {
		if conn != nil {
			conn.Close()
		}
		t.Fatalf("expected socks5.ErrAuth, got %v", dialErr)
	}
	controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node-Quality-Probe", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer controlSrv.Close()
	plan := DefaultPlan()
	plan.Profile = "availability"
	plan.ProbeURL = controlSrv.URL
	plan.ProbeTimeout = time.Second
	reports, err := Evaluate(ctx, []codec.Node{socksNode}, plan, NewBudget(0))
	if err != nil || len(reports) != 1 || reports[0].Status != Fail || reports[0].FailureOrigin != FailureOriginNode || reports[0].FailureCode != "node_auth_required" {
		t.Fatalf("unexpected SOCKS5 auth report: %+v err=%v", reports, err)
	}

	// 3. Environment stream failure preserves general Pass.
	var downloaded atomic.Bool
	envSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node-Quality-Probe", "2")
		switch r.URL.Path {
		case "/api/v2/probe/health":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v2/probe/download":
			_, _ = w.Write(make([]byte, 1024))
			downloaded.Store(true)
		default:
			http.NotFound(w, r)
		}
	}))
	defer envSrv.Close()
	directNode, _ := codec.NewNode(map[string]any{"name": "direct", "type": "direct"}, codec.ClashYAML)
	aiPlan := DefaultPlan()
	aiPlan.Profile, aiPlan.ProbeURL, aiPlan.ProbeTimeout = "ai", envSrv.URL, time.Second
	aiPlan.DownloadBytes, aiPlan.DownloadConcurrency = 1024, 1
	aiPlan.ExitIPURL = ""
	envReport := evaluateNode(context.Background(), directNode, func(ctx context.Context, network, address string) (net.Conn, error) {
		if downloaded.Load() {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.EMFILE}
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}, aiPlan, NewBudget(0), true)
	if envReport.Status != Unknown || envReport.FailureOrigin != FailureOriginEnvironment || !envReport.InfrastructureFailure || envReport.GeneralStatus != Pass {
		t.Fatalf("environment stream failure damaged general Pass: %+v", envReport)
	}
	if _, err := json.Marshal(envReport); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateDownloadHealthToleranceAndHTTPHandler(t *testing.T) {
	base := probe.Handler(probe.Options{})
	var mu struct {
		health, download int
	}
	lock := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lock <- struct{}{}
		switch r.URL.Path {
		case "/api/v2/probe/health":
			mu.health++
			call := mu.health
			<-lock
			if call == 2 {
				panic(http.ErrAbortHandler)
			}
			base.ServeHTTP(w, r)
			return
		case "/api/v2/probe/download":
			mu.download++
			call := mu.download
			<-lock
			if call >= 3 {
				w.Header().Set("X-Node-Quality-Probe", "2")
				w.Header().Set("Content-Length", "10240")
				_, _ = w.Write([]byte("abc"))
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			}
			base.ServeHTTP(w, r)
			return
		}
		<-lock
		base.ServeHTTP(w, r)
	}))
	defer srv.Close()

	node, _ := codec.NewNode(map[string]any{"type": "direct", "tag": "local"}, codec.SingBoxJSON)
	plan := DefaultPlan()
	plan.Core = engine.SingBox
	plan.ProbeURL = srv.URL
	plan.ExitIPURL = ""
	plan.DownloadConcurrency = 3
	plan.DownloadBytes = 30 * 1024
	reports, err := Evaluate(context.Background(), []codec.Node{node}, plan, NewBudget(0))
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports=%+v err=%v", reports, err)
	}
	r := reports[0]
	if r.Status != Pass || r.Metrics.HealthFailures != 1 || r.Metrics.DownloadEndReason != "partial_budget" {
		t.Fatalf("unexpected report: %+v", r)
	}

	// Verify ServerHandler /api/v2/convert with expr filter.
	api := ServerHandler(plan, NewBudget(0), probe.Options{Token: "secret"})
	yamlSrc := "proxies:\n- {name: entry, type: http, server: entry.invalid, port: 80}\n- {name: exit, type: http, server: exit.invalid, port: 80, dialer-proxy: entry}\n- {name: good, type: http, server: good.invalid, port: 80}\n"
	req := httptest.NewRequest(http.MethodPost, "/api/v2/convert?format=clash-yaml&filter="+url.QueryEscape(`name != "entry"`), bytes.NewBufferString(yamlSrc))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "exit.invalid") || !strings.Contains(rec.Body.String(), "good.invalid") {
		t.Fatalf("ServerHandler /api/v2/convert failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
