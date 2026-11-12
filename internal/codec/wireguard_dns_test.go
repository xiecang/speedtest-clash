package codec

import (
	"github.com/xiecang/node-quality/v2/internal/model"
	"testing"
)

func TestClashConfigValidatesWireGuardDNSBeforeNativeAllocation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		config    map[string]any
		wantError bool
	}{
		{"non_wireguard", map[string]any{"type": "direct"}, false},
		{"absent_dns", map[string]any{"type": "wireguard", "remote-dns-resolve": true}, false},
		{"disabled_dns", map[string]any{"type": "wireguard", "dns": []any{"https://[invalid"}}, false},
		{"valid_dns", map[string]any{"type": "wireguard", "remote-dns-resolve": true, "dns": []any{"1.1.1.1", "https://dns.example.invalid/dns-query"}}, false},
		{"invalid_dns", map[string]any{"type": "wireguard", "remote-dns-resolve": true, "dns": []any{"https://[invalid"}}, true},
		{"invalid_type", map[string]any{"type": "wireguard", "remote-dns-resolve": map[string]any{}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := ClashConfig(model.Node{Format: model.ClashYAML, Config: tc.config})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%t", err, tc.wantError)
			}
			if err == nil {
				config["type"] = "changed"
				if tc.config["type"] == "changed" {
					t.Fatal("mutated source configuration")
				}
			}
		})
	}
}
