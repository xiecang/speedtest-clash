package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	mc "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/node-quality/v2/codec"
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
			node, err := codec.NewNode(config, codec.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("WireGuard construction panicked: %v", recovered)
				}
			}()
			for range 3 {
				batch, err := OpenBatch(context.Background(), []codec.Node{node}, Mihomo)
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

func TestMihomoAllowlistAndUnsafeCapabilities(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]any
	}{
		{"disallowed_tailscale", map[string]any{"name": "ts", "type": "tailscale", "server": "192.0.2.1", "port": 443}},
		{"disallowed_openvpn", map[string]any{"name": "ovpn", "type": "openvpn", "server": "192.0.2.1", "port": 443}},
		{"disallowed_dns", map[string]any{"name": "dns", "type": "dns", "server": "192.0.2.1", "port": 53}},
		{"interface_name", map[string]any{"name": "iface", "type": "socks5", "server": "192.0.2.1", "port": 1080, "interface-name": "lo0"}},
		{"routing_mark", map[string]any{"name": "mark", "type": "socks5", "server": "192.0.2.1", "port": 1080, "routing-mark": 42}},
		{"ca_path", map[string]any{"name": "ca", "type": "trojan", "server": "192.0.2.1", "port": 443, "password": "secret", "ca": "/etc/hosts"}},
		{"cert_path", map[string]any{"name": "cert", "type": "trojan", "server": "192.0.2.1", "port": 443, "password": "secret", "certificate": "/etc/hosts"}},
		{"tls_private_key_path", map[string]any{"name": "pk", "type": "trojan", "server": "192.0.2.1", "port": 443, "password": "secret", "private-key": "/etc/hosts"}},
		{"ssh_private_key_path", map[string]any{"name": "ssh-path", "type": "ssh", "server": "192.0.2.1", "port": 22, "username": "u", "private-key": "/etc/hosts"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := codec.NewNode(tc.config, codec.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := OpenBatch(context.Background(), []codec.Node{node}, Mihomo)
			if err != nil {
				t.Fatal(err)
			}
			defer batch.Close()
			_, dialErr := batch.Dialer(node.Key)
			if dialErr == nil || !strings.Contains(dialErr.Error(), "disabled in a measurement runtime") {
				t.Fatalf("expected disabled capability error, got: %v", dialErr)
			}
		})
	}
}

type closeRecorderProxy struct {
	mc.Proxy
	id    int
	order *[]int
}

func (p *closeRecorderProxy) Close() error {
	*p.order = append(*p.order, p.id)
	return nil
}

func TestMihomoDialerNonTCPAndReverseCloseOrder(t *testing.T) {
	d := mihomoDialer{}
	if _, err := d.DialContext(context.Background(), "udp", "192.0.2.1:53"); err == nil || !strings.Contains(err.Error(), "unsupported network") {
		t.Fatalf("expected non-TCP rejection, got: %v", err)
	}

	var closed []int
	b := &Batch{
		proxies: []mc.Proxy{
			&closeRecorderProxy{id: 0, order: &closed},
			&closeRecorderProxy{id: 1, order: &closed},
			&closeRecorderProxy{id: 2, order: &closed},
		},
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if len(closed) != 3 || closed[0] != 2 || closed[1] != 1 || closed[2] != 0 {
		t.Fatalf("expected reverse close order [2 1 0], got %v", closed)
	}
}

func TestRelayCapabilities(t *testing.T) {
	before := mihomoWireGuardRoutines(t)
	wgNode, err := codec.NewNode(map[string]any{
		"name": "wg", "type": "wireguard", "server": "192.0.2.1", "port": 51820,
		"ip":          "10.0.0.2",
		"private-key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		"public-key":  base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
		"udp":         true,
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		info := RelayCapabilities(wgNode)
		if !info.SupportsTCP || !info.SupportsUDP || !info.RequiresUDP {
			t.Fatalf("unexpected wireguard relay info: %+v", info)
		}
	}
	deadline := time.Now().Add(time.Second)
	for mihomoWireGuardRoutines(t) > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := mihomoWireGuardRoutines(t); after > before {
		t.Fatalf("RelayCapabilities leaked WireGuard routines: before=%d after=%d", before, after)
	}

	httpNode, err := codec.NewNode(map[string]any{"name": "h", "type": "http", "server": "192.0.2.1", "port": 8080}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	if got := RelayCapabilities(httpNode); got.SupportsTCP || got.SupportsUDP || got.RequiresUDP {
		t.Fatalf("unexpected http relay info: %+v", got)
	}

	socksUDP, err := codec.NewNode(map[string]any{"name": "s5", "type": "socks5", "server": "192.0.2.1", "port": 1080, "udp": true}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	if got := RelayCapabilities(socksUDP); !got.SupportsTCP || !got.SupportsUDP || got.RequiresUDP {
		t.Fatalf("unexpected socks5 UDP relay info: %+v", got)
	}

	udpCases := []struct {
		name string
		cfg  map[string]any
		want bool
	}{
		{"hy2", map[string]any{"name": "x", "type": "hysteria2", "server": "192.0.2.1", "port": 443, "password": "p"}, true},
		{"masque_default", map[string]any{"name": "x", "type": "masque", "server": "192.0.2.1", "port": 443}, true},
		{"masque_h2", map[string]any{"name": "x", "type": "masque", "server": "192.0.2.1", "port": 443, "network": "h2"}, false},
		{"mieru_udp", map[string]any{"name": "x", "type": "mieru", "server": "192.0.2.1", "port": 443, "transport": "udp"}, true},
		{"mieru_tcp", map[string]any{"name": "x", "type": "mieru", "server": "192.0.2.1", "port": 443, "transport": "TCP"}, false},
		{"trusttunnel_quic", map[string]any{"name": "x", "type": "trusttunnel", "server": "192.0.2.1", "port": 443, "quic": true}, true},
		{"ss_kcptun", map[string]any{"name": "x", "type": "ss", "server": "192.0.2.1", "port": 443, "cipher": "aes-128-gcm", "password": "p", "plugin": "kcptun"}, true},
		{"vmess_quic", map[string]any{"name": "x", "type": "vmess", "server": "192.0.2.1", "port": 443, "uuid": "00000000-0000-0000-0000-000000000001", "network": "quic"}, true},
	}
	for _, tc := range udpCases {
		n := codec.Node{Key: "k-" + tc.name, Format: codec.ClashYAML, Config: tc.cfg}
		if got := RelayCapabilities(n); got.RequiresUDP != tc.want {
			t.Fatalf("%s: RequiresUDP=%v want=%v", tc.name, got.RequiresUDP, tc.want)
		}
	}
}
