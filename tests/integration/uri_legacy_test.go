// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"net/url"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestLegacyURIExportsRoundTrip(t *testing.T) {
	for _, input := range []string{
		"proxies: [{name: SSR, type: ssr, server: example.invalid, port: 443, cipher: aes-128-cfb, password: test, protocol: origin, obfs: plain, protocol-param: param}]",
		"proxies: [{name: Mieru, type: mieru, server: example.invalid, port: 443, username: user, password: test, transport: TCP}]",
		"proxies: [{name: Plugin, type: ss, server: example.invalid, port: 443, cipher: aes-128-gcm, password: test, plugin: obfs, plugin-opts: {mode: http, host: example.invalid}}]",
	} {
		decoded, err := nq.Decode([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		uri, err := nq.Encode(decoded.Nodes, nq.URI)
		if err != nil {
			t.Fatal(err)
		}
		back, err := nq.Decode(uri)
		if err != nil || len(back.Nodes) != 1 {
			t.Fatalf("%s: %v", uri, err)
		}
		if back.Nodes[0].Config["type"] != decoded.Nodes[0].Config["type"] || !strings.Contains(string(uri), "://") {
			t.Fatal(back)
		}
	}
}

func TestURIEditorMetadataDoesNotAlterTransportOrIdentity(t *testing.T) {
	n, err := nq.NewNode(map[string]any{"name": "SGP", "type": "vless", "server": "example.invalid", "port": 443, "uuid": "00000000-0000-0000-0000-000000000000", "__type": "legacy-editor"}, nq.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	key := n.Key
	encoded, err := nq.Encode([]nq.Node{n}, nq.URI)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := nq.Decode(encoded)
	if err != nil || len(decoded.Nodes) != 1 || decoded.Nodes[0].Config["type"] != "vless" || n.Key != key || n.Config["__type"] != "legacy-editor" {
		t.Fatal(decoded, err)
	}
}

func TestLegacyProtocolCapabilityMatrix(t *testing.T) {
	for _, tc := range []struct {
		input string
		sing  bool
	}{
		{"proxies: [{name: SSR, type: ssr, server: example.invalid, port: 443, cipher: aes-128-cfb, password: test, protocol: origin, obfs: plain}]", false},
		{"proxies: [{name: Mieru, type: mieru, server: example.invalid, port: 443, username: user, password: test, transport: TCP}]", false},
		{"proxies: [{name: Plugin, type: ss, server: example.invalid, port: 443, cipher: aes-128-gcm, password: test, plugin: obfs, plugin-opts: {mode: http, host: example.invalid}}]", true},
		{"proxies: [{name: PluginWS, type: ss, server: example.invalid, port: 443, cipher: aes-128-gcm, password: test, plugin: v2ray-plugin, plugin-opts: {mode: websocket, host: example.invalid, path: /proxy, tls: true}}]", true},
	} {
		decoded, err := nq.Decode([]byte(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = nq.Encode(decoded.Nodes, nq.URI); err != nil {
			t.Fatal(err)
		}
		for _, core := range []nq.Core{nq.Mihomo, nq.SingBox} {
			batch, err := nq.OpenBatch(context.Background(), decoded.Nodes, core)
			if err != nil {
				t.Fatal(err)
			}
			_, dialErr := batch.Dialer(decoded.Nodes[0].Key)
			if err = batch.Close(); err != nil {
				t.Fatal(err)
			}
			want := core == nq.Mihomo || tc.sing
			if (dialErr == nil) != want {
				t.Fatalf("%s %s: supported=%t err=%v", decoded.Nodes[0].Name, core, want, dialErr)
			}
		}
	}
}

func TestPluginURIRejectsLossySettings(t *testing.T) {
	for _, plugin := range []string{"v2ray-plugin;skip-cert-verify=true", "obfs-local;mode=http"} {
		input := "ss://YWVzLTEyOC1nY206dGVzdA@example.invalid:443?plugin=" + url.QueryEscape(plugin)
		if _, err := nq.Decode([]byte(input)); err == nil {
			t.Fatal("silently dropped plugin setting", plugin)
		}
	}
}

func TestSSRURIRejectsUnsupportedUsername(t *testing.T) {
	for _, extra := range []map[string]any{{"username": "cannot-preserve"}} {
		config := map[string]any{"name": "SSR", "type": "ssr", "server": "example.invalid", "port": 443, "cipher": "aes-128-cfb", "password": "test", "protocol": "origin", "obfs": "plain"}
		for k, v := range extra {
			config[k] = v
		}
		n, err := nq.NewNode(config, nq.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = nq.Encode([]nq.Node{n}, nq.URI); err == nil {
			t.Fatal("unrepresentable SSR options silently encoded", extra)
		}
	}
}
