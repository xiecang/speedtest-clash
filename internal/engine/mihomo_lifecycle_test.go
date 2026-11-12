package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestMihomoWireGuardRemoteDNS(t *testing.T) {
	for _, scenario := range []string{"enabled", "disabled", "invalid_dns"} {
		t.Run(scenario, func(t *testing.T) {
			before := mihomoWireGuardRoutines(t)
			config := map[string]any{
				"name": "fixture", "type": "wireguard", "server": "192.0.2.1", "port": 51820,
				"ip": "10.0.0.2", "private-key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
				"public-key":         base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
				"remote-dns-resolve": scenario != "disabled", "dns": []any{"1.1.1.1"},
			}
			if scenario == "invalid_dns" {
				config["dns"] = []any{"https://[invalid"}
			}
			node, err := codec.NewNode(config, model.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("WireGuard construction panicked: %v", recovered)
				}
			}()
			for range 3 {
				batch, err := OpenBatch(context.Background(), []model.Node{node}, model.Mihomo)
				if err != nil {
					t.Fatal(err)
				}
				_, dialErr := batch.Dialer(node.Key)
				if err = batch.Close(); err != nil {
					t.Fatal(err)
				}
				if (dialErr != nil) != (scenario == "invalid_dns") {
					t.Fatalf("unexpected constructor result: %v", dialErr)
				}
			}
			deadline := time.Now().Add(time.Second)
			for mihomoWireGuardRoutines(t) > before && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if after := mihomoWireGuardRoutines(t); after > before {
				t.Fatalf("WireGuard routines leaked: before=%d after=%d", before, after)
			}
		})
	}
}

func mihomoWireGuardRoutines(t *testing.T) int {
	t.Helper()
	var stacks bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
		t.Fatal(err)
	}
	return strings.Count(stacks.String(), "github.com/metacubex/wireguard-go/device.(*Device).Routine")
}
