// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/engine"
	"github.com/xiecang/node-quality/v2/internal/model"
	"github.com/xiecang/node-quality/v2/internal/probe"
)

func auditSetup(t *testing.T, core model.Core, handler http.Handler) (model.Plan, model.Node, *httptest.Server) {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	s.EnableHTTP2 = true
	s.StartTLS()
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	p := model.DefaultPlan()
	p.Core, p.ProbeURL, p.ProbeTimeout = core, s.URL, time.Second
	p.ProbeTLSConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	p.DownloadBytes, p.SampleWindow, p.AI.Duration = 32<<10, 350*time.Millisecond, 600*time.Millisecond
	p.ExitIPURL = ""
	n, err := codec.NewNode(map[string]any{"type": "direct", "tag": "audit"}, model.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	return p, n, s
}

func auditRecord(t *testing.T, name string, data any) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("AUDIT %s %s", name, b)
	if dir := os.Getenv("QUALITY_AUDIT_RESULTS"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, strings.ReplaceAll(name, "/", "-")+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditMatrixGeneral(t *testing.T) {
	for _, core := range []model.Core{model.Mihomo, model.SingBox} {
		for _, kind := range []string{"normal", "health_delay", "header_spread", "short_body", "reset", "stall_after_byte", "bad_content_same_length", "health_down"} {
			t.Run(string(core)+"/"+kind, func(t *testing.T) {
				base := probe.ProbeHandler(probe.ProbeOptions{FrameInterval: 10 * time.Millisecond})
				var downloads atomic.Int64
				h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("X-Node-Quality-Probe", "2")
					if strings.HasSuffix(r.URL.Path, "/health") && kind == "health_down" {
						w.WriteHeader(503)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/health") && kind == "health_delay" {
						time.Sleep(25 * time.Millisecond)
					}
					if strings.HasSuffix(r.URL.Path, "/download") {
						switch kind {
						case "header_spread":
							time.Sleep(time.Duration(downloads.Add(1)) * 20 * time.Millisecond)
						case "short_body":
							w.Header().Set("Content-Length", "100000")
							w.Write([]byte("abc"))
							return
						case "reset":
							w.Header().Set("Content-Length", "100000")
							w.Write([]byte("abc"))
							w.(http.Flusher).Flush()
							panic(http.ErrAbortHandler)
						case "stall_after_byte":
							w.WriteHeader(200)
							w.Write([]byte("x"))
							w.(http.Flusher).Flush()
							<-r.Context().Done()
							return
						case "bad_content_same_length":
							var n int
							fmt.Sscan(r.URL.Query().Get("bytes"), &n)
							w.Header().Set("Content-Length", fmt.Sprint(n))
							w.Write([]byte(strings.Repeat("X", n)))
							return
						}
					}
					base.ServeHTTP(w, r)
				})
				p, n, _ := auditSetup(t, core, h)
				reports, err := Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0))
				if err != nil || len(reports) != 1 {
					t.Fatalf("reports=%v err=%v", reports, err)
				}
				r := reports[0]
				auditRecord(t, string(core)+"/"+kind, r)
				expected := model.Pass
				if kind == "short_body" || kind == "reset" {
					expected = model.Unknown
				}
				if kind == "health_down" {
					expected = model.Unknown
				}
				if r.Status != expected {
					t.Errorf("expected %s got %s (%s)", expected, r.Status, r.FailureCode)
				}
				if kind == "health_delay" && r.Metrics.ColdHealthMedian < 25*time.Millisecond {
					t.Errorf("known 25ms delay missing: %v", r.Metrics.ColdHealthMedian)
				}
				if r.Status == model.Pass && r.Metrics.HealthFailures != 0 {
					t.Error("pass had failures")
				}
			})
		}
	}
}

func TestAuditReproProbeCapacityIsInfrastructure(t *testing.T) {
	for _, core := range []model.Core{model.Mihomo, model.SingBox} {
		t.Run(string(core), func(t *testing.T) {
			p, n, s := auditSetup(t, core, probe.ProbeHandler(probe.ProbeOptions{MaxSessions: 1, FrameInterval: 10 * time.Millisecond}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/api/v2/probe/sse?duration_ms=3000", nil)
			held, err := s.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Body.Close()
			reports, err := Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0))
			if err != nil {
				t.Fatal(err)
			}
			r := reports[0]
			auditRecord(t, string(core)+"/probe_capacity", r)
			if r.Status != model.Unknown {
				t.Errorf("probe deliberately full: expected UNKNOWN infrastructure, got %s infrastructure=%v code=%s", r.Status, r.InfrastructureFailure, r.FailureCode)
			}
		})
	}
}

