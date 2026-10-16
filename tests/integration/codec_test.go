// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestChainIdentityIgnoresNamesAndIncludesPath(t *testing.T) {
	source := "proxies:\n- {name: parent, type: http, server: one.invalid, port: 80}\n- {name: child, type: http, server: two.invalid, port: 80, dialer-proxy: parent}\n"
	a, err := nq.Decode([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	b, err := nq.Decode([]byte(strings.ReplaceAll(strings.ReplaceAll(source, "parent", "renamed"), "child", "other")))
	if err != nil {
		t.Fatal(err)
	}
	if a.Nodes[1].Key != b.Nodes[1].Key {
		t.Fatal("display rename changed path identity")
	}
	c, err := nq.Decode([]byte(strings.ReplaceAll(source, "one.invalid", "different.invalid")))
	if err != nil {
		t.Fatal(err)
	}
	if a.Nodes[1].Key == c.Nodes[1].Key {
		t.Fatal("different dependency has same identity")
	}
	for _, node := range a.Nodes {
		if err = nqIdentity(node); err != nil {
			t.Fatal(err)
		}
	}
}

func nqIdentity(n nq.Node) error { return nq.ValidateIdentity(n) }

func TestRoutingPredicatesValidateNativeSingBox(t *testing.T) {
	rule, err := nq.ConvertRule("AND,((NETWORK,UDP),(DST-PORT,443),(DOMAIN-SUFFIX,chatgpt.com)),REJECT")
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}, "route": map[string]any{"rules": []any{rule}, "final": "direct"}}
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err = nq.CheckSingBox(context.Background(), content); err != nil {
		t.Fatal(err)
	}
	if _, err = nq.ConvertRule("UNKNOWN,example.com,DIRECT"); err == nil {
		t.Fatal("unsupported predicate accepted")
	}
}

func TestURIFormatsCredentialsAndVerifiedTLS(t *testing.T) {
	for _, line := range []string{"https://user:p%40ss@example.invalid:443#https", "socks5+tls://user:p%3Ass@example.invalid:443#socks", "hy2://password@example.invalid:443#hy2", "trojan://password@example.invalid:443#trojan"} {
		decoded, err := nq.Decode([]byte(line))
		if err != nil || len(decoded.Nodes) != 1 {
			t.Fatalf("decode protocol failed %v", err)
		}
		node := decoded.Nodes[0]
		if node.Config["skip-cert-verify"] == true {
			t.Fatal("decoder automatically disabled TLS verification")
		}
		encoded, err := nq.Encode([]nq.Node{node}, nq.URI)
		if err != nil {
			t.Fatal(err)
		}
		roundtrip, err := nq.Decode(encoded)
		if err != nil || len(roundtrip.Nodes) != 1 {
			t.Fatal("URI roundtrip failed")
		}
		if roundtrip.Nodes[0].Config["password"] != node.Config["password"] {
			t.Fatal("credentials corrupted on URI roundtrip")
		}
	}
}

