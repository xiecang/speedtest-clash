// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestShadowTLSDependencyPreserved(t *testing.T) {
	const source = `proxies:
  - {name: base, type: socks5, server: 127.0.0.1, port: 1080}
  - name: protected
    type: ss
    server: example.com
    port: 443
    cipher: aes-128-gcm
    password: test
    plugin: shadow-tls
    plugin-opts: {host: www.example.com, password: test, version: 3}
    dialer-proxy: base
`
	for _, tc := range []struct {
		name, source, helperDependency string
		collision                      bool
	}{
		{"front-proxy", source, "base", false},
		{"no-front-proxy", strings.ReplaceAll(source, "    dialer-proxy: base\n", ""), "", false},
		{"native-chain", `{"outbounds":[
{"type":"socks","tag":"base","server":"127.0.0.1","server_port":1080},
{"type":"shadowsocks","tag":"protected","method":"aes-128-gcm","password":"test","detour":"protected-shadowtls"},
{"type":"shadowtls","tag":"protected-shadowtls","server":"example.com","server_port":443,"version":3,"password":"test","tls":{"enabled":true,"server_name":"www.example.com"},"detour":"base"}
]}`, "base", false},
		{"helper-collision", strings.ReplaceAll(source, "base", "protected-shadowtls"), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := nq.Decode([]byte(tc.source))
			if err != nil || len(decoded.Diagnostics) != 0 {
				t.Fatalf("decode: %v diagnostics=%v", err, decoded.Diagnostics)
			}
			output, err := nq.Encode(decoded.Nodes, nq.SingBoxJSON)
			if tc.collision {
				if err == nil || !strings.Contains(err.Error(), "duplicate sing-box tag") {
					t.Fatalf("helper tag collision must be rejected: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = nq.CheckSingBox(context.Background(), output); err != nil {
				t.Fatal(err)
			}
			var document struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if err = json.Unmarshal(output, &document); err != nil {
				t.Fatal(err)
			}
			byTag := map[string]map[string]any{}
			for _, outbound := range document.Outbounds {
				byTag[outbound["tag"].(string)] = outbound
			}
			helperDependency, _ := byTag["protected-shadowtls"]["detour"].(string)
			if byTag["protected"]["detour"] != "protected-shadowtls" || helperDependency != tc.helperDependency {
				t.Fatalf("unexpected plugin chain: protected=%v helper=%v", byTag["protected"]["detour"], helperDependency)
			}
		})
	}
}
