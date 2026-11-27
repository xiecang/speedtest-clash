// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	mlog "github.com/metacubex/mihomo/log"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/clash"
	"github.com/xiecang/node-quality/v2/internal/filter"
	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestAnyTLSAndVMessURIRoundTrips(t *testing.T) {
	vmessPayload, err := json.Marshal(map[string]any{
		"v":    "2",
		"ps":   "vmess-ws",
		"add":  "example.invalid",
		"port": 443,
		"id":   "00000000-0000-4000-8000-000000000001",
		"aid":  0,
		"scy":  "auto",
		"net":  "ws",
		"type": "none",
		"host": "cdn.example.invalid",
		"path": "/ws",
		"tls":  "tls",
		"sni":  "example.invalid",
		"alpn": "h2,http/1.1",
		"fp":   "chrome",
	})
	if err != nil {
		t.Fatal(err)
	}
	vmessURI := "vmess://" + base64.StdEncoding.EncodeToString(vmessPayload)

	for _, uriLine := range []string{
		"anytls://secret@example.invalid:443?sni=example.invalid#anytls-node",
		"anytls://p%3Ass@example.invalid:443?sni=example.invalid&fp=chrome#anytls-colon",
		vmessURI,
	} {
		t.Run(uriLine[:12], func(t *testing.T) {
			decoded, err := Decode([]byte(uriLine))
			if err != nil || len(decoded.Nodes) != 1 {
				t.Fatalf("decode failed: nodes=%d diags=%v err=%v", len(decoded.Nodes), decoded.Diagnostics, err)
			}
			node := decoded.Nodes[0]
			if node.Config["type"] == "anytls" {
				if _, hasUsername := node.Config["username"]; hasUsername {
					t.Fatalf("redundant username retained on anytls: %v", node.Config)
				}
			}
			encodedURI, err := Encode([]model.Node{node}, model.URI)
			if err != nil {
				t.Fatalf("encode URI failed: %v", err)
			}
			roundtrip, err := Decode(encodedURI)
			if err != nil || len(roundtrip.Nodes) != 1 {
				t.Fatalf("roundtrip decode failed: %v", err)
			}
			if roundtrip.Nodes[0].Key != node.Key {
				t.Fatalf("URI roundtrip changed identity:\ngot:  %+v\nwant: %+v", roundtrip.Nodes[0].Config, node.Config)
			}
			singboxData, err := Encode([]model.Node{node}, model.SingBoxJSON)
			if err != nil {
				t.Fatalf("encode SingBoxJSON failed: %v", err)
			}
			if len(singboxData) == 0 {
				t.Fatal("empty SingBoxJSON output")
			}
		})
	}

	// Distinct user:pass on anytls:// cannot be represented losslessly.
	if _, err := DecodeURI("anytls://user:different@example.invalid:443#bad"); err == nil {
		t.Fatal("distinct user:pass on anytls:// was accepted")
	}

	// VMess explicit xudp: false or non-xudp packet-encoding must be rejected in URI export.
	for _, bad := range []map[string]any{
		{"xudp": false},
		{"xudp": "true"},
		{"packet-addr": true},
		{"packet-encoding": "packetaddr"},
		{"packet-encoding": "none"},
	} {
		cfg := map[string]any{"name": "v", "type": "vmess", "server": "example.invalid", "port": 443, "uuid": "00000000-0000-4000-8000-000000000001", "cipher": "auto"}
		for k, v := range bad {
			cfg[k] = v
		}
		n, err := NewNode(cfg, model.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Encode([]model.Node{n}, model.URI); err == nil {
			t.Fatalf("lossy VMess URI export accepted for %v", bad)
		}
	}
}

func TestNormalizeURIGuardsNilTransportMaps(t *testing.T) {
	u, err := url.Parse("vless://00000000-0000-4000-8000-000000000001@example.invalid:443?host=a.invalid,b.invalid")
	if err != nil {
		t.Fatal(err)
	}
	httpCfg := map[string]any{"type": "vless", "network": "http", "http-opts": "not-a-map"}
	if err := normalizeURIConfig(u, httpCfg); err != nil {
		t.Fatal(err)
	}
	opts, ok := httpCfg["http-opts"].(map[string]any)
	if !ok || opts["headers"] == nil {
		t.Fatalf("http-opts not initialized: %v", httpCfg)
	}

	h2Cfg := map[string]any{"type": "trojan", "network": "h2"}
	if err := normalizeURIConfig(u, h2Cfg); err != nil {
		t.Fatal(err)
	}
	h2Opts, ok := h2Cfg["h2-opts"].(map[string]any)
	if !ok || !reflect.DeepEqual(h2Opts["host"], []string{"a.invalid", "b.invalid"}) {
		t.Fatalf("h2-opts not initialized: %v", h2Cfg)
	}
}

func TestEmptyScalarIntegerTypesAndCachedCheckFields(t *testing.T) {
	for _, zero := range []any{int(0), int64(0), int32(0), uint(0), uint64(0), uint32(0), float64(0), "", false, nil} {
		if !emptyScalar(zero) {
			t.Fatalf("expected zero %T(%v) to be emptyScalar", zero, zero)
		}
	}
	for _, nonZero := range []any{int(1), int64(-1), int32(2), uint(3), uint64(4), uint32(5), float64(0.5), "x", true} {
		if emptyScalar(nonZero) {
			t.Fatalf("expected non-zero %T(%v) not to be emptyScalar", nonZero, nonZero)
		}
	}
	typ := reflect.TypeOf(clash.Proxies{})
	for i := 0; i < 5; i++ {
		if err := checkFields(map[string]any{"name": "n", "type": "http", "server": "example.invalid", "port": int64(443)}, typ, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Zero int64/uint64 unconsumed fields should be treated as empty scalars.
	node, err := NewNode(map[string]any{"name": "h", "type": "http", "server": "example.invalid", "port": 8080, "fast-open": int64(0), "udp-over-stream-version": uint32(0)}, model.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode([]model.Node{node}, model.SingBoxJSON); err != nil {
		t.Fatalf("zero integer scalar rejected: %v", err)
	}
}

func TestConvertSelectedRejectsProviderOnlyYAML(t *testing.T) {
	doc := []byte("proxy-providers:\n  ext:\n    url: https://example.invalid/sub.yaml\n")
	_, _, err := ConvertSelected(context.Background(), doc, model.ClashYAML, filter.Options{})
	if err == nil || !strings.Contains(err.Error(), "proxy-providers") || !strings.Contains(err.Error(), "LoadSource") {
		t.Fatalf("expected clear proxy-providers LoadSource error, got: %v", err)
	}
}

func TestDeepCloneMapAndFastIdentityParity(t *testing.T) {
	orig := map[string]any{
		"name":    "orig",
		"type":    "vless",
		"server":  "example.invalid",
		"port":    443,
		"uuid":    "00000000-0000-4000-8000-000000000001",
		"alpn":    []string{"h2", "http/1.1"},
		"ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example.invalid"}},
		"ws-headers": map[string]string{
			"X-Test": "one",
		},
		"peers":    []any{map[string]any{"server": "1.1.1.1", "allowed-ips": []any{"0.0.0.0/0"}}},
		"reserved": []int{1, 2, 3},
	}
	node, err := NewNode(orig, model.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := ClashConfig(node)
	if err != nil {
		t.Fatal(err)
	}
	cloned["alpn"].([]string)[0] = "mutated"
	cloned["ws-opts"].(map[string]any)["path"] = "/mutated"
	cloned["ws-opts"].(map[string]any)["headers"].(map[string]any)["Host"] = "mutated.invalid"
	cloned["ws-headers"].(map[string]string)["X-Test"] = "mutated"
	cloned["peers"].([]any)[0].(map[string]any)["server"] = "9.9.9.9"
	cloned["reserved"].([]int)[0] = 99

	if err := model.ValidateIdentity(node); err != nil {
		t.Fatalf("deep mutation corrupted frozen Node.Config: %v", err)
	}

	// Verify fast IdentityKey matches stdlib json.Marshal byte-for-byte across ASCII, special chars, unicode, floats, nil/empty.
	for _, cfg := range []map[string]any{
		orig,
		{"type": "http", "server": "example.invalid", "port": float64(8080), "username": "a<b>&\"c", "password": "秘密"},
		{"type": "http", "server": "example.invalid", "port": 8080, "empty_slice": []string{}, "nil_slice": ([]string)(nil), "empty_map": map[string]any{}, "nil_map": (map[string]any)(nil), "ratio": float64(1.25)},
	} {
		for _, deps := range [][]string{nil, {}, {"dep-1", "dep-2"}} {
			got, err := model.IdentityKey(cfg, model.ClashYAML, deps)
			if err != nil {
				t.Fatal(err)
			}
			stripped := model.CloneMap(cfg)
			for _, f := range []string{"name", "tag", "dialer-proxy", "detour", "speed", "delay", "latency", "tested_at", "updated_at", "date", "quality", "hidden"} {
				delete(stripped, f)
			}
			raw, err := json.Marshal(struct {
				Format       model.Format
				Config       map[string]any
				Dependencies []string
			}{model.ClashYAML, stripped, deps})
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			want := hex.EncodeToString(sum[:])
			if got != want {
				t.Fatalf("IdentityKey mismatch:\ngot:  %s\nwant: %s\nraw:  %s", got, want, raw)
			}
		}
	}
}

func TestDecodeURISilencesMihomoConverterLogs(t *testing.T) {
	if mlog.Level() != mlog.SILENT {
		t.Fatalf("expected Mihomo log level SILENT, got %v", mlog.Level())
	}
	// Malformed vmess URI with missing port that previously triggered mihomo/log.Warnln with raw line.
	bad := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"secret","add":"1.2.3.4","id":"5c040f1d-45db-43bd-8d46-e4950e433735"}`))
	_, _ = DecodeURI(bad)
}
