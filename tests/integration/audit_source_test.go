// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	nq "github.com/xiecang/node-quality/v2"
)

func TestAuditSourceSharedDependencyCost(t *testing.T) {
	for _, chained := range []bool{false, true} {
		var input strings.Builder
		input.WriteString("proxies:\n")
		const count = 500
		for i := 0; i < count; i++ {
			fmt.Fprintf(&input, "- {name: n%d, type: http, server: source.invalid, port: %d", i, i+1)
			if chained && i > 0 {
				fmt.Fprintf(&input, ", dialer-proxy: n%d", min(i-1, 198))
			}
			input.WriteString("}\n")
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		batches, emitted := 0, 0
		err := nq.LoadSource(context.Background(), "inline:"+input.String(), nil, func(nodes []nq.Node) error {
			batches++
			emitted += len(nodes)
			if err := nq.ValidateDependencies(nodes); err != nil {
				t.Fatal(err)
			}
			if len(nodes) > 200 {
				t.Fatal("unbounded batch")
			}
			return nil
		})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		if after.TotalAlloc-before.TotalAlloc > 128<<20 {
			t.Fatalf("import allocation regression: %d", after.TotalAlloc-before.TotalAlloc)
		}
		t.Logf("chain=%t unique=%d batches=%d emitted=%d elapsed=%s allocated_bytes=%d", chained, count, batches, emitted, time.Since(start), after.TotalAlloc-before.TotalAlloc)
	}
}

func TestSourceEmissionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var input strings.Builder
	input.WriteString("proxies:\n")
	for i := 0; i < 201; i++ {
		fmt.Fprintf(&input, "- {name: n%d, type: http, server: example.invalid, port: %d}\n", i, i+1)
	}
	calls := 0
	err := nq.LoadSource(ctx, "inline:"+input.String(), nil, func(nodes []nq.Node) error { calls++; cancel(); return nil })
	if err != context.Canceled || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
