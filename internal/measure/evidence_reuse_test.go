// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestGeneralDownloadReuseKeepsClockAndDefinition(t *testing.T) {
	now := time.Now()
	n, _ := codec.NewNode(map[string]any{"type": "http", "server": "example.invalid", "port": 443}, model.ClashYAML)
	p := model.DefaultPlan()
	p.Profile = "ai"
	p.WorkerID = "worker"
	p.Environment = "env"
	general := p
	general.Profile = "general"
	r := newReport(n, general)
	r.Status = model.Pass
	r.GeneralMeasuredAt = now.Add(-time.Hour)
	r.FreshUntil = now.Add(time.Hour)
	r.Metrics = model.Metrics{DownloadBytes: 100, DownloadDuration: time.Second}
	p.GeneralEvidence = &r
	_, clock, ok := reusableDownload(n, p)
	if !ok || !clock.Equal(r.GeneralMeasuredAt) {
		t.Fatal("download evidence not retained")
	}
	p.Environment = "other"
	if _, _, ok = reusableDownload(n, p); ok {
		t.Fatal("foreign environment reused")
	}
	p.Environment = "env"
	p.DownloadBytes++
	if _, _, ok = reusableDownload(n, p); ok {
		t.Fatal("different measurement definition reused")
	}
}

// A fresh path failure must veto cached success, including a partial health run.
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
		w.WriteHeader(204)
	}))
	defer server.Close()
	n, _ := codec.NewNode(map[string]any{"type": "http", "server": "example.invalid", "port": 443}, model.ClashYAML)
	p := model.DefaultPlan()
	p.Profile = "ai"
	p.ProbeURL = server.URL
	p.WorkerID = "worker"
	p.Environment = "env"
	general := p
	general.Profile = "general"
	old := newReport(n, general)
	old.Status = model.Pass
	old.GeneralMeasuredAt = time.Now().Add(-time.Hour)
	old.FreshUntil = time.Now().Add(time.Hour)
	old.Metrics = model.Metrics{DownloadBytes: 100, DownloadDuration: time.Second}
	p.GeneralEvidence = &old
	// Disable HTTP retry by using a dialer that fails the second connection.
	var calls atomic.Int64
	dial := func(ctx context.Context, network, address string) (netConn net.Conn, err error) {
		if calls.Add(1) > 1 {
			return nil, errors.New("path failed")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	start := time.Now()
	r := evaluateNode(context.Background(), n, dial, p, NewBudget(0), true)
	if r.Status != model.Unknown || r.GeneralReused || r.GeneralStatus != model.Unknown || r.GeneralMeasuredAt.Before(start) {
		t.Fatalf("stale or rejected failure: %+v", r)
	}
}
