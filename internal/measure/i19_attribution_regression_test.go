package measure

import (
	"context"
	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestI19HealthyProbeDoesNotAttributeLocalOrAmbiguousPathFailureToNode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"local_fd_exhaustion", syscall.EMFILE},
		{"local_network_unreachable", syscall.ENETUNREACH},
		{"ambiguous_path_timeout", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var controls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				controls.Add(1)
				w.Header().Set("X-Node-Quality-Probe", "2")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			plan := model.DefaultPlan()
			plan.Profile = "availability"
			plan.ProbeURL = server.URL
			plan.ProbeTimeout = time.Second
			node, err := codec.NewNode(map[string]any{"name": "i19", "type": "direct"}, model.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			dial := func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: tc.err}
			}
			control := probeControl(context.Background(), plan)
			report := evaluateNode(context.Background(), node, dial, plan, NewBudget(0), control)
			if controls.Load() != 2 {
				t.Fatalf("expected direct controls before and after failure; got %d", controls.Load())
			}
			if report.Status != model.Unknown {
				t.Errorf("local/ambiguous failure mislabeled node unavailable despite healthy direct controls: status=%s code=%s want=unknown", report.Status, report.FailureCode)
			}
			wantOrigin := model.FailureOriginUnknown
			if tc.name == "local_fd_exhaustion" || tc.name == "local_network_unreachable" {
				wantOrigin = model.FailureOriginEnvironment
			}
			if report.FailureOrigin != wantOrigin {
				t.Errorf("failure origin=%q want=%q", report.FailureOrigin, wantOrigin)
			}
			if report.Metrics.HealthAttempts != 1 || report.Metrics.HealthFailures != 1 {
				t.Errorf("invalid health fixture: %+v", report.Metrics)
			}
		})
	}
}
