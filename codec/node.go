package codec

import (
	"errors"
	"strconv"
	"strings"
)

const (
	SchemaVersion  = 2
	MaxSourceBytes = 16 << 20
)

type Format string

const (
	ClashYAML   Format = "clash-yaml"
	SingBoxJSON Format = "sing-box-json"
	URI         Format = "uri"
)

// Node represents a normalized proxy configuration and its resolved dependency chain.
// Format-specific raw fields remain preserved in Config for lossless export, while
// methods on Node expose canonical protocol, endpoint, and transport properties.
type Node struct {
	Key          string         `json:"node_key"`
	Name         string         `json:"name"`
	Hidden       bool           `json:"hidden,omitempty"`
	Format       Format         `json:"format"`
	Config       map[string]any `json:"config"`
	Dependencies []string       `json:"dependencies,omitempty"`
}

type Diagnostic struct {
	Node    string `json:"node,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type DecodeResult struct {
	SchemaVersion int               `json:"schema_version"`
	Nodes         []Node            `json:"nodes"`
	Diagnostics   []Diagnostic      `json:"diagnostics,omitempty"`
	Providers     map[string]string `json:"providers,omitempty"`
}

func NewNode(config map[string]any, format Format) (Node, error) {
	config = CloneMap(config)
	hidden := false
	if raw, exists := config["hidden"]; exists {
		var ok bool
		hidden, ok = raw.(bool)
		if !ok {
			return Node{}, errors.New("node hidden must be boolean")
		}
		delete(config, "hidden")
	}

	if format == ClashYAML {
		if kind, ok := config["type"].(string); ok {
			switch strings.ToLower(kind) {
			case "shadowsocks":
				config["type"] = "ss"
			case "socks":
				config["type"] = "socks5"
			case "hy2":
				config["type"] = "hysteria2"
			case "hy":
				config["type"] = "hysteria"
			}
		}
		if config["type"] == "wireguard" {
			if peers, ok := config["peers"].([]any); ok {
				normalized := make([]any, 0, len(peers))
				for _, raw := range peers {
					if peer, ok := raw.(map[string]any); ok {
						p := CloneMap(peer)
						if p["allowed-ips"] == nil && p["allowed_ips"] != nil {
							p["allowed-ips"] = p["allowed_ips"]
							delete(p, "allowed_ips")
						}
						normalized = append(normalized, p)
					} else {
						normalized = append(normalized, raw)
					}
				}
				config["peers"] = normalized
			}
		}
	}

	name, _ := config["name"].(string)
	if format == SingBoxJSON {
		name, _ = config["tag"].(string)
	}
	key, err := IdentityKey(config, format, nil)
	if err != nil {
		return Node{}, err
	}
	if name == "" {
		name = key[:12]
	}
	return Node{
		Key:    key,
		Name:   name,
		Hidden: hidden,
		Format: format,
		Config: config,
	}, nil
}

// Protocol returns the canonical lowercase protocol name across Clash and sing-box formats.
func (n Node) Protocol() string {
	raw, _ := n.Config["type"].(string)
	kind := strings.ToLower(strings.TrimSpace(raw))
	switch kind {
	case "shadowsocks":
		return "ss"
	case "socks":
		return "socks5"
	case "hy2":
		return "hysteria2"
	case "hy":
		return "hysteria"
	default:
		return kind
	}
}

// Server returns the primary remote hostname or IP address of the node.
func (n Node) Server() string {
	if s, ok := n.Config["server"].(string); ok && s != "" {
		return s
	}
	if n.Protocol() == "wireguard" {
		if peers, ok := n.Config["peers"].([]any); ok && len(peers) > 0 {
			if first, ok := peers[0].(map[string]any); ok {
				if addr, ok := first["address"].(string); ok && addr != "" {
					return addr
				}
				if srv, ok := first["server"].(string); ok && srv != "" {
					return srv
				}
			}
		}
	}
	return ""
}

// Port returns the fixed remote port, or 0 if unconfigured or port-hopping only.
func (n Node) Port() int {
	if p, ok := parsePortValue(n.Config["port"]); ok {
		return p
	}
	if p, ok := parsePortValue(n.Config["server_port"]); ok {
		return p
	}
	if n.Protocol() == "wireguard" {
		if peers, ok := n.Config["peers"].([]any); ok && len(peers) > 0 {
			if first, ok := peers[0].(map[string]any); ok {
				if p, ok := parsePortValue(first["port"]); ok {
					return p
				}
			}
		}
	}
	return 0
}

// Network returns the canonical lowercase transport layer (tcp, ws, grpc, h2, http, httpupgrade, quic, xhttp, udp).
func (n Node) Network() string {
	proto := n.Protocol()
	switch proto {
	case "hysteria", "hysteria2", "tuic":
		return "quic"
	case "wireguard", "masque":
		return "udp"
	}
	if n.Format == SingBoxJSON {
		if tr, ok := n.Config["transport"].(map[string]any); ok {
			if t, ok := tr["type"].(string); ok && t != "" {
				return strings.ToLower(t)
			}
		}
		return "tcp"
	}
	if ws, ok := n.Config["ws-opts"].(map[string]any); ok && ws["v2ray-http-upgrade"] == true {
		return "httpupgrade"
	}
	if netName, ok := n.Config["network"].(string); ok && netName != "" {
		return strings.ToLower(netName)
	}
	if proto == "mieru" {
		if tr, ok := n.Config["transport"].(string); ok && tr != "" {
			return strings.ToLower(tr)
		}
	}
	if plugin, _ := n.Config["plugin"].(string); plugin == "v2ray-plugin" || plugin == "gost-plugin" {
		opts, _ := n.Config["plugin-opts"].(map[string]any)
		mode, _ := opts["mode"].(string)
		if mode == "" || strings.EqualFold(mode, "websocket") {
			return "ws"
		}
	}
	return "tcp"
}

// HasTLS reports whether the node encrypts transport with TLS, Reality, QUIC TLS, or ShadowTLS.
func (n Node) HasTLS() bool {
	proto := n.Protocol()
	switch proto {
	case "trojan", "hysteria", "hysteria2", "tuic", "anytls", "masque", "naive", "shadowtls", "trusttunnel":
		return true
	}
	if n.Format == SingBoxJSON {
		if tlsMap, ok := n.Config["tls"].(map[string]any); ok {
			if tlsMap["enabled"] == true {
				return true
			}
			if r, ok := tlsMap["reality"].(map[string]any); ok && r["enabled"] == true {
				return true
			}
		}
		return false
	}
	if n.Config["tls"] == true {
		return true
	}
	if r, ok := n.Config["reality-opts"].(map[string]any); ok {
		if pk, _ := r["public-key"].(string); pk != "" {
			return true
		}
	}
	if e, ok := n.Config["ech-opts"].(map[string]any); ok && e["enable"] == true {
		return true
	}
	if plugin, _ := n.Config["plugin"].(string); plugin != "" {
		if plugin == "shadow-tls" {
			return true
		}
		opts, _ := n.Config["plugin-opts"].(map[string]any)
		if (plugin == "v2ray-plugin" || plugin == "gost-plugin") && opts["tls"] == true {
			return true
		}
		if plugin == "obfs" && opts["mode"] == "tls" {
			return true
		}
	}
	if proto == "snell" {
		if opts, ok := n.Config["obfs-opts"].(map[string]any); ok && opts["mode"] == "tls" {
			return true
		}
	}
	return false
}

// SupportsUDP reports whether UDP relay is enabled/supported by this node configuration.
func (n Node) SupportsUDP() bool {
	proto := n.Protocol()
	switch proto {
	case "direct":
		return true
	case "http", "ssh", "naive", "tor", "shadowtls":
		return false
	case "wireguard", "hysteria", "hysteria2", "tuic", "masque":
		return n.Config["udp"] != false
	}
	if n.Format == SingBoxJSON {
		switch nv := n.Config["network"].(type) {
		case string:
			return nv == "" || strings.Contains(strings.ToLower(nv), "udp")
		case []string:
			for _, item := range nv {
				if strings.EqualFold(item, "udp") {
					return true
				}
			}
			return len(nv) == 0
		case []any:
			for _, item := range nv {
				if s, ok := item.(string); ok && strings.EqualFold(s, "udp") {
					return true
				}
			}
			return len(nv) == 0
		default:
			return true
		}
	}
	if proto == "snell" {
		if v, ok := parseIntValue(n.Config["version"]); ok && (v == 1 || v == 2) {
			return false
		}
	}
	if v, exists := n.Config["udp"]; exists {
		b, _ := v.(bool)
		return b
	}
	return false
}

// Cipher returns the symmetric cipher or security method configured on the node, if any.
func (n Node) Cipher() string {
	for _, key := range []string{"cipher", "method", "security"} {
		if s, ok := n.Config[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// DialerRef returns the raw upstream proxy name referenced by dialer-proxy or detour.
func (n Node) DialerRef() string {
	field := "dialer-proxy"
	if n.Format == SingBoxJSON {
		field = "detour"
	}
	ref, _ := n.Config[field].(string)
	return ref
}

func parsePortValue(raw any) (int, bool) {
	v, ok := parseIntValue(raw)
	if !ok || v < 1 || v > 65535 {
		return 0, false
	}
	return v, true
}

func parseIntValue(raw any) (int, bool) {
	switch val := raw.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case int32:
		return int(val), true
	case uint:
		return int(val), true
	case uint64:
		return int(val), true
	case uint32:
		return int(val), true
	case uint16:
		return int(val), true
	case float64:
		if float64(int(val)) == val {
			return int(val), true
		}
		return 0, false
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
