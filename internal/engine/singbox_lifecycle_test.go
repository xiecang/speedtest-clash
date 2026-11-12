//go:build with_wireguard && with_gvisor

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"
)

func TestCheckSingBoxReleasesUnstartedWireGuard(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, scenario := range []string{
		"valid", "valid_system", "valid_mapped_address", "later_constructor_failure",
		"duplicate_local_address", "duplicate_system_address", "broadcast_address", "broadcast_system_address",
		"invalid_local_address", "invalid_system_address",
	} {
		t.Run(scenario, func(t *testing.T) {
			before := gvisorProcessors(t)
			outbound := map[string]any{"type": "direct", "tag": "direct"}
			if scenario == "later_constructor_failure" {
				outbound = map[string]any{"type": "shadowsocks", "tag": "invalid", "server": "192.0.2.1", "server_port": 443, "method": "invalid-cipher", "password": "fixture"}
			}
			addresses := []string{"10.0.0.2/32"}
			if strings.HasPrefix(scenario, "duplicate_") {
				addresses = append(addresses, "10.0.0.2/24")
			}
			if strings.HasPrefix(scenario, "broadcast_") {
				addresses = []string{"255.255.255.255/32"}
			}
			if strings.HasPrefix(scenario, "invalid_") {
				addresses = []string{""}
			}
			if scenario == "valid_mapped_address" {
				addresses = append(addresses, "::ffff:10.0.0.2/128")
			}
			content, err := json.Marshal(map[string]any{
				"endpoints": []any{map[string]any{
					"type": "wireguard", "tag": "wg", "address": addresses, "system": strings.Contains(scenario, "system"),
					"private_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
					"peers":       []any{map[string]any{"address": "192.0.2.1", "port": 51820, "public_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)), "allowed_ips": []string{"0.0.0.0/0"}}},
				}},
				"outbounds": []any{outbound},
				// An occupied listener must not matter to construction-only validation.
				"inbounds": []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": listener.Addr().(*net.TCPAddr).Port}},
			})
			if err != nil {
				t.Fatal(err)
			}
			callerManager := &struct{ adapter.EndpointManager }{}
			ctx := service.ContextWith[adapter.EndpointManager](context.Background(), callerManager)
			for range 5 {
				err := CheckSingBox(ctx, content)
				if (err != nil) != !strings.HasPrefix(scenario, "valid") {
					t.Fatalf("unexpected construction error: %v", err)
				}
				if scenario == "later_constructor_failure" && !strings.Contains(err.Error(), "invalid-cipher") {
					t.Fatalf("did not reach the later outbound constructor: %v", err)
				}
				if service.FromContext[adapter.EndpointManager](ctx) != callerManager {
					t.Fatal("validation replaced the caller's endpoint manager")
				}
			}
			deadline := time.Now().Add(time.Second)
			for gvisorProcessors(t) > before && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if after := gvisorProcessors(t); after > before {
				t.Fatalf("unstarted WireGuard processors leaked: before=%d after=%d", before, after)
			}
		})
	}
}

func gvisorProcessors(t *testing.T) int {
	t.Helper()
	var stacks bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&stacks, 2); err != nil {
		t.Fatal(err)
	}
	return strings.Count(stacks.String(), "github.com/sagernet/gvisor/pkg/tcpip/transport/tcp.(*processor).start(")
}
