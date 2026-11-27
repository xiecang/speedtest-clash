package codec

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDuplicateIndependentNamesAllowedAndAmbiguousDependencyRejected(t *testing.T) {
	independent := "proxies:\n" +
		"- {name: HK, type: http, server: a.invalid, port: 80}\n" +
		"- {name: HK, type: http, server: b.invalid, port: 81}\n"
	decoded, err := Decode([]byte(independent))
	if err != nil || len(decoded.Nodes) != 2 {
		t.Fatalf("duplicate independent node names should be allowed: nodes=%d err=%v", len(decoded.Nodes), err)
	}

	ambiguous := independent + "- {name: exit, type: http, server: c.invalid, port: 82, dialer-proxy: HK}\n"
	if _, err := Decode([]byte(ambiguous)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous dependency error, got: %v", err)
	}
}

func TestDependencyClosureRejectsMissingUpstream(t *testing.T) {
	node := Node{
		Key:          strings.Repeat("a", 64),
		Name:         "exit",
		Format:       ClashYAML,
		Config:       map[string]any{"type": "http", "server": "exit.invalid", "port": 80},
		Dependencies: []string{strings.Repeat("b", 64)},
	}
	if _, err := DependencyClosure([]Node{node}, []Node{node}); err == nil || !strings.Contains(err.Error(), "missing dependency") {
		t.Fatalf("expected missing dependency error, got: %v", err)
	}
}

func TestRulesInvertRegexAndPortRanges(t *testing.T) {
	cases := []struct {
		rule string
		want map[string]any
	}{
		{
			rule: "GEOSITE,CN,DIRECT",
			want: map[string]any{"rule_set": []string{"geosite-cn"}, "action": "route", "outbound": "direct"},
		},
		{
			rule: "GEOSITE,!cn,PROXY",
			want: map[string]any{"rule_set": []string{"geosite-cn"}, "invert": true, "action": "route", "outbound": "PROXY"},
		},
		{
			rule: "GEOSITE,geolocation-!cn,PROXY",
			want: map[string]any{"rule_set": []string{"geosite-geolocation-!cn"}, "action": "route", "outbound": "PROXY"},
		},
		{
			rule: "GEOIP,!cn,PROXY,no-resolve",
			want: map[string]any{"rule_set": []string{"geoip-cn"}, "invert": true, "action": "route", "outbound": "PROXY"},
		},
		{
			rule: "DOMAIN-REGEX,^api\\..*\\.example\\.com$,PROXY",
			want: map[string]any{"domain_regex": []string{`^api\..*\.example\.com$`}, "action": "route", "outbound": "PROXY"},
		},
		{
			rule: "PROCESS-PATH-REGEX,(?i)telegram,PROXY",
			want: map[string]any{"process_path_regex": []string{"(?i)telegram"}, "action": "route", "outbound": "PROXY"},
		},
		{
			rule: "DST-PORT,80/443/8000-8080,DIRECT",
			want: map[string]any{"port": []int{80, 443}, "port_range": []string{"8000:8080"}, "action": "route", "outbound": "direct"},
		},
	}
	for _, tc := range cases {
		got, err := ConvertRule(tc.rule)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("ConvertRule(%q) = %#v, err=%v; want %#v", tc.rule, got, err, tc.want)
		}
	}
	for _, bad := range []string{
		"GEOSITE,!,DIRECT",
		"DOMAIN-REGEX,[,DIRECT",
		"PROCESS-PATH-REGEX,(unclosed,DIRECT",
		"DST-PORT,9000-8000,DIRECT",
		"DST-PORT,70000,DIRECT",
	} {
		if _, err := ConvertRule(bad); err == nil {
			t.Fatalf("expected invalid rule %q to fail", bad)
		}
	}
}

