// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestIPCacheProviderURLAndCheckRevision(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exit" {
			requests.Add(1)
		}
		_, _ = w.Write([]byte(`{"ip":"8.8.8.8","country_code":"US"}`))
	}))
	defer server.Close()
	p := model.DefaultPlan()
	p.ExitIPURL = server.URL + "/exit"
	p.IPProviders = map[string]string{"same": server.URL + "/first"}
	p.ProbeTimeout = time.Second
	cache := map[string]model.IPResult{}
	p.SaveIP = func(_ context.Context, r model.IPResult) error { cache[r.Provider+"@"+r.CheckRevision] = r; return nil }
	p.LookupIP = func(_ context.Context, _ string, _ int, key string) (model.IPResult, bool, error) {
		r, ok := cache[key]
		return r, ok, nil
	}
	first := queryIP(context.Background(), server.Client(), p)
	second := queryIP(context.Background(), server.Client(), p)
	if len(first) != 1 || first[0].Status != model.Pass || len(second) != 1 || requests.Load() != 1 || !first[0].MeasuredAt.Equal(second[0].MeasuredAt) {
		t.Fatal(first, second, requests.Load())
	}
	p.IPProviders["same"] = server.URL + "/second"
	third := queryIP(context.Background(), server.Client(), p)
	if requests.Load() != 2 || third[0].CheckRevision == first[0].CheckRevision {
		t.Fatal("provider URL reused stale cache", third, requests.Load())
	}
	for k, r := range cache {
		r.CheckRevision = "obsolete"
		cache[k] = r
	}
	if fourth := queryIP(context.Background(), server.Client(), p); requests.Load() != 3 || fourth[0].Status != model.Pass {
		t.Fatal("obsolete checker reused", fourth, requests.Load())
	}
}
