package filter

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/xiecang/node-quality/v2/codec"
)

func TestExprSelectorNormalizedEnvAcrossFormats(t *testing.T) {
	clashSS, err := codec.NewNode(map[string]any{
		"name": "🇭🇰 香港 IEPL 01", "type": "ss", "server": "hk1.example.com", "port": 8443,
		"cipher": "2022-blake3-aes-128-gcm", "password": "secret", "udp": true,
		"plugin": "shadow-tls", "plugin-opts": map[string]any{"host": "cloudflare.com", "password": "p", "version": 3},
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	singBoxSS, err := codec.NewNode(map[string]any{
		"tag": "🇯🇵 东京 BGP 02", "type": "shadowsocks", "server": "jp1.example.com", "server_port": 443,
		"method": "aes-128-gcm", "password": "secret", "network": "tcp",
	}, codec.SingBoxJSON)
	if err != nil {
		t.Fatal(err)
	}
	vlessWS, err := codec.NewNode(map[string]any{
		"name": "🇺🇸 美国 01", "type": "vless", "server": "us1.example.com", "port": 2053,
		"uuid": "00000000-0000-4000-8000-000000000001", "tls": true, "udp": true,
		"network": "ws", "ws-opts": map[string]any{"path": "/ws", "v2ray-http-upgrade": true},
	}, codec.ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	nodes := []codec.Node{clashSS, singBoxSS, vlessWS}

	cases := []struct {
		expr string
		want []bool
	}{
		{`type == "ss"`, []bool{true, true, false}},
		{`type == "ss" and tls and udp`, []bool{true, false, false}},
		{`type == "ss" and !udp`, []bool{false, true, false}},
		{`network == "httpupgrade" and port in 2000..3000`, []bool{false, false, true}},
		{`name matches "(?i)香港|美国" and server endsWith ".example.com"`, []bool{true, false, true}},
		{`type in ["vless", "trojan", "hysteria2"] and config["uuid"] != nil`, []bool{false, false, true}},
	}
	for _, tc := range cases {
		s, err := Compile(Options{Filter: tc.expr})
		if err != nil {
			t.Fatalf("Compile(%q) failed: %v", tc.expr, err)
		}
		got, err := s.Match(nodes)
		if err != nil {
			t.Fatalf("Match(%q) failed: %v", tc.expr, err)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("expr %q node[%d]: got %v, want %v", tc.expr, i, got, tc.want)
			}
		}
	}
}

func TestDisplayNamesRegexAndPruneCompletePaths(t *testing.T) {
	entry := codec.Node{Key: "entry", Name: "drop-entry", Config: map[string]any{"type": "http", "server": "a.invalid", "port": 80}}
	exit := codec.Node{Key: "exit", Name: "keep-exit", Dependencies: []string{"entry"}, Config: map[string]any{"type": "http", "server": "b.invalid", "port": 80}}
	chain := codec.Node{Key: "chain", Name: "keep-chain", Dependencies: []string{"exit"}, Config: map[string]any{"type": "http", "server": "c.invalid", "port": 80}}
	good := codec.Node{Key: "good", Name: "keep-good", Config: map[string]any{"type": "http", "server": "d.invalid", "port": 80}}

	s, err := Compile(Options{
		NameRegexContain:    "^keep-",
		NameRegexNotContain: "blocked",
		Filter:              `!relay or name contains "keep"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	pruned, err := s.Prune([]codec.Node{entry, exit, chain, good})
	if err != nil || len(pruned) != 1 || pruned[0].Key != "good" {
		t.Fatalf("expected Prune to drop dependants of rejected entry, got %+v err=%v", pruned, err)
	}

	// Display override applies to `name` while `raw_name` and legacy regexes match original Name.
	dispSelector, err := Compile(Options{
		NameRegexContain: "^JP",
		Filter:           `name startsWith "🇯🇵" and raw_name == "JP-raw"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	n := codec.Node{Key: "k1", Name: "JP-raw", Config: map[string]any{"type": "vless", "server": "jp.invalid", "port": 443}}
	mask, err := dispSelector.MatchDisplay([]codec.Node{n}, map[string]string{"k1": "🇯🇵 东京 01"})
	if err != nil || !mask[0] {
		t.Fatalf("MatchDisplay failed: mask=%v err=%v", mask, err)
	}
	mask, err = dispSelector.MatchDisplay([]codec.Node{n}, map[string]string{"k1": "🇭🇰 香港 01"})
	if err != nil || mask[0] {
		t.Fatalf("MatchDisplay should reject mismatched display name: mask=%v err=%v", mask, err)
	}
}

func TestCompileRejectsNonBoolOversizedAndConcurrentExecution(t *testing.T) {
	for _, bad := range []string{
		`port + 1`,
		`name`,
		`unknown_field == "x"`,
		`name matches "["`,
		strings.Repeat("a", 4097),
	} {
		if _, err := Compile(Options{Filter: bad}); err == nil {
			t.Fatalf("expected invalid filter %q to fail compilation", bad)
		}
	}

	nodes := make([]codec.Node, 50000)
	for i := range nodes {
		nodes[i] = codec.Node{
			Key:    fmt.Sprint(i),
			Name:   fmt.Sprintf("香港 %05d", i),
			Format: codec.ClashYAML,
			Config: map[string]any{"type": "vless", "server": "edge.invalid", "port": 443, "tls": true, "network": "ws"},
		}
	}
	s, err := Compile(Options{Filter: `name contains "香港" and type == "vless" and tls and network == "ws"`})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mask, err := s.Match(nodes)
			if err != nil || len(mask) != len(nodes) || !mask[0] || !mask[len(mask)-1] {
				t.Errorf("concurrent 50k match failed: err=%v", err)
			}
		}()
	}
	wg.Wait()
}
