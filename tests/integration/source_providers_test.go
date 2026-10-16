package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	nq "github.com/xiecang/node-quality/v2"
)

type providerTimeoutTransport struct{ base http.RoundTripper }

func (t providerTimeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/slow" {
		return t.base.RoundTrip(r)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
	defer cancel()
	return t.base.RoundTrip(r.WithContext(ctx))
}

func TestProviderFailuresKeepHealthyDocuments(t *testing.T) {
	for _, failure := range []string{"/unavailable", "/broken", "/cycle", "/nested", "/slow", "%invalid"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/root":
					fmt.Fprintf(w, "proxies: [{name: main, type: http, server: main, port: 443}, {name: dependent, type: http, server: child, port: 443, dialer-proxy: lost}]\nproxy-providers: {a: {url: %q}, b: {url: /good}, c: {type: file}, d: bad}", failure)
				case "/broken":
					fmt.Fprint(w, "proxies:\n- {name: lost, type: http, server: lost, port: 443}\nbroken: [")
				case "/cycle":
					fmt.Fprint(w, "proxy-providers: {loop: {url: /cycle}}")
				case "/nested":
					fmt.Fprint(w, "proxies: [{name: nested, type: http, server: nested, port: 443}]\nproxy-providers: {bad: {url: /unavailable}}")
				case "/slow":
					<-r.Context().Done()
				case "/good":
					fmt.Fprint(w, "proxies: [{name: good, type: http, server: good, port: 443}]")
				default:
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer server.Close()
			client := server.Client()
			client.Transport = providerTimeoutTransport{base: client.Transport}
			var names []string
			summary, err := nq.LoadSourceDetailed(context.Background(), server.URL+"/root", client, func(nodes []nq.Node) error {
				for _, node := range nodes {
					names = append(names, node.Name)
				}
				return nil
			})
			want := []string{"main", "good"}
			if failure == "/nested" {
				want = []string{"main", "nested", "good"}
			}
			if err != nil || !summary.Partial || !reflect.DeepEqual(names, want) {
				t.Fatalf("names=%v partial=%v err=%v", names, summary.Partial, err)
			}
		})
	}
}

func TestProviderFatalErrorsDoNotEmitPartialGeneration(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/root" {
				fmt.Fprint(w, "proxies: [{name: good, type: http, server: good, port: 443}]\nproxy-providers: {bad: {url: /child}}")
				return
			}
			if cancelRequest {
				cancel()
				return
			}
			fmt.Fprint(w, strings.Repeat("x", nq.MaxSourceBytes+1))
		}))
		emitted := false
		_, err := nq.LoadSourceDetailed(ctx, server.URL+"/root", server.Client(), func([]nq.Node) error { emitted = true; return nil })
		cancel()
		server.Close()
		if err == nil || emitted {
			t.Fatal("fatal error was hidden", err, emitted)
		}
	}
	callbackErr := errors.New("storage unavailable")
	_, err := nq.LoadSourceDetailed(context.Background(), "inline:http://good:443", nil, func([]nq.Node) error { return callbackErr })
	if !errors.Is(err, callbackErr) {
		t.Fatal("callback error was hidden", err)
	}
}

func TestProviderRootDeadlineDoesNotEmitPartialGeneration(t *testing.T) {
	rootEntered := make(chan struct{}, 1)
	rootCanceled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rootEntered <- struct{}{}
		<-r.Context().Done()
		rootCanceled <- struct{}{}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	emitted := false
	_, err := nq.LoadSourceDetailed(ctx, server.URL+"/root", server.Client(), func([]nq.Node) error {
		emitted = true
		return nil
	})
	select {
	case <-rootEntered:
		select {
		case <-rootCanceled:
		case <-time.After(time.Second):
			t.Fatal("entered root request did not receive cancellation")
		}
	default:
	}
	if ctx.Err() != context.DeadlineExceeded || err == nil || emitted {
		t.Fatalf("root deadline result err=%v emitted=%v context=%v", err, emitted, ctx.Err())
	}
}

func TestFailedProviderReadsConsumeBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/root" {
			fmt.Fprint(w, "proxies: [{name: good, type: http, server: good, port: 443}]\nproxy-providers: {a: {url: /a}, b: {url: /b}}")
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint((9<<20)+1))
		fmt.Fprint(w, strings.Repeat("x", 9<<20))
	}))
	defer server.Close()
	emitted := false
	_, err := nq.LoadSourceDetailed(context.Background(), server.URL+"/root", server.Client(), func([]nq.Node) error { emitted = true; return nil })
	if err == nil || emitted || !strings.Contains(err.Error(), "16MiB") {
		t.Fatal("failed response bytes bypassed the import budget", err, emitted)
	}
}
