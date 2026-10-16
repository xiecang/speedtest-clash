package measure

import (
	"context"
	"errors"
	msocks "github.com/metacubex/mihomo/transport/socks5"
	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/engine"
	"github.com/xiecang/node-quality/v2/internal/model"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// The real native Mihomo SOCKS adapter receives a protocol response requiring
// credentials absent from this node configuration; no fabricated error flag.
func TestI19NativeNodeAuthenticationRequiredRemainsFail(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var replies atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			if e = conn.SetDeadline(time.Now().Add(time.Second)); e != nil {
				conn.Close()
				continue
			}
			var greeting [3]byte
			if _, e = io.ReadFull(conn, greeting[:]); e == nil && greeting == [3]byte{5, 1, 0} {
				if _, e = conn.Write([]byte{5, 2}); e == nil {
					replies.Add(1)
				}
			}
			conn.Close()
		}
	}()
	defer func() { listener.Close(); <-done }()
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	node, err := codec.NewNode(map[string]any{"name": "auth-required", "type": "socks5", "server": host, "port": port}, model.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	batch, err := engine.OpenBatch(ctx, []model.Node{node}, model.Mihomo)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	dial, err := batch.Dialer(node.Key)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dial(ctx, "tcp", "example.invalid:443")
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, msocks.ErrAuth) {
		t.Fatalf("native adapter lost structured authentication-required sentinel: %T", err)
	}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node-Quality-Probe", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer control.Close()
	plan := model.DefaultPlan()
	plan.Profile = "availability"
	plan.ProbeURL = control.URL
	plan.ProbeTimeout = time.Second
	reports, err := Evaluate(ctx, []model.Node{node}, plan, NewBudget(0))
	if err != nil || len(reports) != 1 {
		t.Fatalf("evaluate error=%v count=%d", err, len(reports))
	}
	if reports[0].Status != model.Fail {
		t.Errorf("proven native node configuration failure was suppressed: status=%s want=fail", reports[0].Status)
	}
	if reports[0].FailureOrigin != model.FailureOriginNode || reports[0].FailureCode != "node_auth_required" {
		t.Errorf("native auth failure attribution=%q/%q want node/node_auth_required", reports[0].FailureOrigin, reports[0].FailureCode)
	}
	if replies.Load() != 2 {
		t.Errorf("expected actual peer handshake for raw adapter and Evaluate; got=%d", replies.Load())
	}
	t.Logf("real native handshake sentinel verified; status=%s; peer_replies=%d", reports[0].Status, replies.Load())
}