func TestModernSymmetricProtocolConversion(t *testing.T) {
	input := `proxies:
  - name: vless-ech-brutal
    type: vless
    server: edge.example.com
    port: 443
    uuid: 00000000-0000-4000-8000-000000000001
    tls: true
    servername: sni.example.com
    ech-opts:
      enable: true
      query-server-name: ech.example.com
    network: ws
    ws-opts:
      path: /up
      headers:
        Host: ws.example.com
      v2ray-http-upgrade: true
    smux:
      enabled: true
      protocol: h2mux
      max-streams: 16
      padding: true
      brutal-opts:
        enabled: true
        up: "50 Mbps"
        down: "200 Mbps"
  - name: trojan-h2
    type: trojan
    server: tj.example.com
    port: 443
    password: secret
    network: h2
    h2-opts:
      path: /grpc-web
      host: [tj.example.com]
  - name: tuic-hb
    type: tuic
    server: tuic.example.com
    port: 8443
    uuid: 00000000-0000-4000-8000-000000000002
    password: secret
    heartbeat-interval: 10000
  - name: hy2-salamander
    type: hysteria2
    server: hy2.example.com
    port: 443
    ports: "20000-30000"
    hop-interval: 30
    password: secret
    obfs: salamander
    obfs-password: obfs-secret
  - name: snell-v4
    type: snell
    server: snell.example.com
    port: 9443
    psk: snell-secret
    version: 4
    reuse: true
    obfs-opts:
      mode: tls
      host: bing.com
  - name: ssh-jump
    type: ssh
    server: ssh.example.com
    port: 22
    username: root
    password: ssh-password
    client-version: SSH-2.0-OpenSSH_9.0
  - name: ss-shadowtls
    type: ss
    server: stls.example.com
    port: 443
    cipher: 2022-blake3-aes-128-gcm
    password: AQEBAQEBAQEBAQEBAQEBAQ==
    tfo: true
    mptcp: true
    plugin: shadow-tls
    plugin-opts:
      host: cloudflare.com
      password: tls-password
      version: 3
`
	decoded, err := Decode([]byte(input))
	if err != nil {
		t.Fatalf("Decode ClashYAML failed: %v", err)
	}
	sbJSON, err := Encode(decoded.Nodes, SingBoxJSON)
	if err != nil {
		t.Fatalf("Encode SingBoxJSON failed: %v", err)
	}
	sbDecoded, err := Decode(sbJSON)
	if err != nil {
		t.Fatalf("Decode SingBoxJSON failed: %v", err)
	}
	// Check ShadowTLS attached tfo/mptcp to outer shadowtls helper, not inner shadowsocks.
	for _, n := range sbDecoded.Nodes {
		if n.Config["type"] == "shadowsocks" && (n.Config["tcp_fast_open"] == true || n.Config["tcp_multi_path"] == true) {
			t.Fatalf("tfo/mptcp leaked onto inner shadowsocks outbound: %v", n.Config)
		}
		if n.Config["type"] == "shadowtls" && (n.Config["tcp_fast_open"] != true || n.Config["tcp_multi_path"] != true) {
			t.Fatalf("tfo/mptcp missing on outer shadowtls outbound: %v", n.Config)
		}
	}

	clashProxies, err := EncodeClashProxies(sbDecoded.Nodes, DisplayNames(sbDecoded.Nodes))
	if err != nil {
		t.Fatalf("EncodeClashProxies reverse conversion failed: %v", err)
	}
	if len(clashProxies) != 7 {
		t.Fatalf("expected 7 folded Clash proxies, got %d", len(clashProxies))
	}
	byName := map[string]map[string]any{}
	for _, p := range clashProxies {
		byName[p["name"].(string)] = p
	}
	if hb, ok := byName["tuic-hb"]["heartbeat-interval"].(int); !ok || hb != 10000 {
		t.Fatalf("expected integer heartbeat-interval 10000, got %T %v", byName["tuic-hb"]["heartbeat-interval"], byName["tuic-hb"]["heartbeat-interval"])
	}
	if byName["ss-shadowtls"]["plugin"] != "shadow-tls" || byName["ss-shadowtls"]["tfo"] != true {
		t.Fatalf("expected folded shadow-tls with tfo=true, got %v", byName["ss-shadowtls"])
	}

	// Incomplete Hysteria2 obfs must fail explicitly rather than silently stripping obfs.
	badHy2, err := NewNode(map[string]any{
		"name": "bad-hy2", "type": "hysteria2", "server": "hy2.example.com", "port": 443,
		"password": "secret", "obfs": "salamander",
	}, ClashYAML)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := EncodeSingBoxNodes([]Node{badHy2}, nil); err == nil {
		t.Fatal("expected incomplete Hysteria2 obfs without obfs-password to fail")
	}
}

func TestLoaderPipeSourcesAndInlineLocalProviderBlocking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "proxies: [{name: %s, type: http, server: %s.invalid, port: 80}]", r.URL.Path[1:], r.URL.Path[1:])
	}))
	defer srv.Close()

	var loaded []Node
	if err := LoadSource(context.Background(), srv.URL+"/a|"+srv.URL+"/b", srv.Client(), func(batch []Node) error {
		loaded = append(loaded, batch...)
		return nil
	}); err != nil {
		t.Fatalf("pipe multi-source failed: %v", err)
	}
	if len(loaded) != 2 || loaded[0].Name != "a" || loaded[1].Name != "b" {
		t.Fatalf("unexpected pipe nodes: %+v", loaded)
	}

	// Inline sources must never resolve local filesystem providers even in LoadSource.
	inlineEvil := "inline:proxies: [{name: ok, type: http, server: ok.invalid, port: 80}]\nproxy-providers: {p: {url: ./local.yaml}}"
	if err := LoadSource(context.Background(), inlineEvil, nil, func([]Node) error { return nil }); err == nil {
		t.Fatal("inline source allowed local file provider in strict LoadSource")
	}
	summary, err := LoadSourceDetailed(context.Background(), inlineEvil, nil, func([]Node) error { return nil })
	if err != nil || !summary.Partial {
		t.Fatalf("expected inline local file provider to be isolated as partial failure, summary=%+v err=%v", summary, err)
	}
	if _, err := LoadSourceDetailed(context.Background(), "inline:proxies: []", nil, func([]Node) error { return nil }); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("expected ErrNoNodes, got %v", err)
	}
}
