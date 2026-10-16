// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestCVTURIFieldConformance(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		fields map[string]any
	}{
		{"http", map[string]any{"tls": true, "sni": "host.example", "username": "u@name", "password": "p:word"}},
		{"socks5", map[string]any{"tls": true, "sni": "host.example", "username": "user", "password": "p:word"}},
		{"ss", map[string]any{"cipher": "aes-128-gcm", "password": "test", "plugin": "gost-plugin", "plugin-opts": map[string]any{"mode": "websocket", "host": "cdn.example", "path": "/ws", "tls": true, "mux": false}}},
		{"ss", map[string]any{"cipher": "aes-128-gcm", "password": "test", "plugin": "shadow-tls", "plugin-opts": map[string]any{"host": "cdn.example", "password": "plugin-password", "version": 3, "alpn": []string{"h2"}}}},
		{"ssr", map[string]any{"server": "2001:db8::1", "cipher": "aes-128-cfb", "password": "test", "protocol": "origin", "obfs": "plain", "obfs-param": "host", "protocol-param": "param"}},
		{"mieru", map[string]any{"port": nil, "port-range": "4000-4010", "username": "user", "password": "test", "transport": "TCP", "multiplexing": "MULTIPLEXING_HIGH", "handshake-mode": "HANDSHAKE_STANDARD"}},
		{"vmess", map[string]any{"uuid": "00000000-0000-0000-0000-000000000001", "cipher": "auto", "alterId": 0, "tls": true, "alpn": []string{"h2", "http/1.1"}, "client-fingerprint": "firefox", "network": "grpc", "grpc-opts": map[string]any{"grpc-service-name": "service"}}},
		{"vless", map[string]any{"uuid": "00000000-0000-0000-0000-000000000001", "tls": true, "flow": "xtls-rprx-vision", "encryption": "none", "network": "ws", "ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example"}, "v2ray-http-upgrade": true}, "reality-opts": map[string]any{"public-key": "public-key", "short-id": "1234"}, "fingerprint": "ABCD"}},
		{"trojan", map[string]any{"password": "test", "network": "h2", "h2-opts": map[string]any{"path": "/h2", "host": []string{"one.example", "two.example"}}, "client-fingerprint": "firefox", "fingerprint": "ABCD"}},
		{"hysteria", map[string]any{"auth-str": "auth", "protocol": "faketcp", "up": "10", "down": "20", "obfs": "obfs-secret", "fast-open": true, "sni": "host.example"}},
		{"hysteria2", map[string]any{"password": "p@word", "ports": "1000-2000", "up": "10", "down": "20", "obfs": "salamander", "obfs-password": "secret", "hop-interval": 30, "fingerprint": "ABCD"}},
		{"tuic", map[string]any{"uuid": "00000000-0000-0000-0000-000000000001", "password": "secret", "congestion-controller": "bbr", "udp-relay-mode": "native", "alpn": []string{"h3"}}},
		{"wireguard", map[string]any{"private-key": "private", "public-key": "public", "ip": "10.0.0.1/32", "ipv6": "fd00::1/128", "reserved": []int{1, 2, 3}, "mtu": 1400}},
		{"anytls", map[string]any{"password": "secret", "sni": "cdn.example", "alpn": []string{"h2"}, "fingerprint": "ABCD"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			config := map[string]any{"name": "test", "type": tc.kind, "server": "server.example", "port": 443}
			for key, value := range tc.fields {
				if value == nil {
					delete(config, key)
				} else {
					config[key] = value
				}
			}
			node, err := nq.NewNode(config, nq.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := nq.Encode([]nq.Node{node}, nq.URI)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := nq.Decode(encoded)
			if err != nil || len(decoded.Nodes) != 1 {
				t.Fatal(string(encoded), err)
			}
			for key, want := range tc.fields {
				if want == nil {
					continue
				}
				got := decoded.Nodes[0].Config[key]
				a, _ := json.Marshal(want)
				b, _ := json.Marshal(got)
				if string(a) != string(b) {
					t.Errorf("field %s: want %s got %s; URI %s", key, a, b, encoded)
				}
			}
		})
	}
}

