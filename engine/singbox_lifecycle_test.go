//go:build with_wireguard && with_gvisor

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/service"
	"github.com/xiecang/node-quality/v2/codec"
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
				"inbounds":  []any{map[string]any{"type": "mixed", "listen": "127.0.0.1", "listen_port": listener.Addr().(*net.TCPAddr).Port}},
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

func TestOpenSingBoxReleasesWireGuardAndIsolatesFailures(t *testing.T) {
	before := gvisorProcessors(t)
	privSecret := bytes.Repeat([]byte{0x42}, 32)
	pskSecret := bytes.Repeat([]byte{0x99}, 32)
	privB64 := base64.StdEncoding.EncodeToString(privSecret)
	privHex := hex.EncodeToString(privSecret)
	pskB64 := base64.StdEncoding.EncodeToString(pskSecret)
	pskHex := hex.EncodeToString(pskSecret)
	pubB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x07}, 32))

	healthyDirect, err := codec.NewNode(map[string]any{"type": "direct", "tag": "direct-ok"}, codec.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	healthyWG, err := codec.NewNode(map[string]any{
		"name": "wg-ok", "type": "wireguard", "server": "192.0.2.1", "port": 51820,
		"ip": "10.0.0.2/32", "private-key": privB64, "public-key": pubB64, "pre-shared-key": pskB64,
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	badWG, err := codec.NewNode(map[string]any{
		"name": "wg-bad", "type": "wireguard", "server": "192.0.2.2", "port": 51820,
		"ip": "255.255.255.255/32", "private-key": privB64, "public-key": pubB64, "pre-shared-key": pskB64,
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	badStartWG, err := codec.NewNode(map[string]any{
		"name": "wg-bad-ipc", "type": "wireguard", "server": "192.0.2.3", "port": 51820,
		"ip": "10.0.0.3/32", "private-key": base64.StdEncoding.EncodeToString([]byte("short")), "public-key": pubB64, "pre-shared-key": pskB64,
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	snellV4, err := codec.NewNode(map[string]any{
		"name": "snell-v4", "type": "snell", "server": "192.0.2.4", "port": 9443, "psk": "fixture-secret", "version": 4,
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	sshNode, err := codec.NewNode(map[string]any{
		"name": "ssh-ok", "type": "ssh", "server": "192.0.2.5", "port": 22, "username": "user", "password": "fixture-password",
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	decodedChain, err := codec.Decode([]byte(`{"outbounds":[{"type":"direct","tag":"entry"},{"type":"http","tag":"chained-ok","server":"192.0.2.10","server_port":8080,"detour":"entry"}]}`))
	if err != nil {
		t.Fatal(err)
	}

	nodes := append([]codec.Node{healthyDirect, healthyWG, snellV4, sshNode, badWG, badStartWG}, decodedChain.Nodes...)
	callerManager := &struct{ adapter.EndpointManager }{}
	ctx := service.ContextWith[adapter.EndpointManager](context.Background(), callerManager)

	for range 3 {
		batch, err := OpenBatch(ctx, nodes, SingBox)
		if err != nil {
			t.Fatalf("OpenBatch failed instead of isolating bad nodes: %v", err)
		}
		if service.FromContext[adapter.EndpointManager](ctx) != callerManager {
			_ = batch.Close()
			t.Fatal("OpenBatch replaced caller's endpoint manager")
		}
		for _, okNode := range []codec.Node{healthyDirect, healthyWG, snellV4, sshNode, decodedChain.Nodes[0], decodedChain.Nodes[1]} {
			if _, dErr := batch.Dialer(okNode.Key); dErr != nil {
				_ = batch.Close()
				t.Fatalf("healthy node %s lost dialer: %v", okNode.Name, dErr)
			}
		}
		for _, badNode := range []codec.Node{badWG, badStartWG} {
			_, dErr := batch.Dialer(badNode.Key)
			if dErr == nil {
				_ = batch.Close()
				t.Fatalf("expected failure for %s", badNode.Key)
			}
			for _, secret := range []string{privB64, privHex, pskB64, pskHex} {
				if strings.Contains(dErr.Error(), secret) {
					_ = batch.Close()
					t.Fatalf("secret leaked in failure error: %v", dErr)
				}
			}
		}
		if err := batch.Close(); err != nil {
			t.Fatalf("batch.Close failed: %v", err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for gvisorProcessors(t) > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := gvisorProcessors(t); after > before {
		t.Fatalf("OpenBatch WireGuard processors leaked: before=%d after=%d", before, after)
	}
}

func TestSanitizeEngineErrorRedactsWireGuardSecrets(t *testing.T) {
	sentinel := errors.New("unsupported cipher: invalid-cipher")
	if got := sanitizeEngineError(sentinel); got != sentinel {
		t.Fatalf("clean error was replaced: %v", got)
	}

	ipcErr := errors.New("start native sing-box: start endpoint[wireguard/wg]: setup wireguard: \nprivate_key=0102030405060708\npublic_key=deadbeef\npreshared_key=cafebabe12345678\nallowed_ip=0.0.0.0/0\n: ipcErrorf: -22")
	sanitized := sanitizeEngineError(ipcErr)
	for _, forbidden := range []string{"0102030405060708", "cafebabe12345678", "allowed_ip=0.0.0.0/0"} {
		if strings.Contains(sanitized.Error(), forbidden) {
			t.Fatalf("IPC dump leaked %q in %q", forbidden, sanitized.Error())
		}
	}
	if !strings.Contains(sanitized.Error(), "ipcErrorf: -22") {
		t.Fatalf("lost underlying non-secret cause: %q", sanitized.Error())
	}

	inlineErr := errors.New(`parse failure: private_key=secretA preshared_key=secretB "private-key":"secretC"`)
	inlineSanitized := sanitizeEngineError(inlineErr).Error()
	for _, forbidden := range []string{"secretA", "secretB", "secretC"} {
		if strings.Contains(inlineSanitized, forbidden) {
			t.Fatalf("inline secret %q leaked in %q", forbidden, inlineSanitized)
		}
	}
}