func TestUnknownFieldsPreservedAndCrossFormatDiagnosed(t *testing.T) {
	result, err := nq.Decode([]byte("proxies:\n- {name: n, type: http, server: example.invalid, port: 443, custom-transport: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes[0].Config["custom-transport"] != true {
		t.Fatal("unknown field was lost")
	}
	if _, err = nq.Encode(result.Nodes, nq.SingBoxJSON); err == nil {
		t.Fatal("unknown cross-format field silently dropped")
	}
}

func TestNestedUnknownAndWireGuardRoundTrip(t *testing.T) {
	node, err := nq.NewNode(map[string]any{"type": "vless", "name": "unknown", "server": "example.invalid", "port": 443, "uuid": "e28783bb-5a3f-4b0b-8472-692fa8f9f51b", "network": "ws", "ws-opts": map[string]any{"path": "/", "dangerous-extra": true}}, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = nq.Encode([]nq.Node{node}, nq.SingBoxJSON); err == nil {
		t.Fatal("nested field silently lost")
	}
	content := []byte(`{"endpoints":[{"type":"wireguard","tag":"wg","address":["10.0.0.2/32"],"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","peers":[{"address":"127.0.0.1","port":51820,"public_key":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=","allowed_ips":["0.0.0.0/0"]}]}]}`)
	decoded, err := nq.Decode(content)
	if err != nil {
		t.Fatal(err)
	}
	clash, err := nq.Encode(decoded.Nodes, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(clash), "allowed-ips") {
		t.Fatal("WireGuard routes lost")
	}
	back, _, err := nq.Convert(clash, nq.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err = nq.CheckSingBox(context.Background(), back); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteLogicalRuleTarget(t *testing.T) {
	rule, err := nq.RewriteRuleTarget("AND,((DOMAIN-SUFFIX,example.com),(DST-PORT,443)),friendly,no-resolve", "stable-key")
	if err != nil || rule != "AND,((DOMAIN-SUFFIX,example.com),(DST-PORT,443)),stable-key,no-resolve" {
		t.Fatalf("%s %v", rule, err)
	}
	if _, err := nq.RewriteRuleTarget(rule, "bad,target"); err == nil {
		t.Fatal("invalid target accepted")
	}
}

// The checked-in reference was executed against the user's local cvt tree.
func TestLocalCVTBehaviorReference(t *testing.T) {
	content, err := os.ReadFile("../../testdata/cvt-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		URI    string         `json:"uri"`
		Config map[string]any `json:"config"`
	}
	if err = json.Unmarshal(content, &cases); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range cases {
		t.Run(fixture.URI, func(t *testing.T) {
			decoded, err := nq.Decode([]byte(fixture.URI))
			if err != nil || len(decoded.Nodes) != 1 {
				t.Fatalf("%+v %v", decoded, err)
			}
			actual := decoded.Nodes[0].Config
			for _, field := range []string{"type", "name", "server", "port", "username", "password", "uuid", "network", "tls"} {
				if expected, ok := fixture.Config[field]; ok && fmt.Sprint(actual[field]) != fmt.Sprint(expected) {
					t.Fatalf("%s got %v want %v", field, actual[field], expected)
				}
			}
			if actual["skip-cert-verify"] == true {
				t.Fatal("cvt's insecure default must not be imported")
			}
			if fixture.Config["network"] == "ws" {
				options, _ := actual["ws-opts"].(map[string]any)
				if options["path"] != "/chat" {
					t.Fatal("WS path lost")
				}
			}
		})
	}
}

func TestConversionRejectsDiscardedPluginAndURITLSFields(t *testing.T) {
	for _, input := range []string{
		"proxies: [{name: x, type: ss, server: example.invalid, port: 443, cipher: aes-128-gcm, password: test, plugin: obfs, plugin-opts: {mode: http, unknown: x}}]",
		"proxies: [{name: x, type: anytls, server: example.invalid, port: 443, password: test, tfo: true}]",
	} {
		if _, _, err := nq.Convert([]byte(input), nq.SingBoxJSON); err == nil {
			t.Fatal("lossy conversion accepted")
		}
	}
	if _, _, err := nq.Convert([]byte("proxies: [{name: x, type: trojan, server: example.invalid, port: 443, password: test, disable-sni: true}]"), nq.URI); err == nil {
		t.Fatal("lossy URI TLS accepted")
	}
}

func TestSourceDoesNotEmitPartialProviderGeneration(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "fixture failure", 503) }))
	defer provider.Close()
	source := fmt.Sprintf("inline:proxies: [{name: good, type: http, server: example.invalid, port: 80}]\nproxy-providers: {bad: {url: %s}}\n", provider.URL)
	emitted := false
	err := nq.LoadSource(context.Background(), source, nil, func([]nq.Node) error { emitted = true; return nil })
	if err == nil || emitted {
		t.Fatalf("partial generation emitted=%v error=%v", emitted, err)
	}
}

func TestTCPOnlyAndProtocolFieldDiagnostics(t *testing.T) {
	output, _, err := nq.Convert([]byte("proxies: [{name: x, type: socks5, server: example.invalid, port: 1080, udp: false}]"), nq.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err = nq.CheckSingBox(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"network":"tcp"`) {
		t.Fatal("UDP disable lost")
	}
	if _, _, err = nq.Convert([]byte("proxies: [{name: x, type: hysteria2, server: example.invalid, port: 443, password: test, ca-str: certificate}]"), nq.SingBoxJSON); err == nil {
		t.Fatal("ignored TLS pin accepted")
	}
}

func TestForgedMeasurementIdentityIsRejected(t *testing.T) {
	node, err := nq.NewNode(map[string]any{"name": "x", "type": "http", "server": "example.invalid", "port": 80}, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	node.Config["server"] = "changed.invalid"
	if _, err = nq.OpenBatch(context.Background(), []nq.Node{node}, nq.Mihomo); err == nil {
		t.Fatal("changed configuration kept old report identity")
	}
}

func TestDisabledNativeTLSFeaturesStayDisabled(t *testing.T) {
	node, err := nq.NewNode(map[string]any{"type": "vless", "tag": "disabled", "server": "example.invalid", "server_port": 443, "uuid": "test", "tls": map[string]any{"enabled": true, "utls": map[string]any{"enabled": false, "fingerprint": "chrome"}, "reality": map[string]any{"enabled": false, "public_key": "ignored"}}}, nq.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	config, err := nq.ClashConfig(node)
	if err != nil {
		t.Fatal(err)
	}
	if config["client-fingerprint"] != nil || config["reality-opts"] != nil {
		t.Fatalf("disabled features enabled: %v", config)
	}
}

func TestWireGuardConversionRejectsPortWrapping(t *testing.T) {
	for _, extra := range []string{"port: 70000", "port: 51820, peers: [{server: example.invalid, port: 70000, public-key: test}]", "port: 51820, mtu: -1"} {
		input := "proxies: [{name: wg, type: wireguard, server: example.invalid, ip: 10.0.0.1, private-key: test, public-key: test, " + extra + "}]"
		if _, _, err := nq.Convert([]byte(input), nq.SingBoxJSON); err == nil {
			t.Fatalf("invalid WireGuard conversion accepted: %s", extra)
		}
	}
}
