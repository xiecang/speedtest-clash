package speedtest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xiecang/speedtest-clash/speedtest/models"
)

func TestProxySourceLoaderLoadStreamBatchesSingleSource(t *testing.T) {
	path := writeProxyConfig(t, "one", "two", "three", "four", "five")
	loader := &ProxySourceLoader{}

	var batchSizes []int
	var names []string
	err := loader.LoadStream(context.Background(), path, 2, func(batch []map[string]any) error {
		if len(batch) > 2 {
			t.Fatalf("batch size = %d, want <= 2", len(batch))
		}
		batchSizes = append(batchSizes, len(batch))
		for _, proxy := range batch {
			names = append(names, proxy["name"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("LoadStream error = %v", err)
	}
	if got, want := len(names), 5; got != want {
		t.Fatalf("proxy count = %d, want %d", got, want)
	}
	if got, want := sprintInts(batchSizes), "2,2,1"; got != want {
		t.Fatalf("batch sizes = %s, want %s", got, want)
	}
}

func TestProxySourceLoaderLoadManyStreamBatchedRespectsSourceConcurrencyOne(t *testing.T) {
	first := writeProxyConfig(t, "a1", "a2", "a3")
	second := writeProxyConfig(t, "b1", "b2")
	loader := &ProxySourceLoader{Options: &models.Options{SourceConcurrency: 1}}

	var names []string
	err := loader.LoadManyStreamBatched(context.Background(), []string{first + "|" + second}, 2, func(batch []map[string]any) error {
		for _, proxy := range batch {
			names = append(names, proxy["name"].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("LoadManyStreamBatched error = %v", err)
	}
	if got, want := sprintStrings(names), "a1,a2,a3,b1,b2"; got != want {
		t.Fatalf("names = %s, want %s", got, want)
	}
}

func writeProxyConfig(t *testing.T, names ...string) string {
	t.Helper()
	body := "proxies:\n"
	for _, name := range names {
		body += "  - name: " + name + "\n"
		body += "    type: ss\n"
		body += "    server: 127.0.0.1\n"
		body += "    port: 8388\n"
		body += "    cipher: aes-128-gcm\n"
		body += "    password: pass\n"
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func sprintInts(values []int) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ","
		}
		out += string(rune('0' + value))
	}
	return out
}

func sprintStrings(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ","
		}
		out += value
	}
	return out
}