func TestCVTXHTTPNestedFieldConformanceAndTargetIsolation(t *testing.T) {
	opts := map[string]any{"path": "/upload", "host": "cdn.example", "mode": "auto", "no-grpc-header": true, "x-padding-key": "key", "uplink-http-method": "POST", "reuse-settings": map[string]any{"max-connections": "4", "h-keep-alive-period": 10}, "download-settings": map[string]any{"server": "download.example", "port": 8443, "path": "/download", "host": "down.example", "tls": true, "servername": "sni.example", "alpn": []string{"h2"}, "no-grpc-header": true, "sc-min-posts-interval-ms": "10", "reality-opts": map[string]any{"public-key": "pk", "short-id": "sid"}}}
	node, err := nq.NewNode(map[string]any{"name": "xhttp", "type": "vless", "server": "server.example", "port": 443, "uuid": "00000000-0000-0000-0000-000000000001", "network": "xhttp", "xhttp-opts": opts}, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := nq.Encode([]nq.Node{node}, nq.URI)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := nq.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(opts)
	got, _ := json.Marshal(decoded.Nodes[0].Config["xhttp-opts"])
	if string(want) != string(got) {
		t.Fatal("nested fields", string(want), string(got))
	}
	if _, err = nq.Encode([]nq.Node{node}, nq.ClashYAML); err != nil {
		t.Fatal("Mihomo incorrectly blocked by sing-box", err)
	}
	if _, err = nq.Encode([]nq.Node{node}, nq.SingBoxJSON); err == nil {
		t.Fatal("sing-box dropped XHTTP")
	}
}

func TestHiddenIdentitySpoolAndFilter(t *testing.T) {
	config := map[string]any{"name": "test", "type": "http", "server": "server.example", "port": 443}
	base, err := nq.NewNode(config, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []bool{false, true} {
		config["hidden"] = hidden
		node, err := nq.NewNode(config, nq.ClashYAML)
		if err != nil || node.Key != base.Key || node.Revision != base.Revision || node.Hidden != hidden || node.Config["hidden"] != nil {
			t.Fatal(node, err)
		}
		selector, err := nq.CompileSelector(context.Background(), nq.SelectionOptions{Filter: fmt.Sprintf("hidden=%t", hidden)})
		if err != nil {
			t.Fatal(err)
		}
		mask, err := selector.Match(context.Background(), []nq.Node{node})
		if err != nil || !mask[0] {
			t.Fatal(mask, err)
		}
		input := fmt.Sprintf("inline:proxies: [{name: test, type: http, server: server.example, port: 443, hidden: %t}]", hidden)
		_, err = nq.LoadSourceDetailed(context.Background(), input, nil, func(nodes []nq.Node) error {
			if len(nodes) != 1 || nodes[0].Key != base.Key || nodes[0].Hidden != hidden {
				t.Fatal(nodes)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		yaml, err := nq.Encode([]nq.Node{node}, nq.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(yaml), "hidden: true") != hidden {
			t.Fatal(string(yaml))
		}
	}
}

func TestCVTAllClashFieldsSurviveDetailedLoadingAndExport(t *testing.T) {
	data, err := os.ReadFile("../testdata/cvt-clash-fields.json")
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if err = json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	var nodes []nq.Node
	summary, err := nq.LoadSourceDetailed(context.Background(), "inline:"+string(data), nil, func(batch []nq.Node) error { nodes = append(nodes, batch...); return nil })
	if err != nil || summary.Supported != 18 || summary.Unsupported != 0 {
		t.Fatal(summary, err)
	}
	output, err := nq.Encode(nodes, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := nq.Decode(output)
	if err != nil || len(decoded.Nodes) != 18 {
		t.Fatal(err)
	}
	byType := map[string]nq.Node{}
	for _, node := range decoded.Nodes {
		byType[node.Config["type"].(string)] = node
	}
	fields := 0
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
			fields++
		}
	}
	t.Logf("18 Clash types, %d top-level field assertions plus nested objects", fields)
}

func TestURITargetCannotSilentlyDropOtherProtocolFields(t *testing.T) {
	for _, kind := range []string{"http", "ss", "hysteria2"} {
		config := map[string]any{"name": "test", "type": kind, "server": "server.example", "port": 443, "password": "secret", "cipher": "aes-128-gcm", "network": "ws", "ws-opts": map[string]any{"path": "/ws"}}
		node, err := nq.NewNode(config, nq.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = nq.Encode([]nq.Node{node}, nq.URI); err == nil {
			t.Fatal(kind, "silently dropped protocol fields")
		}
	}
}

func TestCVTProtocolConstructionMatrix(t *testing.T) {
	uuid := "00000000-0000-0000-0000-000000000001"
	key := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind              string
		fields            map[string]any
		sing, mihomo, uri bool
	}{
		{"http", nil, true, true, true}, {"socks5", nil, true, true, true},
		{"ss", map[string]any{"cipher": "aes-128-gcm", "password": "fixture"}, true, true, true},
		{"ssr", map[string]any{"cipher": "aes-128-cfb", "password": "fixture", "protocol": "origin", "obfs": "plain"}, false, true, true},
		{"mieru", map[string]any{"username": "user", "password": "fixture", "transport": "TCP"}, false, true, true},
		{"snell", map[string]any{"psk": "fixture", "version": 3}, false, true, false},
		{"vmess", map[string]any{"uuid": uuid, "cipher": "auto", "alterId": 0}, true, true, true},
		{"vless", map[string]any{"uuid": uuid}, true, true, true},
		{"trojan", map[string]any{"password": "fixture"}, true, true, true},
		{"hysteria", map[string]any{"auth-str": "fixture", "up": "10", "down": "20"}, true, true, true},
		{"hysteria2", map[string]any{"password": "fixture"}, true, true, true},
		{"tuic", map[string]any{"uuid": uuid, "password": "fixture"}, true, true, true},
		{"wireguard", map[string]any{"private-key": key, "public-key": key, "ip": "10.0.0.1"}, true, true, true},
		{"ssh", map[string]any{"username": "user", "password": "fixture"}, false, true, false},
		{"anytls", map[string]any{"password": "fixture"}, true, true, true},
		{"sudoku", map[string]any{"key": "fixture"}, false, true, false},
		{"masque", map[string]any{"private-key": base64.StdEncoding.EncodeToString(private), "public-key": base64.StdEncoding.EncodeToString(public), "ip": "10.0.0.1"}, false, true, false},
		{"trusttunnel", map[string]any{"username": "user", "password": "fixture"}, false, true, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			config := map[string]any{"name": tc.kind, "type": tc.kind, "server": "127.0.0.1", "port": 443}
			for k, v := range tc.fields {
				config[k] = v
			}
			node, err := nq.NewNode(config, nq.ClashYAML)
			if err != nil {
				t.Fatal(err)
			}
			for _, core := range []nq.Core{nq.Mihomo, nq.SingBox} {
				batch, err := nq.OpenBatch(context.Background(), []nq.Node{node}, core)
				if err != nil {
					t.Fatal(err)
				}
				_, dialErr := batch.Dialer(node.Key)
				if err = batch.Close(); err != nil {
					t.Fatal(err)
				}
				want := tc.mihomo
				if core == nq.SingBox {
					want = tc.sing
				}
				if (dialErr == nil) != want {
					t.Errorf("%s construction want %t got %v", core, want, dialErr)
					if core == nq.SingBox {
						encoded, e := nq.Encode([]nq.Node{node}, nq.SingBoxJSON)
						if e == nil {
							e = nq.CheckSingBox(context.Background(), encoded)
						}
						t.Logf("native error: %v", e)
					}
				}
			}
			_, err = nq.Encode([]nq.Node{node}, nq.URI)
			if (err == nil) != tc.uri {
				t.Errorf("URI want %t got %v", tc.uri, err)
			}
			if _, err = nq.Encode([]nq.Node{node}, nq.ClashYAML); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExplicitURIFlagsAndAliases(t *testing.T) {
	for _, alias := range []string{"vless", "trojan-go", "hy2", "anytls", "socks5+tls"} {
		auth := "password"
		if alias == "vless" {
			auth = "00000000-0000-0000-0000-000000000001"
		}
		input := strings.ToUpper(alias) + "://" + auth + "@server.example:443?insecure=true"
		if alias == "vless" || alias == "trojan-go" {
			input += "&security=tls"
		}
		decoded, err := nq.Decode([]byte(input))
		if err != nil || len(decoded.Nodes) != 1 || decoded.Nodes[0].Config["skip-cert-verify"] != true {
			t.Fatal(alias, err)
		}
	}
	for _, mode := range []string{"none", "packet", "xudp"} {
		input := "vless://00000000-0000-0000-0000-000000000001@server.example:443?packetEncoding=" + mode
		decoded, err := nq.Decode([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := nq.Encode(decoded.Nodes, nq.URI)
		if err != nil || !strings.Contains(string(encoded), "packetEncoding="+mode) {
			t.Fatal(mode, string(encoded), err)
		}
	}
}

func TestURIUnknownConnectionParameterIsDiagnosed(t *testing.T) {
	decoded, err := nq.Decode([]byte("vless://00000000-0000-0000-0000-000000000001@server.example:443?unknown-transport=required"))
	if err == nil || len(decoded.Diagnostics) == 0 {
		t.Fatal("unknown connection parameter silently dropped")
	}
}

func TestWireGuardURIRejectsUnrepresentableAddresses(t *testing.T) {
	input := "wireguard://private@server.example:443?publickey=public&address=10.0.0.1/32,10.0.0.2/32"
	decoded, err := nq.Decode([]byte(input))
	if err == nil || len(decoded.Diagnostics) == 0 {
		t.Fatal("multiple addresses silently overwritten")
	}
}
