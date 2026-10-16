// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestEightNodesShareDownloadSlotsAndCancelledWaitReleases(t *testing.T) {
	gate := make(chan struct{})
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	budget, err := NewBudgetWithConcurrency(100<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	plan := model.DefaultPlan()
	plan.ProbeURL = server.URL
	plan.DownloadBytes = 40 << 10
	plan.SampleWindow = 5 * time.Second
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { _, err := measureDownload(context.Background(), server.Client(), plan, budget); results <- err }()
	}
	deadline := time.Now().Add(3 * time.Second)
	for requests.Load() < 40 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if requests.Load() != 40 || budget.Stats().ActiveNodes != 8 {
		close(gate)
		t.Fatalf("requests=%d stats=%+v", requests.Load(), budget.Stats())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = measureDownload(ctx, server.Client(), plan, budget); err == nil {
		close(gate)
		t.Fatal("ninth node did not wait")
	}
	close(gate)
	for i := 0; i < 8; i++ {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	if _, err = measureDownload(context.Background(), server.Client(), plan, budget); err != nil {
		t.Fatal(err)
	}
	stats := budget.Stats()
	if stats.ActiveNodes != 0 || stats.DownloadedBytes != 9*plan.DownloadBytes || stats.QueueWait < 20*time.Millisecond {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestParallelReadsShare100MiBRate(t *testing.T) {
	budget, err := NewBudgetWithConcurrency(100<<20, 8)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 16<<20)
	started := time.Now()
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := copyDownload(context.Background(), bytes.NewReader(data), int64(len(data)), budget)
			errors <- err
		}()
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		if err = <-errors; err != nil && err != io.EOF {
			t.Fatal(err)
		}
	}
	if time.Since(started) < 1250*time.Millisecond || budget.Stats().DownloadedBytes != 128<<20 {
		t.Fatalf("shared rate elapsed=%s stats=%+v", time.Since(started), budget.Stats())
	}
	for _, nodes := range []int{0, 129} {
		if _, err = NewBudgetWithConcurrency(100<<20, nodes); err == nil {
			t.Fatal("invalid concurrency accepted")
		}
	}
}
