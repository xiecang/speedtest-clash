// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
	"github.com/xiecang/node-quality/v2/internal/probe"
)

func TestHealthConnectionSamplesAndHeaderMedian(t *testing.T) {
	var downloads atomic.Int64
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/probe/download" {
			time.Sleep(time.Duration(downloads.Add(1)) * 20 * time.Millisecond)
		}
		probe.ProbeHandler(probe.ProbeOptions{}).ServeHTTP(w, r)
	}))
	defer probe.Close()
	p := model.DefaultPlan()
	p.ProbeURL = probe.URL
	p.ExitIPURL = ""
	p.DownloadBytes = 5 * 1024
	n, err := codec.NewNode(map[string]any{"type": "direct", "name": "metric"}, model.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0))
	if err != nil {
		t.Fatal(err)
	}
	r := reports[0]
	m := r.Metrics
	if r.Status != model.Pass || m.HealthAttempts != 5 || m.HealthFailures != 0 || m.ColdHealthSamples != 1 || m.ReusedHealthSamples != 4 || m.DownloadHeaderSamples != 5 {
		t.Fatalf("unexpected samples: %+v", r)
	}
	// Scheduling and race instrumentation add real header latency; they cannot
	// make the third of the five delayed responses faster than 50ms.
	if m.DownloadHeadersMedian < 50*time.Millisecond {
		t.Fatalf("headers median must use all five responses, got %s", m.DownloadHeadersMedian)
	}
	if m.RateLimited || m.AggregateLimiterWait != 0 || m.RateLimitBytesPerSecond != 0 {
		t.Fatalf("unlimited sample reports a limiter: %+v", m)
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

func TestLimiterChargesPartialReadsAndReturnsUnusedPermits(t *testing.T) {
	budget := NewBudget(64 << 10)
	started := time.Now()
	count, _, err := copyDownload(context.Background(), &oneByteReader{remaining: 4096}, 4096, budget)
	if err != nil || count != 4096 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("partial reads consumed full chunk permits")
	}
	// The unused chunk is returned even when a read terminates with EOF.
	count, _, err = copyDownload(context.Background(), bytes.NewReader([]byte{1}), 4096, budget)
	if count != 1 || err != io.ErrUnexpectedEOF {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if budget.limiter.Tokens()+float64(budget.credit) < (32<<10)-4097 {
		t.Fatal("unused read permits were lost")
	}
}

func TestProbeHTTPFailuresAreUnknown(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			base := probe.ProbeHandler(probe.ProbeOptions{})
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/probe/download" {
					w.WriteHeader(status)
					return
				}
				base.ServeHTTP(w, r)
			}))
			defer probe.Close()
			p := model.DefaultPlan()
			p.ProbeURL = probe.URL
			p.ExitIPURL = ""
			n, err := codec.NewNode(map[string]any{"type": "direct", "name": "metric"}, model.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			reports, err := Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0))
			if err != nil {
				t.Fatal(err)
			}
			if reports[0].Status != model.Unknown || !reports[0].InfrastructureFailure {
				t.Fatalf("HTTP %d: %+v", status, reports[0])
			}
		})
	}
}

func TestURIRejectsUnrepresentableArrayValues(t *testing.T) {
	for _, reserved := range []any{[]int{1, 2}, []int{1, 2, 256}, []any{1, 2, 0.5}, "1,2,3"} {
		n, err := codec.NewNode(map[string]any{"type": "wireguard", "name": "invalid", "server": "127.0.0.1", "port": 51820, "private-key": "test", "public-key": "test", "ip": "10.0.0.2/32", "reserved": reserved}, model.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = codec.Encode([]model.Node{n}, model.URI); err == nil {
			t.Fatalf("reserved %v was silently represented", reserved)
		}
	}
	for _, alpn := range []any{[]any{"h2", 1}, []string{"h2,http/1.1"}, []string{""}, "h2"} {
		n, err := codec.NewNode(map[string]any{"type": "trojan", "name": "invalid", "server": "127.0.0.1", "port": 443, "password": "test", "alpn": alpn}, model.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = codec.Encode([]model.Node{n}, model.URI); err == nil {
			t.Fatalf("ALPN %v was silently represented", alpn)
		}
	}
}

func TestHealthHTTPFailuresAreUnknownDespiteHealthyControl(t *testing.T) {
	for _, failedRequest := range []int64{2, 3} {
		t.Run(fmt.Sprint(failedRequest), func(t *testing.T) {
			var health atomic.Int64
			base := probe.ProbeHandler(probe.ProbeOptions{})
			probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/probe/health" && health.Add(1) == failedRequest {
					w.WriteHeader(503)
					return
				}
				base.ServeHTTP(w, r)
			}))
			defer probe.Close()
			p := model.DefaultPlan()
			p.ProbeURL = probe.URL
			p.ExitIPURL = ""
			p.DownloadBytes = 1024
			n, err := codec.NewNode(map[string]any{"type": "direct", "name": "health"}, model.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			reports, err := Evaluate(context.Background(), []model.Node{n}, p, NewBudget(0))
			if err != nil {
				t.Fatal(err)
			}
			r := reports[0]
			if r.Status != model.Unknown || !r.InfrastructureFailure || r.FailureCode != "probe_http_503" {
				t.Fatalf("%+v", r)
			}
			if r.Metrics.DownloadBytes != 0 || r.GeneralReused {
				t.Fatal("failed health check downloaded or reused passing evidence")
			}
		})
	}
}

func TestMedianUsesMiddleSamples(t *testing.T) {
	for _, c := range []struct {
		values []time.Duration
		want   time.Duration
	}{
		{nil, 0}, {[]time.Duration{9}, 9}, {[]time.Duration{100, 20, 80, 40, 60}, 60}, {[]time.Duration{10, 30, 20, 40}, 25},
	} {
		if got := median(c.values); got != c.want {
			t.Fatalf("median=%s want=%s", got, c.want)
		}
	}
}

type cancelledEOFReader struct {
	ctx      context.Context
	terminal error
	sent     bool
}

func (r *cancelledEOFReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		p[0] = 1
		return 1, nil
	}
	<-r.ctx.Done()
	return 0, r.terminal
}

func TestControlledCancellationDoesNotInventEarlyEOF(t *testing.T) {
	for _, terminal := range []error{io.EOF, io.ErrUnexpectedEOF} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		count, _, err := copyDownload(ctx, &cancelledEOFReader{ctx: ctx, terminal: terminal}, 10, NewBudget(0))
		cancel()
		if count != 1 || err != context.DeadlineExceeded {
			t.Fatalf("bytes=%d err=%v", count, err)
		}
	}
}
