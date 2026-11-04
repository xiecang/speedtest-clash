package measure

import (
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
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestStreamConnectUsesProbeTimeout(t *testing.T) {
	for _, protocol := range []string{"h1", "h2", "websocket"} {
		t.Run(protocol, func(t *testing.T) {
			plan := model.DefaultPlan()
			plan.ProbeURL = "https://probe.invalid"
			plan.ProbeTimeout = 30 * time.Millisecond
			plan.AI.Duration = time.Second
			dial := func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			result := measureStream(ctx, dial, plan, protocol)
			if result.Duration > 150*time.Millisecond {
				t.Fatalf("connection ignored probe timeout %s: %+v", plan.ProbeTimeout, result)
			}
			if result.Status != model.Unknown || result.FailureOrigin != model.FailureOriginUnknown || result.FailureStage != "connect" || result.FailureReason != "timeout" {
				t.Fatalf("incorrect connect failure attribution: %+v", result)
			}
		})
	}
}

func TestStreamFailureDiagnosticsPreserveStrictCompletion(t *testing.T) {
	for _, protocol := range []string{"h1", "h2", "websocket"} {
		for _, kind := range []string{"active", "idle", "resumed", "early_done"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var send func(model.StreamFrame)
					if protocol == "websocket" {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer conn.Close(websocket.StatusNormalClosure, "")
						send = func(f model.StreamFrame) {
							b, _ := json.Marshal(f)
							_ = conn.Write(r.Context(), websocket.MessageText, b)
						}
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						send = func(f model.StreamFrame) {
							b, _ := json.Marshal(f)
							_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
							_ = http.NewResponseController(w).Flush()
						}
					}
					send(model.StreamFrame{Kind: "data", Sequence: 1})
					if kind != "active" {
						send(model.StreamFrame{Kind: "idle", Sequence: 1, IdleMilliseconds: 50})
					}
					if kind == "resumed" || kind == "early_done" {
						send(model.StreamFrame{Kind: "data", Sequence: 2})
					}
					if kind == "early_done" {
						send(model.StreamFrame{Kind: "done", Sequence: 2})
					}
				}))
				server.EnableHTTP2 = true
				server.StartTLS()
				defer server.Close()
				roots := x509.NewCertPool()
				roots.AddCert(server.Certificate())
				plan := model.DefaultPlan()
				plan.ProbeURL = server.URL
				plan.AI.Duration = time.Second
				plan.ProbeTLSConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
				result := measureStream(context.Background(), (&net.Dialer{}).DialContext, plan, protocol)
				stage, code := kind, "stream_incomplete"
				if kind == "early_done" {
					stage, code = "completion", "invalid_end_marker"
				}
				if result.Status != model.Unknown || result.FailureCode != code || result.FailureStage != stage {
					t.Fatalf("unexpected result: %+v", result)
				}
				if kind != "early_done" && result.FailureReason != "eof" && result.FailureReason != "websocket_close" {
					t.Fatalf("missing bounded failure reason: %+v", result)
				}
			})
		}
	}
}

func TestStreamFailureReasonNeverExportsRawErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"}, {context.Canceled, "cancelled"},
		{io.ErrUnexpectedEOF, "eof"}, {syscall.ECONNRESET, "connection_reset"},
		{&json.SyntaxError{}, "invalid_frame"}, {errors.New("password=fixture-secret"), "read_error"},
	} {
		if got := streamFailureReason(tc.err); got != tc.want {
			t.Fatalf("reason=%s want=%s", got, tc.want)
		}
	}
}
