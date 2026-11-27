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
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/probe"
)

func TestStreamsCompletenessAndDiagnostics(t *testing.T) {
	srv := httptest.NewUnstartedServer(probe.Handler(probe.Options{Token: "test", FrameInterval: 10 * time.Millisecond}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	plan := DefaultPlan()
	plan.Profile = "ai"
	plan.ProbeURL = srv.URL
	plan.ProbeToken = "test"
	plan.AI.Duration = 400 * time.Millisecond
	plan.ProbeTLSConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	node, err := codec.NewNode(map[string]any{"type": "direct", "tag": "local"}, codec.SingBoxJSON)
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
	results := measureStreams(context.Background(), dial, plan)
	for _, result := range results {
		if result.Status != Pass || result.Frames == 0 {
			t.Fatalf("stream failed: %+v", result)
		}
	}

	// Stream connect timeout & stage diagnostics.
	for _, protocol := range []string{"h1", "h2", "websocket"} {
		p := DefaultPlan()
		p.ProbeURL = "https://probe.invalid"
		p.ProbeTimeout = 30 * time.Millisecond
		p.AI.Duration = time.Second
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		res := measureStream(ctx, func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }, p, protocol)
		cancel()
		if res.Duration > 150*time.Millisecond || res.Status != Unknown || res.FailureStage != "connect" || res.FailureReason != "timeout" {
			t.Fatalf("%s connect timeout attribution failed: %+v", protocol, res)
		}
	}

	for _, tc := range []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "cancelled"},
		{io.ErrUnexpectedEOF, "eof"},
		{syscall.ECONNRESET, "connection_reset"},
		{&json.SyntaxError{}, "invalid_frame"},
		{errors.New("password=fixture-secret"), "read_error"},
	} {
		if got := streamFailureReason(tc.err); got != tc.want {
			t.Fatalf("reason=%s want=%s", got, tc.want)
		}
	}

	// Early done / incomplete stage check on WebSocket.
	earlySrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node-Quality-Probe", "2")
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for _, f := range []streamFrame{
			{Kind: "data", Sequence: 1},
			{Kind: "idle", Sequence: 1, IdleMilliseconds: 50},
			{Kind: "data", Sequence: 2},
			{Kind: "done", Sequence: 2},
		} {
			b, _ := json.Marshal(f)
			_ = conn.Write(r.Context(), websocket.MessageText, b)
		}
	}))
	earlySrv.EnableHTTP2 = true
	earlySrv.StartTLS()
	defer earlySrv.Close()
	earlyPool := x509.NewCertPool()
	earlyPool.AddCert(earlySrv.Certificate())
	earlyPlan := DefaultPlan()
	earlyPlan.ProbeURL = earlySrv.URL
	earlyPlan.AI.Duration = time.Second
	earlyPlan.ProbeTLSConfig = &tls.Config{RootCAs: earlyPool, MinVersion: tls.VersionTLS12}
	earlyRes := measureStream(context.Background(), (&net.Dialer{}).DialContext, earlyPlan, "websocket")
	if earlyRes.Status != Unknown || earlyRes.FailureCode != "invalid_end_marker" || earlyRes.FailureStage != "completion" {
		t.Fatalf("unexpected early done result: %+v", earlyRes)
	}
}

type oneByteReader struct{ remaining int }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	r.remaining--
	p[0] = 0
	return 1, nil
}

func TestBudgetRateLimitingConcurrencyAndPartialReads(t *testing.T) {
	count, _, err := copyDownload(context.Background(), bytes.NewBufferString("abc"), 10, NewBudget(0))
	if count != 3 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("bytes=%d error=%v", count, err)
	}

	budget := NewBudget(64 << 10)
	started := time.Now()
	count, _, err = copyDownload(context.Background(), &oneByteReader{remaining: 4096}, 4096, budget)
	if err != nil || count != 4096 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("partial reads consumed full chunk permits: count=%d err=%v", count, err)
	}

	gate := make(chan struct{})
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Node-Quality-Probe", "2")
		requests.Add(1)
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		_, _ = w.Write(make([]byte, n))
	}))
	defer server.Close()

	multiBudget, err := NewBudgetWithConcurrency(100<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	plan := DefaultPlan()
	plan.ProbeURL = server.URL
	plan.DownloadBytes = 40 << 10
	plan.SampleWindow = 5 * time.Second
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := measureDownload(context.Background(), server.Client(), plan, multiBudget)
			results <- err
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for requests.Load() < 40 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if requests.Load() != 40 || multiBudget.Stats().ActiveNodes != 8 {
		close(gate)
		t.Fatalf("requests=%d stats=%+v", requests.Load(), multiBudget.Stats())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = measureDownload(ctx, server.Client(), plan, multiBudget); err == nil {
		close(gate)
		t.Fatal("ninth node did not wait")
	}
	close(gate)
	for i := 0; i < 8; i++ {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	if _, err = measureDownload(context.Background(), server.Client(), plan, multiBudget); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int{0, 129} {
		if _, err := NewBudgetWithConcurrency(100<<20, bad); err == nil {
			t.Fatalf("expected invalid concurrency %d to fail", bad)
		}
	}

	var wg sync.WaitGroup
	rateBudget, _ := NewBudgetWithConcurrency(100<<20, 8)
	payload := make([]byte, 8<<20)
	rateStart := time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := copyDownload(context.Background(), bytes.NewReader(payload), int64(len(payload)), rateBudget)
			if e != nil && e != io.EOF {
				t.Error(fmt.Errorf("parallel read error: %w", e))
			}
		}()
	}
	wg.Wait()
	if time.Since(rateStart) < 600*time.Millisecond || rateBudget.Stats().DownloadedBytes != 64<<20 {
		t.Fatalf("shared rate elapsed=%s stats=%+v", time.Since(rateStart), rateBudget.Stats())
	}
}
