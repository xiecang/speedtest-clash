// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"fmt"
	"net/netip"
	"reflect"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/clash"
	"github.com/xiecang/node-quality/v2/internal/model"
)

// ClashConfig converts native sing-box nodes only when all fields are understood.
func ClashConfig(node model.Node) (map[string]any, error) {
	if node.Format == model.ClashYAML {
		return model.CloneMap(node.Config), nil
	}
	if node.Format != model.SingBoxJSON {
		return nil, fmt.Errorf("unsupported input format %s", node.Format)
	}
	if node.Config["type"] == "wireguard" {
		return wireguardClash(node)
	}
	result := map[string]any{"name": node.Name}
	typeName, _ := node.Config["type"].(string)
	protocols := map[string]string{"direct": "direct", "http": "http", "socks": "socks5", "shadowsocks": "ss", "vmess": "vmess", "vless": "vless", "trojan": "trojan", "hysteria": "hysteria", "hysteria2": "hysteria2", "tuic": "tuic", "anytls": "anytls"}
	target, ok := protocols[typeName]
	if !ok {
		return nil, fmt.Errorf("sing-box protocol %q has no lossless Mihomo mapping", typeName)
	}
	result["type"] = target
	fields := map[string]string{"tag": "name", "server": "server", "server_port": "port", "username": "username", "password": "password", "uuid": "uuid", "method": "cipher", "security": "cipher", "alter_id": "alterId", "flow": "flow", "up_mbps": "up", "down_mbps": "down", "congestion_control": "congestion-controller", "udp_relay_mode": "udp-relay-mode", "zero_rtt_handshake": "reduce-rtt", "heartbeat": "heartbeat-interval", "tcp_fast_open": "tfo", "tcp_multi_path": "mptcp"}
	for key, value := range node.Config {
		switch key {
		case "type", "tag", "detour":
			continue
		case "tls":
			tls, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid native TLS")
			}
			tlsFields := map[string]string{"enabled": "tls", "server_name": "servername", "insecure": "skip-cert-verify", "alpn": "alpn", "disable_sni": "disable-sni"}
			for field, v := range tls {
				if field == "utls" {
					m, ok := v.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("invalid uTLS")
					}
					if m["enabled"] != true {
						continue
					}
					for k, v := range m {
						if k == "enabled" {
							continue
						}
						if k != "fingerprint" {
							return nil, fmt.Errorf("unmapped uTLS field %s", k)
						}
						result["client-fingerprint"] = v
					}
					continue
				}
				if field == "reality" {
					m, ok := v.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("invalid Reality")
					}
					if m["enabled"] != true {
						continue
					}
					out := map[string]any{}
					for k, v := range m {
						if k == "enabled" {
							continue
						}
						if k != "public_key" && k != "short_id" {
							return nil, fmt.Errorf("unmapped Reality field %s", k)
						}
						out[strings.ReplaceAll(k, "_", "-")] = v
					}
					result["reality-opts"] = out
					continue
				}
				target, ok := tlsFields[field]
				if !ok {
					return nil, fmt.Errorf("unmapped TLS field %s", field)
				}
				result[target] = v
			}
		case "transport":
			transport, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid transport")
			}
			kind, _ := transport["type"].(string)
			options := map[string]any{}
			switch kind {
			case "ws":
				result["network"] = "ws"
				for k, v := range transport {
					if k == "type" {
						continue
					}
					mapped := map[string]string{"path": "path", "headers": "headers", "max_early_data": "max-early-data", "early_data_header_name": "early-data-header-name"}[k]
					if mapped == "" {
						return nil, fmt.Errorf("unmapped WS field %s", k)
					}
					options[mapped] = v
				}
				result["ws-opts"] = options
			case "grpc":
				result["network"] = "grpc"
				for k, v := range transport {
					if k == "type" {
						continue
					}
					if k != "service_name" {
						return nil, fmt.Errorf("unmapped gRPC field %s", k)
					}
					options["grpc-service-name"] = v
				}
				result["grpc-opts"] = options
			default:
				return nil, fmt.Errorf("unmapped transport %s", kind)
			}
		default:
			target, ok := fields[key]
			if !ok {
				return nil, fmt.Errorf("unmapped native field %s", key)
			}
			result[target] = value
		}
	}
	if len(node.Dependencies) == 1 {
		result["dialer-proxy"] = node.Dependencies[0]
	}
	return result, nil
}

