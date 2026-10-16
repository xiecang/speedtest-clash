// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nq "github.com/xiecang/node-quality/v2"
	"golang.org/x/sys/unix"
)

// Opt-in real-duration acceptance: 5/10 nodes, both native cores, 180s streams.
func TestAIParallelLongRun(t *testing.T) {
	if os.Getenv("NQ_AI_LONG_TEST") != "1" {
		t.Skip("set NQ_AI_LONG_TEST=1 for the real 180s acceptance")
	}
	var active, peak atomic.Int64
	handler := nq.ProbeHandler(nq.ProbeOptions{})
	probe := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isStream := strings.HasSuffix(r.URL.Path, "/sse") || strings.HasSuffix(r.URL.Path, "/ws")
		if isStream {
			n := active.Add(1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			defer active.Add(-1)
		}
		handler.ServeHTTP(w, r)
	}))
	probe.EnableHTTP2 = true
	probe.StartTLS()
	defer probe.Close()
	roots := x509.NewCertPool()
	roots.AddCert(probe.Certificate())
	for _, concurrency := range []int{5, 10} {
		t.Run(strconv.Itoa(concurrency), func(t *testing.T) {
			var nodes []nq.Node
			for i := 0; i < concurrency; i++ {
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstream, err := net.DialTimeout("tcp", r.Host, time.Second)
					if err != nil {
						http.Error(w, "dial", 502)
						return
					}
					defer upstream.Close()
					connection, buffer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
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
				t.Cleanup(proxy.Close)
				node, err := nq.NewNode(map[string]any{"name": "probe", "type": "http", "server": "127.0.0.1", "port": proxy.Listener.Addr().(*net.TCPAddr).Port}, nq.ClashYAML)
				if err != nil {
					t.Fatal(err)
				}
				nodes = append(nodes, node)
			}
			for _, core := range []nq.Core{nq.Mihomo, nq.SingBox} {
				plan := nq.DefaultPlan()
				plan.Core = core
				plan.Profile = "ai"
				plan.ProbeURL = probe.URL
				plan.ExitIPURL = ""
				plan.ProbeTLSConfig = &tls.Config{RootCAs: roots}
				plan.DownloadBytes = 50 << 20
				plan.AI.Concurrency = concurrency
				peak.Store(0)
				var cpuBefore unix.Rusage
				unix.Getrusage(unix.RUSAGE_SELF, &cpuBefore)
				started := time.Now()
				reports, err := nq.Evaluate(context.Background(), nodes, plan, nq.NewBudget(10<<20))
				if err != nil {
					t.Fatal(err)
				}
				var queueMax time.Duration
				for _, report := range reports {
					queueMax = max(queueMax, report.Metrics.BandwidthQueueWait)
					if report.Status != nq.Pass {
						t.Fatalf("core=%s report=%+v", core, report)
					}
				}
				var memory runtime.MemStats
				runtime.ReadMemStats(&memory)
				var cpuAfter unix.Rusage
				unix.Getrusage(unix.RUSAGE_SELF, &cpuAfter)
				cpuSeconds := float64(cpuAfter.Utime.Sec-cpuBefore.Utime.Sec+cpuAfter.Stime.Sec-cpuBefore.Stime.Sec) + float64(cpuAfter.Utime.Usec-cpuBefore.Utime.Usec+cpuAfter.Stime.Usec-cpuBefore.Stime.Usec)/1e6
				rss := cpuAfter.Maxrss
				if runtime.GOOS == "linux" {
					rss *= 1024
				}
				if peak.Load() != int64(3*concurrency) {
					t.Fatalf("stream peak=%d want=%d", peak.Load(), 3*concurrency)
				}
				if active.Load() != 0 {
					t.Fatal("stream sessions leaked")
				}
				t.Logf("core=%s nodes=%d elapsed=%s heap=%d rss_peak=%d cpu_seconds=%.3f streams_peak=%d goroutines=%d bandwidth_queue_max=%s bytes_per_node=%d", core, concurrency, time.Since(started), memory.HeapAlloc, rss, cpuSeconds, peak.Load(), runtime.NumGoroutine(), queueMax, plan.DownloadBytes)
			}
		})
	}
}
