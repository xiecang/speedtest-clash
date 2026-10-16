package measure

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestI19EnvironmentStreamFailureKeepsGeneralPass(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"resource", syscall.EMFILE, "environment_resource_exhausted"},
		{"route", syscall.ENETUNREACH, "environment_route_unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var downloaded atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			defer server.Close()
			node, err := codec.NewNode(map[string]any{"name": "i19-environment-stream", "type": "direct"}, model.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			plan := model.DefaultPlan()
			plan.Profile, plan.ProbeURL, plan.ProbeTimeout = "ai", server.URL, time.Second
			plan.DownloadBytes, plan.DownloadConcurrency = 1024, 1
			dial := func(ctx context.Context, network, address string) (net.Conn, error) {
				if downloaded.Load() {
					return nil, &net.OpError{Op: "dial", Net: network, Err: tc.err}
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			report := evaluateNode(context.Background(), node, dial, plan, NewBudget(0), true)
			if report.Status != model.Unknown || report.FailureOrigin != model.FailureOriginEnvironment || !report.InfrastructureFailure || report.FailureCode != tc.code || report.GeneralStatus != model.Pass || report.GeneralFailureOrigin != "" {
				t.Fatalf("environment failure lost general PASS or was not marked infrastructure: %+v", report)
			}
			if len(report.Streams) != 3 {
				t.Fatalf("stream failures=%d want 3", len(report.Streams))
			}
			if _, err = json.Marshal(report); err != nil {
				t.Fatalf("wire report encoding: %v", err)
			}
		})
	}
}

func TestI19ProbeStageFailuresRemainUnknown(t *testing.T) {
	for _, profile := range []string{"partial_health", "download", "ai_stream"} {
		t.Run(profile, func(t *testing.T) {
			var health atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Node-Quality-Probe", "2")
				switch r.URL.Path {
				case "/api/v2/probe/health":
					call := health.Add(1)
					if profile == "partial_health" && call >= 3 && call <= 5 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case "/api/v2/probe/download":
					if profile == "download" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write(make([]byte, 1024))
				case "/api/v2/probe/sse", "/api/v2/probe/ws":
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			node, err := codec.NewNode(map[string]any{"name": "i19-stage", "type": "direct"}, model.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			plan := model.DefaultPlan()
			plan.Profile = "general"
			if profile == "ai_stream" {
				plan.Profile = "ai"
			}
			plan.ProbeURL = server.URL
			plan.ProbeTimeout = time.Second
			plan.DownloadBytes = 1024
			plan.DownloadConcurrency = 1
			dial := (&net.Dialer{}).DialContext
			report := evaluateNode(context.Background(), node, dial, plan, NewBudget(0), true)
			if report.Status != model.Unknown || report.FailureOrigin != model.FailureOriginProbe || !report.InfrastructureFailure {
				t.Fatalf("probe failure became a node result: status=%s origin=%s infrastructure=%v code=%s", report.Status, report.FailureOrigin, report.InfrastructureFailure, report.FailureCode)
			}
			if profile == "ai_stream" && (report.GeneralStatus != model.Pass || report.GeneralFailureOrigin != "") {
				t.Fatalf("AI-only probe failure damaged successful general evidence: status=%s general=%s origin=%s", report.Status, report.GeneralStatus, report.GeneralFailureOrigin)
			}
			if profile == "partial_health" && report.Metrics.HealthFailures == 0 {
				t.Fatal(fmt.Sprintf("partial health fixture did not fail: %+v", report.Metrics))
			}
		})
	}
}
