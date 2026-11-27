package integration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/xiecang/node-quality/v2/codec"
	"github.com/xiecang/node-quality/v2/engine"
	"github.com/xiecang/node-quality/v2/filter"
)

func TestChainIdentityIgnoresNamesAndIncludesPath(t *testing.T) {
	source := "proxies:\n- {name: parent, type: http, server: one.invalid, port: 80}\n- {name: child, type: http, server: two.invalid, port: 80, dialer-proxy: parent}\n"
	a, err := codec.Decode([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	b, err := codec.Decode([]byte(strings.ReplaceAll(strings.ReplaceAll(source, "parent", "renamed"), "child", "other")))
	if err != nil {
		t.Fatal(err)
	}
	if a.Nodes[1].Key != b.Nodes[1].Key {
		t.Fatal("display rename changed path identity")
	}
	c, err := codec.Decode([]byte(strings.ReplaceAll(source, "one.invalid", "different.invalid")))
	if err != nil {
		t.Fatal(err)
	}
	if a.Nodes[1].Key == c.Nodes[1].Key {
		t.Fatal("different dependency has same identity")
	}
	for _, node := range a.Nodes {
		if err = codec.ValidateIdentity(node); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoutingPredicatesValidateNativeSingBox(t *testing.T) {
	for _, ruleStr := range []string{
		"AND,((NETWORK,UDP),(DST-PORT,443),(DOMAIN-SUFFIX,chatgpt.com)),REJECT",
		"NOT,((DOMAIN,example.com)),REJECT",
		"DOMAIN-REGEX,^api\\..*\\.example\\.com$,DIRECT",
		"PROCESS-PATH-REGEX,(?i)curl,DIRECT",
		"DST-PORT,80/443/8000-8080,DIRECT",
	} {
		rule, err := codec.ConvertRule(ruleStr)
		if err != nil {
			t.Fatalf("ConvertRule(%q) failed: %v", ruleStr, err)
		}
		document := map[string]any{
			"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
			"route":     map[string]any{"rules": []any{rule}, "final": "direct"},
		}
		content, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err = engine.CheckSingBox(context.Background(), content); err != nil {
			t.Fatalf("CheckSingBox(%q) failed: %v", ruleStr, err)
		}
	}
	if _, err := codec.ConvertRule("UNKNOWN,example.com,DIRECT"); err == nil {
		t.Fatal("unsupported predicate accepted")
	}
	rewritten, err := codec.RewriteRuleTarget("AND,((DOMAIN-SUFFIX,example.com),(DST-PORT,443)),friendly,no-resolve", "stable-key")
	if err != nil || rewritten != "AND,((DOMAIN-SUFFIX,example.com),(DST-PORT,443)),stable-key,no-resolve" {
		t.Fatalf("%s %v", rewritten, err)
	}
}

func TestURIFormatsCredentialsAndUnknownFieldDiagnostics(t *testing.T) {
	vmessURI := "vmess://eyJ2IjoiMiIsInBzIjoidm1lc3MiLCJhZGQiOiJleGFtcGxlLmludmFsaWQiLCJwb3J0Ijo0NDMsImlkIjoiZTI4NzgzYmItNWEzZi00YjBiLTg0NzItNjkyZmE4ZjlmNTFiIiwiYWlkIjowLCJzY3kiOiJhdXRvIiwibmV0Ijoid3MiLCJ0eXBlIjoibm9uZSIsImhvc3QiOiJleGFtcGxlLmludmFsaWQiLCJwYXRoIjoiL3dzIiwidGxzIjoidGxzIiwic25pIjoiZXhhbXBsZS5pbnZhbGlkIn0="
	for _, line := range []string{
		"https://user:p%40ss@example.invalid:443#https",
		"socks5+tls://user:p%3Ass@example.invalid:443#socks",
		"hy2://password@example.invalid:443#hy2",
		"trojan://password@example.invalid:443#trojan",
		"anytls://password@example.invalid:443?sni=example.invalid#anytls",
		vmessURI,
	} {
		decoded, err := codec.Decode([]byte(line))
		if err != nil || len(decoded.Nodes) != 1 {
			t.Fatalf("decode protocol failed (%s): %v", line, err)
		}
		node := decoded.Nodes[0]
		if node.Config["skip-cert-verify"] == true {
			t.Fatal("decoder automatically disabled TLS verification")
		}
		encoded, err := codec.Encode([]codec.Node{node}, codec.URI)
		if err != nil {
			t.Fatalf("encode URI failed (%s): %v", line, err)
		}
		roundtrip, err := codec.Decode(encoded)
		if err != nil || len(roundtrip.Nodes) != 1 || roundtrip.Nodes[0].Key != node.Key {
			t.Fatalf("URI roundtrip failed (%s): %v", line, err)
		}
		if !strings.HasPrefix(line, "socks5+tls://") {
			singboxOut, err := codec.Encode([]codec.Node{node}, codec.SingBoxJSON)
			if err != nil {
				t.Fatalf("encode SingBoxJSON failed (%s): %v", line, err)
			}
			if err = engine.CheckSingBox(context.Background(), singboxOut); err != nil {
				t.Fatalf("CheckSingBox failed (%s): %v", line, err)
			}
		}
	}

	// Unknown fields preserved in ClashYAML, rejected on cross-format SingBoxJSON conversion.
	unknownRes, err := codec.Decode([]byte("proxies:\n- {name: n, type: http, server: example.invalid, port: 443, custom-transport: true}\n"))
	if err != nil || unknownRes.Nodes[0].Config["custom-transport"] != true {
		t.Fatal("unknown field lost", err)
	}
	if _, err = codec.Encode(unknownRes.Nodes, codec.SingBoxJSON); err == nil {
		t.Fatal("unknown cross-format field silently dropped")
	}

	// WireGuard SingBox -> Clash -> SingBox round-trip.
	wgContent := []byte(`{"endpoints":[{"type":"wireguard","tag":"wg","address":["10.0.0.2/32"],"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","peers":[{"address":"127.0.0.1","port":51820,"public_key":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=","allowed_ips":["0.0.0.0/0"]}]}]}`)
	wgDec, err := codec.Decode(wgContent)
	if err != nil {
		t.Fatal(err)
	}
	wgClash, err := codec.Encode(wgDec.Nodes, codec.ClashYAML)
	if err != nil || !strings.Contains(string(wgClash), "allowed-ips") {
		t.Fatal("WireGuard routes lost", err)
	}
	wgClashDec, err := codec.Decode(wgClash)
	if err != nil {
		t.Fatal(err)
	}
	wgBack, err := codec.Encode(wgClashDec.Nodes, codec.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.CheckSingBox(context.Background(), wgBack); err != nil {
		t.Fatal(err)
	}
}

func TestURIReferenceAndAllClashFieldsConformance(t *testing.T) {
	refBytes, err := os.ReadFile("../../codec/testdata/uri-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var refCases []struct {
		URI    string         `json:"uri"`
		Config map[string]any `json:"config"`
	}
	if err = json.Unmarshal(refBytes, &refCases); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range refCases {
		decoded, err := codec.Decode([]byte(fixture.URI))
		if err != nil || len(decoded.Nodes) != 1 {
			t.Fatalf("%s: %v", fixture.URI, err)
		}
		actual := decoded.Nodes[0].Config
		for _, field := range []string{"type", "name", "server", "port", "username", "password", "uuid", "network", "tls"} {
			if expected, ok := fixture.Config[field]; ok && fmt.Sprint(actual[field]) != fmt.Sprint(expected) {
				t.Fatalf("%s.%s got %v want %v", fixture.URI, field, actual[field], expected)
			}
		}
		if actual["skip-cert-verify"] == true {
			t.Fatal("insecure default must not be imported")
		}
	}

	clashFieldsBytes, err := os.ReadFile("../../codec/testdata/clash-fields.json")
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if err = json.Unmarshal(clashFieldsBytes, &original); err != nil {
		t.Fatal(err)
	}
	var nodes []codec.Node
	summary, err := codec.LoadSourceDetailed(context.Background(), "inline:"+string(clashFieldsBytes), nil, func(batch []codec.Node) error {
		nodes = append(nodes, batch...)
		return nil
	})
	if err != nil || summary.Supported != 18 || summary.Unsupported != 0 {
		t.Fatal(summary, err)
	}
	output, err := codec.Encode(nodes, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(output)
	if err != nil || len(decoded.Nodes) != 18 {
		t.Fatal(err)
	}
	byType := map[string]codec.Node{}
	for _, node := range decoded.Nodes {
		byType[node.Config["type"].(string)] = node
	}
	for _, want := range original.Proxies {
		got := byType[want["type"].(string)]
		for field, value := range want {
			if field == "name" {
				continue
			}
			actual := got.Config[field]
			if field == "hidden" {
				actual = got.Hidden
			}
			a, _ := json.Marshal(value)
			b, _ := json.Marshal(actual)
			if string(a) != string(b) {
				t.Errorf("%s.%s want %s got %s", want["type"], field, a, b)
			}
		}
	}

	// XHTTP nested URI roundtrip and target isolation.
	xhttpOpts := map[string]any{
		"path": "/upload", "host": "cdn.example", "mode": "auto", "no-grpc-header": true,
		"x-padding-key": "key", "uplink-http-method": "POST",
		"reuse-settings":    map[string]any{"max-connections": "4", "h-keep-alive-period": 10},
		"download-settings": map[string]any{"server": "download.example", "port": 8443, "path": "/download", "host": "down.example", "tls": true, "servername": "sni.example", "alpn": []string{"h2"}, "no-grpc-header": true, "sc-min-posts-interval-ms": "10", "reality-opts": map[string]any{"public-key": "pk", "short-id": "sid"}},
	}
	xhttpNode, err := codec.NewNode(map[string]any{"name": "xhttp", "type": "vless", "server": "server.example", "port": 443, "uuid": "00000000-0000-0000-0000-000000000001", "network": "xhttp", "xhttp-opts": xhttpOpts}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	xhttpURI, err := codec.Encode([]codec.Node{xhttpNode}, codec.URI)
	if err != nil {
		t.Fatal(err)
	}
	xhttpDec, err := codec.Decode(xhttpURI)
	if err != nil {
		t.Fatal(err)
	}
	wantX, _ := json.Marshal(xhttpOpts)
	gotX, _ := json.Marshal(xhttpDec.Nodes[0].Config["xhttp-opts"])
	if string(wantX) != string(gotX) {
		t.Fatalf("XHTTP nested mismatch: %s vs %s", wantX, gotX)
	}
	if _, err = codec.Encode([]codec.Node{xhttpNode}, codec.SingBoxJSON); err == nil {
		t.Fatal("sing-box dropped XHTTP")
	}

	// Hidden flag identity & expr filter.
	baseNode, _ := codec.NewNode(map[string]any{"name": "test", "type": "http", "server": "server.example", "port": 443}, codec.ClashYAML)
	for _, hidden := range []bool{false, true} {
		hn, err := codec.NewNode(map[string]any{"name": "test", "type": "http", "server": "server.example", "port": 443, "hidden": hidden}, codec.ClashYAML)
		if err != nil || hn.Key != baseNode.Key || hn.Hidden != hidden || hn.Config["hidden"] != nil {
			t.Fatal(hn, err)
		}
		sel, err := filter.Compile(filter.Options{Filter: fmt.Sprintf("hidden == %t", hidden)})
		if err != nil {
			t.Fatal(err)
		}
		mask, err := sel.Match([]codec.Node{hn})
		if err != nil || !mask[0] {
			t.Fatal(mask, err)
		}
	}

	// Lossy plugin URI rejected.
	for _, plugin := range []string{"v2ray-plugin;skip-cert-verify=true", "obfs-local;mode=http"} {
		if _, err := codec.Decode([]byte("ss://YWVzLTEyOC1nY206dGVzdA@example.invalid:443?plugin=" + url.QueryEscape(plugin))); err == nil {
			t.Fatal("silently dropped plugin setting", plugin)
		}
	}
}

func TestProtocolConstructionMatrix(t *testing.T) {
	uuid := "00000000-0000-0000-0000-000000000001"
	wgKey := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, _ := x509.MarshalECPrivateKey(ec)
	public, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)

	for _, tc := range []struct {
		name              string
		kind              string
		fields            map[string]any
		sing, mihomo, uri bool
	}{
		{"http", "http", nil, true, true, true},
		{"socks5", "socks5", nil, true, true, true},
		{"ss", "ss", map[string]any{"cipher": "aes-128-gcm", "password": "fixture"}, true, true, true},
		{"ssr", "ssr", map[string]any{"cipher": "aes-128-cfb", "password": "fixture", "protocol": "origin", "obfs": "plain"}, false, true, true},
		{"mieru", "mieru", map[string]any{"username": "user", "password": "fixture", "transport": "TCP"}, false, true, true},
		{"snell-v3", "snell", map[string]any{"psk": "fixture", "version": 3}, false, true, false},
		{"snell-v4", "snell", map[string]any{"psk": "fixture", "version": 4}, true, true, false},
		{"vmess", "vmess", map[string]any{"uuid": uuid, "cipher": "auto", "alterId": 0}, true, true, true},
		{"vless", "vless", map[string]any{"uuid": uuid}, true, true, true},
		{"trojan", "trojan", map[string]any{"password": "fixture"}, true, true, true},
		{"hysteria", "hysteria", map[string]any{"auth-str": "fixture", "up": "10", "down": "20"}, true, true, true},
		{"hysteria2", "hysteria2", map[string]any{"password": "fixture"}, true, true, true},
		{"tuic", "tuic", map[string]any{"uuid": uuid, "password": "fixture"}, true, true, true},
		{"wireguard", "wireguard", map[string]any{"private-key": wgKey, "public-key": wgKey, "ip": "10.0.0.1"}, true, true, true},
		{"ssh", "ssh", map[string]any{"username": "user", "password": "fixture"}, true, true, false},
		{"anytls", "anytls", map[string]any{"password": "fixture"}, true, true, true},
		{"sudoku", "sudoku", map[string]any{"key": "fixture"}, false, true, false},
		{"masque", "masque", map[string]any{"private-key": base64.StdEncoding.EncodeToString(private), "public-key": base64.StdEncoding.EncodeToString(public), "ip": "10.0.0.1"}, false, true, false},
		{"trusttunnel", "trusttunnel", map[string]any{"username": "user", "password": "fixture"}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{"name": tc.name, "type": tc.kind, "server": "127.0.0.1", "port": 443}
			for k, v := range tc.fields {
				config[k] = v
			}
			node, err := codec.NewNode(config, codec.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			for _, core := range []engine.Core{engine.Mihomo, engine.SingBox} {
				batch, err := engine.OpenBatch(context.Background(), []codec.Node{node}, core)
				if err != nil {
					t.Fatal(err)
				}
				_, dialErr := batch.Dialer(node.Key)
				_ = batch.Close()
				want := tc.mihomo
				if core == engine.SingBox {
					want = tc.sing
				}
				if (dialErr == nil) != want {
					t.Errorf("%s construction want %t got %v", core, want, dialErr)
				}
			}
			_, err = codec.Encode([]codec.Node{node}, codec.URI)
			if (err == nil) != tc.uri {
				t.Errorf("URI want %t got %v", tc.uri, err)
			}
			if _, err = codec.Encode([]codec.Node{node}, codec.ClashYAML); err != nil {
				t.Fatal(err)
			}
		})
	}
}
