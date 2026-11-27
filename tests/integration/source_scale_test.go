package integration_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/codec"
	"golang.org/x/sys/unix"
)

func TestDetailedSource50000(t *testing.T) {
	if os.Getenv("NQ_INSPECTION_SCALE") != "1" {
		t.Skip("explicit capacity experiment only")
	}
	var input strings.Builder
	input.WriteString("proxies:\n")
	dense := os.Getenv("NQ_INSPECTION_DENSE") == "1"
	for i := 0; i < 50000; i++ {
		if dense {
			fmt.Fprintf(&input, "- {name: n%d, type: vless, server: source.invalid, port: %d, uuid: 00000000-0000-0000-0000-000000000000, tls: true, servername: example.invalid, client-fingerprint: chrome, network: ws, ws-opts: {path: /proxy, headers: {Host: example.invalid}}}\n", i, i+1)
		} else {
			fmt.Fprintf(&input, "- {name: n%d, type: http, server: source.invalid, port: %d}\n", i, i+1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	batches, emitted, maxBatch := 0, 0, 0
	summary, err := codec.LoadSourceDetailed(ctx, "inline:"+input.String(), nil, func(nodes []codec.Node) error {
		batches++
		emitted += len(nodes)
		maxBatch = max(maxBatch, len(nodes))
		return nil
	})
	runtime.ReadMemStats(&after)
	var usage unix.Rusage
	if e := unix.Getrusage(unix.RUSAGE_SELF, &usage); e != nil {
		t.Fatal(e)
	}
	rss := usage.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	t.Logf("supported=%d input_bytes=%d batches=%d emitted=%d max_batch=%d elapsed=%s heap_before=%d heap_after=%d total_alloc=%d peak_rss=%d", summary.Supported, input.Len(), batches, emitted, maxBatch, time.Since(started), before.HeapAlloc, after.HeapAlloc, after.TotalAlloc-before.TotalAlloc, rss)
	if err != nil {
		t.Fatal(err)
	}
	if rss > 256<<20 {
		t.Fatalf("source peak RSS exceeds 256 MiB: %d", rss)
	}
	if emitted != 50000 || maxBatch > 200 {
		t.Fatal("source bounds incorrect")
	}
}
