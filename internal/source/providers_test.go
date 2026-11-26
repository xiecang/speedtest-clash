package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiecang/node-quality/v2/internal/model"
)

type mockRoundTripper func(*http.Request) (*http.Response, error)

func (f mockRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSpoolStorageFailureIsFatal(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-spool")
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	spool := sourceSpool{file: file, names: map[string][]int{}}
	_, err = spool.decode([]byte("proxies: [{name: good, type: http, server: good, port: 443}]"))
	if !errors.Is(err, errSourceStorage) || !fatalSourceError(context.Background(), err) {
		t.Fatal("storage error was treated as a bad provider", err)
	}
	if err = spool.rollback(0, 0); !errors.Is(err, errSourceStorage) {
		t.Fatal("rollback error was hidden", err)
	}
}

func TestRemoteProviderRejectsNonHTTPSchemeSmuggling(t *testing.T) {
	for _, smuggled := range []string{
		"data:text/plain,http%3A%2F%2Fsmuggled.invalid%3A80%23bad",
		"inline:proxies: [{name: bad, type: http, server: smuggled.invalid, port: 80}]",
		"file:///etc/passwd",
	} {
		body := fmt.Sprintf("proxies:\n  - {name: root, type: http, server: ok.invalid, port: 80}\nproxy-providers:\n  evil:\n    url: %q\n", smuggled)
		client := &http.Client{
			Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://sub.example.invalid/dir/sub.yaml" {
					return nil, fmt.Errorf("unexpected fetch %s", req.URL)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}),
		}

		var strictNodes []model.Node
		if err := LoadSource(context.Background(), "https://sub.example.invalid/dir/sub.yaml", client, func(batch []model.Node) error {
			strictNodes = append(strictNodes, batch...)
			return nil
		}); err == nil || len(strictNodes) != 0 {
			t.Fatalf("LoadSource accepted smuggled scheme %q: nodes=%d err=%v", smuggled, len(strictNodes), err)
		}

		var detailedNodes []model.Node
		summary, err := LoadSourceDetailed(context.Background(), "https://sub.example.invalid/dir/sub.yaml", client, func(batch []model.Node) error {
			detailedNodes = append(detailedNodes, batch...)
			return nil
		})
		if err != nil {
			t.Fatalf("LoadSourceDetailed failed instead of isolating bad provider: %v", err)
		}
		if !summary.Partial || len(detailedNodes) != 1 || detailedNodes[0].Name != "root" {
			t.Fatalf("smuggled provider not isolated: summary=%+v nodes=%+v", summary, detailedNodes)
		}
	}
}

func TestLocalRelativeProvidersAndCrossProviderDialerProxy(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "configs", "nested")
	provDir := filepath.Join(dir, "configs", "providers")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(provDir, 0o755); err != nil {
		t.Fatal(err)
	}
	providerFile := filepath.Join(provDir, "upstream.yaml")
	if err := os.WriteFile(providerFile, []byte("proxies:\n  - {name: entry, type: http, server: one.invalid, port: 80}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(subDir, "main.yaml")
	rootYAML := "proxies:\n  - {name: exit, type: http, server: two.invalid, port: 80, dialer-proxy: entry}\nproxy-providers:\n  p1:\n    url: ../providers/upstream.yaml\n"
	if err := os.WriteFile(rootFile, []byte(rootYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var strictNodes []model.Node
	if err := LoadSource(context.Background(), rootFile, nil, func(batch []model.Node) error {
		strictNodes = append(strictNodes, batch...)
		return nil
	}); err != nil {
		t.Fatalf("LoadSource cross-provider dialer-proxy failed: %v", err)
	}
	if len(strictNodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(strictNodes))
	}
	for _, n := range strictNodes {
		if err := model.ValidateIdentity(n); err != nil {
			t.Fatalf("invalid node identity on %s: %v", n.Name, err)
		}
	}

	var detailedNodes []model.Node
	summary, err := LoadSourceDetailed(context.Background(), rootFile, nil, func(batch []model.Node) error {
		detailedNodes = append(detailedNodes, batch...)
		return nil
	})
	if err != nil || summary.Partial || len(detailedNodes) != 2 {
		t.Fatalf("LoadSourceDetailed failed: summary=%+v nodes=%d err=%v", summary, len(detailedNodes), err)
	}
	if strictNodes[0].Key != detailedNodes[0].Key || strictNodes[1].Key != detailedNodes[1].Key {
		t.Fatalf("strict vs detailed keys mismatch: %v vs %v", strictNodes, detailedNodes)
	}
}
