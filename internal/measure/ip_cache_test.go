// SPDX-License-Identifier: GPL-3.0-only
package measure

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestIPDualStackDoesNotConflictAndConflictingProvidersDoNotSaveCache(t *testing.T) {
	// Case 1: Dual-stack node where ExitIPURL sees IPv4 and one provider sees IPv6.
	dualClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "exit.example":
			return jsonHTTPResponse(`{"ip":"8.8.8.8"}`), nil
		case "v4.example":
			return jsonHTTPResponse(`{"ip":"8.8.8.8","country_code":"US"}`), nil
		case "v6.example":
			return jsonHTTPResponse(`{"ip":"2606:4700:4700::1111","country_code":"US"}`), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	})}
	p := model.DefaultPlan()
	p.ExitIPURL = "https://exit.example/ip"
	p.IPProviders = map[string]string{
		"p4": "https://v4.example/json/{ip}",
		"p6": "https://v6.example/json",
	}
	var savedDual []model.IPResult
	p.SaveIP = func(_ context.Context, r model.IPResult) error {
		savedDual = append(savedDual, r)
		return nil
	}
	dualResults := queryIP(context.Background(), dualClient, p)
	if len(dualResults) != 2 {
		t.Fatalf("expected 2 dual-stack results, got %+v", dualResults)
	}
	for _, r := range dualResults {
		if r.Status != model.Pass || r.Conflict {
			t.Fatalf("dual-stack result falsely marked failed/conflict: %+v", r)
		}
	}
	if len(savedDual) != 2 {
		t.Fatalf("expected both dual-stack providers saved after validation, got %d", len(savedDual))
	}

	// Case 2: Same-family conflict across two IPv4 providers must NOT invoke SaveIP.
	conflictClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "a.example":
			return jsonHTTPResponse(`{"ip":"8.8.8.8","country_code":"US"}`), nil
		case "b.example":
			return jsonHTTPResponse(`{"ip":"1.1.1.1","country_code":"AU"}`), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	})}
	pConflict := model.DefaultPlan()
	pConflict.ExitIPURL = ""
	pConflict.IPProviders = map[string]string{
		"a": "https://a.example/json",
		"b": "https://b.example/json",
	}
	var savedConflicts int
	pConflict.SaveIP = func(_ context.Context, _ model.IPResult) error {
		savedConflicts++
		return nil
	}
	conflictResults := queryIP(context.Background(), conflictClient, pConflict)
	for _, r := range conflictResults {
		if r.Status != model.Unknown || !r.Conflict {
			t.Fatalf("expected conflict unknown status, got %+v", r)
		}
	}
	if savedConflicts != 0 {
		t.Fatalf("SaveIP called %d times on conflicting providers; want 0", savedConflicts)
	}
}
