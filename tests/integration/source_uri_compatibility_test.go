// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestLegacySourceURIHintsAndParameterAliases(t *testing.T) {
	const base = "vless://00000000-0000-4000-8000-000000000001@example.invalid:443?security=tls&type=ws&path=%2Fws"
	for _, tc := range []struct {
		input, canonical string
		metadata         bool
	}{
		{base + "&spx=%2F&ech=fixture-hint", base, true},
		{base + "&servername=sni.example", base + "&sni=sni.example", false},
		{base + "&max_early_data=2048", base + "&ed=2048", false},
		{base + "&max_early_data=2048&early_data_header_name=Sec-WebSocket-Protocol", base + "&ed=2048&eh=Sec-WebSocket-Protocol", false},
		{"hy2://fixture-secret@example.invalid:443?peer=sni.example&security=tls", "hy2://fixture-secret@example.invalid:443?sni=sni.example&security=tls", false},
	} {
		var got []nq.Node
		summary, err := nq.LoadSourceDetailed(context.Background(), "inline:"+tc.input+"#fixture", nil, func(nodes []nq.Node) error { got = append(got, nodes...); return nil })
		want, decodeErr := nq.Decode([]byte(tc.canonical + "#fixture"))
		actual, marshalErr := json.Marshal(got)
		expected, expectedErr := json.Marshal(want.Nodes)
		if err != nil || decodeErr != nil || marshalErr != nil || expectedErr != nil || !bytes.Equal(actual, expected) {
			t.Fatal("legacy parameter changed", err, decodeErr, marshalErr, expectedErr)
		}
		if tc.metadata && (len(summary.Diagnostics) != 1 || summary.Diagnostics[0].Code != "legacy_uri_metadata") {
			t.Fatal("unapplied V1 hints hidden", summary)
		}
	}
	for _, input := range []string{base + "&spx=a&spx=b", base + "&sni=a&servername=b", base + "&unknownConnectionOption=1", "hy2://fixture-secret@example.invalid:443?security=none", "hy2://fixture-secret@example.invalid:443?security=", "hy2://fixture-secret@example.invalid:443?pinSHA256%20security=tls"} {
		if _, err := nq.LoadSourceDetailed(context.Background(), "inline:"+input, nil, func([]nq.Node) error { return nil }); err == nil {
			t.Fatal("invalid connection parameter accepted")
		}
	}
	if _, err := nq.Decode([]byte(base + "&ech=fixture-hint")); err == nil {
		t.Fatal("strict decode silently discarded a hint")
	}
}

func TestHysteria2CompleteAuthentication(t *testing.T) {
	for _, scheme := range []string{"hy2", "hysteria2"} {
		for _, tc := range []struct{ encoded, password string }{
			{"name:secret", "name:secret"}, {"name:", "name:"}, {":secret", ":secret"},
			{"name%3Asecret", "name:secret"}, {"name%253Asecret", "name%3Asecret"}, {"name+secret", "name+secret"},
		} {
			decoded, err := nq.Decode([]byte(scheme + "://" + tc.encoded + "@example.invalid:443#fixture"))
			if err != nil || len(decoded.Nodes) != 1 || decoded.Nodes[0].Config["password"] != tc.password {
				t.Fatalf("%s %s: %+v %v", scheme, tc.encoded, decoded, err)
			}
		}
	}
}