func TestAuditMatrixStreams(t *testing.T) {
	for _, core := range []model.Core{model.Mihomo, model.SingBox} {
		for _, kind := range []string{"normal", "missing_done", "sequence_gap", "buffered_40ms", "h2_disabled", "busy", "idle_drop", "active_stall"} {
			t.Run(string(core)+"/"+kind, func(t *testing.T) {
				base := probe.ProbeHandler(probe.ProbeOptions{FrameInterval: 10 * time.Millisecond})
				h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/sse") && (kind == "missing_done" || kind == "sequence_gap" || kind == "busy" || kind == "idle_drop" || kind == "active_stall") {
						w.Header().Set("Content-Type", "text/event-stream")
						if kind == "busy" {
							w.WriteHeader(503)
							return
						}
						seq := int64(1)
						if kind == "sequence_gap" {
							seq = 2
						}
						fmt.Fprintf(w, "data: {\"kind\":\"data\",\"sequence\":%d}\n\n", seq)
						w.(http.Flusher).Flush()
						if kind == "idle_drop" {
							fmt.Fprint(w, "data: {\"kind\":\"idle\",\"sequence\":1,\"idle_ms\":200}\n\n")
							w.(http.Flusher).Flush()
						}
						if kind == "active_stall" {
							<-r.Context().Done()
						}
						return
					}
					if strings.HasSuffix(r.URL.Path, "/sse") && kind == "buffered_40ms" {
						// Preserve the server's timeline but deliver output in 40ms bursts.
						w.Header().Set("Content-Type", "text/event-stream")
						buffered := &burstWriter{ResponseWriter: w, last: time.Now()}
						base.ServeHTTP(buffered, r)
						buffered.flush()
						return
					}
					base.ServeHTTP(w, r)
				})
				p, n, s := auditSetup(t, core, h)
				if kind == "h2_disabled" {
					s.Close()
					s = httptest.NewUnstartedServer(h)
					s.EnableHTTP2 = false
					s.StartTLS()
					defer s.Close()
					p.ProbeURL = s.URL
					roots := x509.NewCertPool()
					roots.AddCert(s.Certificate())
					p.ProbeTLSConfig.RootCAs = roots
				}
				batch, err := engine.OpenBatch(context.Background(), []model.Node{n}, core)
				if err != nil {
					t.Fatal(err)
				}
				defer batch.Close()
				dial, err := batch.Dialer(n.Key)
				if err != nil {
					t.Fatal(err)
				}
				results := measureStreams(context.Background(), dial, p)
				auditRecord(t, string(core)+"/streams-"+kind, results)
				for _, r := range results {
					expected := model.Pass
					if r.Protocol != "websocket" && (kind == "missing_done" || kind == "sequence_gap" || kind == "idle_drop" || kind == "active_stall") {
						expected = model.Unknown
					}
					if kind == "active_stall" && r.Protocol != "websocket" && (r.Duration < 9*time.Second || r.Duration > 12*time.Second || r.MaxActiveSilence < 9*time.Second) {
						t.Errorf("active stall must use actual 10s no-progress deadline: %+v", r)
					}
					if r.Protocol != "websocket" && kind == "busy" {
						expected = model.Unknown
					}
					if r.Protocol == "h2" && kind == "h2_disabled" {
						expected = model.Unknown
					}
					if r.Status != expected {
						t.Errorf("%s expected %s got %+v", r.Protocol, expected, r)
					}
				}
			})
		}
	}
}

func TestAuditMetricsRateAccounting(t *testing.T) {
	p, n, _ := auditSetup(t, model.SingBox, probe.ProbeHandler(probe.ProbeOptions{}))
	p.DownloadBytes = 1 << 20
	p.SampleWindow = 200 * time.Millisecond
	reports, err := Evaluate(context.Background(), []model.Node{n}, p, NewBudget(64<<10))
	if err != nil {
		t.Fatal(err)
	}
	r := reports[0]
	auditRecord(t, "rate-accounting", r)
	if r.Status != model.Pass || !r.Metrics.RateLimited {
		t.Fatalf("unexpected report %+v", r)
	}
	t.Logf("sum_wait/wall=%.2f observed_budget_ratio=%.2f", float64(r.Metrics.AggregateLimiterWait)/float64(r.Metrics.DownloadDuration), r.Metrics.GoodputBytesPerSecond/(64<<10))
}

func TestAuditReproWireGuardURIReservedRoundTrip(t *testing.T) {
	in := "wg://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa%3D@127.0.0.1:51820?publickey=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb%3D&address=10.0.0.2%2F32&reserved=1,2,3#demo"
	decoded, err := codec.Decode([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := codec.Encode(decoded.Nodes, model.URI)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(string(out))
	if err != nil {
		t.Fatal(err)
	}
	auditRecord(t, "wireguard-reserved", map[string]any{"input_reserved": "1,2,3", "output_reserved": u.Query().Get("reserved")})
	if u.Query().Get("reserved") != "1,2,3" {
		t.Errorf("reserved lost: got %q", u.Query().Get("reserved"))
	}
}

func TestAuditReproURIALPNRoundTrip(t *testing.T) {
	in := "trojan://test@127.0.0.1:443?sni=example.test&alpn=h2,http%2F1.1#demo"
	decoded, err := codec.Decode([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := codec.Encode(decoded.Nodes, model.URI)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(string(out))
	auditRecord(t, "uri-alpn", map[string]any{"input_alpn": "h2,http/1.1", "output_alpn": u.Query().Get("alpn"), "decoded_type": fmt.Sprintf("%T", decoded.Nodes[0].Config["alpn"])})
	if u.Query().Get("alpn") != "h2,http/1.1" {
		t.Errorf("ALPN lost: got %q", u.Query().Get("alpn"))
	}
}

func TestAuditReproSharedRateBoundAcrossCancelledSamples(t *testing.T) {
	p, _, s := auditSetup(t, model.SingBox, probe.ProbeHandler(probe.ProbeOptions{}))
	p.DownloadBytes = 1 << 20
	p.SampleWindow = 200 * time.Millisecond
	budget := NewBudget(64 << 10)
	started := time.Now()
	var total int64
	for i := 0; i < 4; i++ {
		m, err := measureDownload(context.Background(), s.Client(), p, budget)
		if err != nil {
			t.Fatal(err)
		}
		total += m.DownloadBytes
	}
	elapsed := time.Since(started)
	upper := float64(32<<10) + elapsed.Seconds()*float64(64<<10)
	auditRecord(t, "shared-rate-cancel", map[string]any{"actual_bytes": total, "elapsed_seconds": elapsed.Seconds(), "allowed_rate_plus_burst_bytes": upper, "limiter_tokens": budget.limiter.Tokens()})
	if float64(total) > upper+4096 {
		t.Errorf("actual %d exceeds rate plus burst %.0f by %.2fx", total, upper, float64(total)/upper)
	}
}