func checkClashFields(config map[string]any) error {
	if err := checkFields(config, reflect.TypeOf(clash.Proxies{}), ""); err != nil {
		return err
	}
	// These legacy model fields are not consumed by the upstream converter.
	for _, field := range []string{"fingerprint", "fast-open", "udp-over-stream-version"} {
		if value := config[field]; !emptyScalar(value) {
			return fmt.Errorf("Clash field %q has no lossless sing-box mapping", field)
		}
	}
	if network, _ := config["network"].(string); network != "" && network != "tcp" && network != "ws" && network != "grpc" && network != "h2" && network != "http" {
		return fmt.Errorf("unsupported transport %q", network)
	}
	kind, _ := config["type"].(string)
	fields := map[string]string{
		"direct":    "",
		"ss":        "cipher udp-over-tcp udp-over-tcp-version plugin plugin-opts client-fingerprint smux",
		"http":      "username tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts",
		"socks5":    "username tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts",
		"vmess":     "uuid alterId cipher global-padding authenticated-length packet-encoding packet_encoding network ws-opts ws-headers grpc-opts h2-opts http-opts tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts smux",
		"vless":     "uuid flow packet-encoding packet_encoding network ws-opts ws-headers grpc-opts h2-opts http-opts tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts smux",
		"trojan":    "network ws-opts ws-headers grpc-opts tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts smux",
		"hysteria":  "ports hop-interval up down auth-str auth_str ca-str ca_str protocol obfs recv-window recv_window recv-window-conn recv_window_conn disable_mtu_discovery tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts",
		"hysteria2": "ports hop-interval up down obfs obfs-password tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts",
		"tuic":      "uuid ip congestion-controller udp-relay-mode udp-over-stream reduce-rtt heartbeat-interval tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts",
		"anytls":    "idle-session-check-interval idle-session-timeout min-idle-session tls skip-cert-verify servername sni alpn client-fingerprint disable-sni reality-opts",
		"wireguard": "ip ipv6 public-key pre-shared-key private-key reserved peers mtu",
	}[kind]
	allowed := " name type server port password dialer-proxy udp tfo mptcp " + fields + " "
	for field, value := range config {
		if !strings.Contains(allowed, " "+field+" ") && !emptyScalar(value) {
			return fmt.Errorf("Clash %s field %q is not consumed by sing-box conversion", kind, field)
		}
	}
	if plugin, _ := config["plugin"].(string); plugin != "" {
		fields := map[string]string{"obfs": "mode host", "v2ray-plugin": "mode tls host path mux", "shadow-tls": "host password version"}[plugin]
		if fields == "" {
			return fmt.Errorf("unsupported SS plugin %q", plugin)
		}
		if options, ok := config["plugin-opts"].(map[string]any); ok {
			for key := range options {
				if !strings.Contains(" "+fields+" ", " "+key+" ") {
					return fmt.Errorf("unmapped plugin option %q", key)
				}
			}
		}
	}
	// Reject recognized but unconsumed fields, instead of silently changing the path.
	if kind != "hysteria" && (!emptyScalar(config["protocol"]) || !emptyScalar(config["protocol-param"]) || !emptyScalar(config["obfs-param"])) {
		return fmt.Errorf("unmapped protocol/obfs options")
	}
	if kind == "anytls" && config["tfo"] == true {
		return fmt.Errorf("AnyTLS tfo cannot be mapped")
	}
	if kind == "wireguard" {
		if peers, ok := config["peers"].([]any); ok {
			for _, value := range peers {
				if peer, ok := value.(map[string]any); ok && (!emptyScalar(peer["ip"]) || !emptyScalar(peer["ipv6"])) {
					return fmt.Errorf("WireGuard peer IP fields cannot be mapped; use allowed-ips")
				}
			}
		}
	}
	if kind == "vless" && config["network"] == "ws" && !emptyScalar(config["flow"]) {
		return fmt.Errorf("VLESS flow cannot be combined with WS safely")
	}
	for _, field := range []string{"packet-encoding", "packet_encoding"} {
		if value, ok := config[field].(string); ok && value != "" && value != "packetaddr" && value != "packet" && value != "xudp" {
			return fmt.Errorf("unmapped packet encoding %q", value)
		}
	}
	if options, ok := config["http-opts"].(map[string]any); ok {
		if paths, ok := options["path"].([]any); ok && len(paths) > 1 {
			return fmt.Errorf("multiple HTTP paths cannot be mapped")
		}
	}
	return nil
}

