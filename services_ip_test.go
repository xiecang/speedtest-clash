package nodequality

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/probe"
)

func TestOptionalServicesAndRevisions(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   Status
	}{
		{403, `{"cf_details":"Request is not allowed. Please try again later."}`, Pass},
		{200, "hello", Unknown},
		{403, "You may be connected to a disallowed ISP", Fail},
		{200, "verify you are human", Unknown},
		{200, "unsupported_country", Fail},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		check := ServiceCheck{Kind: "openai-mobile", URL: server.URL}
		got := checkOptionalService(context.Background(), server.Client(), check, time.Second)
		server.Close()
		if got.Status != tc.want || got.CheckRevision != ServiceCheckRevision(check) {
			t.Fatal(got, tc)
		}
	}

	for _, tc := range []struct {
		status int
		body   string
		want   Status
	}{
		{401, `{"type":"error","error":{"type":"authentication_error","message":"x-api-key header is required"}}`, Pass},
		{403, `{"type":"error","error":{"type":"forbidden","message":"Request not allowed"}}`, Fail},
		{403, "<title>Just a moment...</title>", Unknown},
		{200, "{}", Unknown},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		check := ServiceCheck{Kind: "anthropic", URL: server.URL}
		got := checkOptionalService(context.Background(), server.Client(), check, time.Second)
		server.Close()
		if got.Status != tc.want {
			t.Fatal(got, tc)
		}
	}

	p := DefaultPlan()
	before := PlanRevision(p)
	p.ServiceChecks = map[string]ServiceCheck{"Custom": {Kind: "http", URL: "https://example.com"}}
	if PlanRevision(p) != before {
		t.Fatal("ServiceChecks invalidated core PlanRevision")
	}

	for _, core := range []engine.Core{engine.Mihomo, engine.SingBox} {
		t.Run(string(core), func(t *testing.T) {
			var downloads, services atomic.Int64
			base := probe.Handler(probe.Options{})
			p, n, server := auditSetup(t, core, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/download") {
					downloads.Add(1)
				}
				if r.URL.Path == "/service" {
					services.Add(1)
					_, _ = w.Write([]byte("OK"))
					return
				}
				base.ServeHTTP(w, r)
			}))
			p.Profile = "availability"
			reports, err := Evaluate(context.Background(), []codec.Node{n}, p, NewBudget(0))
			if err != nil || len(reports) != 1 || reports[0].Status != Pass || reports[0].GeneralStatus == Pass || len(reports[0].IP) > 0 || len(reports[0].Services) > 0 || downloads.Load() != 0 {
				t.Fatal(reports, err)
			}
			p.Profile = "services"
			p.ServiceChecks = map[string]ServiceCheck{"test": {Kind: "http", URL: server.URL + "/service"}}
			for i := 0; i < 3; i++ {
				reports, err = Evaluate(context.Background(), []codec.Node{n}, p, NewBudget(0))
				if err != nil || reports[0].Services["test"].Status != Pass || downloads.Load() != 0 {
					t.Fatal(reports, err)
				}
				p.ServiceEvidence = map[string]map[string]ServiceResult{n.Key: reports[0].Services}
			}
			if services.Load() != 1 {
				t.Fatal("fresh service cache was ignored", services.Load())
			}
			p.ServiceChecks["test"] = ServiceCheck{Kind: "http", URL: server.URL + "/service?v=2"}
			if _, err = Evaluate(context.Background(), []codec.Node{n}, p, NewBudget(0)); err != nil || services.Load() != 2 {
				t.Fatal("changed service definition reused", services.Load(), err)
			}
		})
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

func TestIPCacheKeyOrderingAndDualStackConflictHandling(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exit" {
			requests.Add(1)
		}
		_, _ = w.Write([]byte(`{"ip":"8.8.8.8","country_code":"US"}`))
	}))
	defer server.Close()

	p := DefaultPlan()
	p.ExitIPURL = server.URL + "/exit"
	p.IPProviders = map[string]string{"same": server.URL + "/first"}
	p.ProbeTimeout = time.Second
	cache := map[IPCacheKey]IPResult{}
	p.SaveIP = func(_ context.Context, r IPResult) error {
		cache[IPCacheKey{IP: r.IP, Family: r.Family, Provider: r.Provider, CheckRevision: r.CheckRevision}] = r
		return nil
	}
	p.LookupIP = func(_ context.Context, key IPCacheKey) (IPResult, bool, error) {
		r, ok := cache[key]
		return r, ok, nil
	}
	first := queryIP(context.Background(), server.Client(), p)
	second := queryIP(context.Background(), server.Client(), p)
	if len(first) != 1 || first[0].Status != Pass || len(second) != 1 || requests.Load() != 1 || !first[0].MeasuredAt.Equal(second[0].MeasuredAt) {
		t.Fatal(first, second, requests.Load())
	}
	p.IPProviders["same"] = server.URL + "/second"
	third := queryIP(context.Background(), server.Client(), p)
	if requests.Load() != 2 || third[0].CheckRevision == first[0].CheckRevision {
		t.Fatal("provider URL reused stale cache", third, requests.Load())
	}

	// Deterministic provider ordering & dual-stack validation.
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
	pDual := DefaultPlan()
	pDual.ExitIPURL = "https://exit.example/ip"
	pDual.IPProviders = map[string]string{
		"z_v6": "https://v6.example/json",
		"a_v4": "https://v4.example/json/{ip}",
	}
	var savedDual []IPResult
	pDual.SaveIP = func(_ context.Context, r IPResult) error {
		savedDual = append(savedDual, r)
		return nil
	}
	dualResults := queryIP(context.Background(), dualClient, pDual)
	if len(dualResults) != 2 || dualResults[0].Provider != "a_v4" || dualResults[1].Provider != "z_v6" || len(savedDual) != 2 {
		t.Fatalf("expected sorted dual-stack results, got %+v", dualResults)
	}

	// Same-family conflict must mark Conflict=true and skip SaveIP.
	conflictClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "a.example" {
			return jsonHTTPResponse(`{"ip":"8.8.8.8","country_code":"US"}`), nil
		}
		return jsonHTTPResponse(`{"ip":"1.1.1.1","country_code":"AU"}`), nil
	})}
	pConflict := DefaultPlan()
	pConflict.ExitIPURL = ""
	pConflict.IPProviders = map[string]string{"a": "https://a.example/json", "b": "https://b.example/json"}
	var savedConflicts int
	pConflict.SaveIP = func(_ context.Context, _ IPResult) error { savedConflicts++; return nil }
	conflictResults := queryIP(context.Background(), conflictClient, pConflict)
	if savedConflicts != 0 || !conflictResults[0].Conflict || conflictResults[0].Status != Unknown {
		t.Fatalf("expected conflict without SaveIP, got %+v saved=%d", conflictResults, savedConflicts)
	}
}