func checkFields(config map[string]any, model reflect.Type, path string) error {
	fields := make(map[string]reflect.Type)
	for i := 0; i < model.NumField(); i++ {
		f := model.Field(i)
		fields[strings.Split(f.Tag.Get("yaml"), ",")[0]] = f.Type
	}
	for field, value := range config {
		typ, ok := fields[field]
		if !ok {
			return fmt.Errorf("Clash field %q cannot be represented in sing-box", path+field)
		}
		for typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if nested, ok := value.(map[string]any); ok && typ.Kind() == reflect.Struct && field != "plugin-opts" && field != "reserved" {
			if err := checkFields(nested, typ, path+field+"."); err != nil {
				return err
			}
		}
		if list, ok := value.([]any); ok && typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Struct {
			for _, item := range list {
				if nested, ok := item.(map[string]any); ok {
					if err := checkFields(nested, typ.Elem(), path+field+"."); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func wireguardClash(node model.Node) (map[string]any, error) {
	out := map[string]any{"name": node.Name, "type": "wireguard"}
	for key, value := range node.Config {
		switch key {
		case "type", "tag", "detour":
		case "system":
			if value == true {
				return nil, fmt.Errorf("system WireGuard cannot be represented safely")
			}
		case "private_key":
			out["private-key"] = value
		case "mtu":
			out["mtu"] = value
		case "address":
			addresses, ok := value.([]any)
			if !ok {
				if text, ok := value.(string); ok {
					addresses = []any{text}
				}
			}
			for _, raw := range addresses {
				text, ok := raw.(string)
				if !ok {
					return nil, fmt.Errorf("invalid WireGuard address")
				}
				prefix, err := netip.ParsePrefix(text)
				if err != nil {
					return nil, err
				}
				field := "ipv6"
				if prefix.Addr().Is4() {
					field = "ip"
				}
				if out[field] != nil {
					return nil, fmt.Errorf("multiple WireGuard addresses cannot be mapped")
				}
				out[field] = prefix.String()
			}
		case "peers":
			peers, ok := value.([]any)
			if !ok || len(peers) == 0 {
				return nil, fmt.Errorf("WireGuard peers required")
			}
			var mapped []any
			fields := map[string]string{"address": "server", "port": "port", "public_key": "public-key", "pre_shared_key": "pre-shared-key", "allowed_ips": "allowed-ips", "reserved": "reserved"}
			for _, raw := range peers {
				peer, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid WireGuard peer")
				}
				m := map[string]any{}
				for k, v := range peer {
					target, ok := fields[k]
					if !ok {
						return nil, fmt.Errorf("unmapped WireGuard peer field %q", k)
					}
					m[target] = v
				}
				mapped = append(mapped, m)
			}
			out["peers"] = mapped
			// Mihomo's base options also require a server/port for the first peer.
			first := mapped[0].(map[string]any)
			out["server"] = first["server"]
			out["port"] = first["port"]
		default:
			return nil, fmt.Errorf("unmapped WireGuard field %q", key)
		}
	}
	if len(node.Dependencies) == 1 {
		out["dialer-proxy"] = node.Dependencies[0]
	}
	return out, nil
}

func emptyScalar(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case int:
		return v == 0
	case float64:
		return v == 0
	}
	return false
}
