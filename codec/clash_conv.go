package codec

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

var singBoxToClashProtocols = map[string]string{
	"direct":      "direct",
	"http":        "http",
	"socks":       "socks5",
	"shadowsocks": "ss",
	"vmess":       "vmess",
	"vless":       "vless",
	"trojan":      "trojan",
	"hysteria":    "hysteria",
	"hysteria2":   "hysteria2",
	"tuic":        "tuic",
	"anytls":      "anytls",
	"snell":       "snell",
	"ssh":         "ssh",
}

var singBoxScalarFieldMapping = map[string]string{
	"server":                 "server",
	"server_port":            "port",
	"username":               "username",
	"user":                   "username",
	"password":               "password",
	"psk":                    "psk",
	"reuse":                  "reuse",
	"uuid":                   "uuid",
	"method":                 "cipher",
	"security":               "cipher",
	"alter_id":               "alterId",
	"global_padding":         "global-padding",
	"authenticated_length":   "authenticated-length",
	"packet_encoding":        "packet-encoding",
	"flow":                   "flow",
	"up":                     "up",
	"down":                   "down",
	"up_mbps":                "up",
	"down_mbps":              "down",
	"auth_str":               "auth-str",
	"recv_window":            "recv-window",
	"recv_window_conn":       "recv-window-conn",
	"disable_mtu_discovery":  "disable_mtu_discovery",
	"congestion_control":     "congestion-controller",
	"udp_relay_mode":         "udp-relay-mode",
	"udp_over_stream":        "udp-over-stream",
	"zero_rtt_handshake":     "reduce-rtt",
	"min_idle_session":       "min-idle-session",
	"private_key_passphrase": "private-key-passphrase",
	"host_key":               "host-key",
	"host_key_algorithms":    "host-key-algorithms",
	"client_version":         "client-version",
	"tcp_fast_open":          "tfo",
	"tcp_multi_path":         "mptcp",
}

// ClashConfig converts a Node into a Clash/Mihomo proxy map, failing if any field has no lossless mapping.
func ClashConfig(node Node) (map[string]any, error) {
	if node.Format == ClashYAML {
		if err := validateWireGuardDNS(node.Config); err != nil {
			return nil, err
		}
		return CloneMap(node.Config), nil
	}
	if node.Format != SingBoxJSON {
		return nil, fmt.Errorf("unsupported input format %s", node.Format)
	}
	if node.Config["type"] == "wireguard" {
		return wireguardSingBoxToClash(node)
	}
	typeName, _ := node.Config["type"].(string)
	targetProto, ok := singBoxToClashProtocols[typeName]
	if !ok {
		return nil, fmt.Errorf("sing-box protocol %q has no lossless Mihomo mapping", typeName)
	}
	result := map[string]any{
		"name": node.Name,
		"type": targetProto,
	}

	var obfsMode, obfsHost string
	for key, value := range node.Config {
		switch key {
		case "type", "tag", "detour":
			continue
		case "version":
			if typeName != "snell" {
				return nil, fmt.Errorf("unmapped native field %s", key)
			}
			ver, ok := parseIntValue(value)
			if !ok || ver != 4 {
				return nil, fmt.Errorf("unsupported Snell version %v", value)
			}
			result["version"] = ver
		case "obfs_mode":
			if typeName != "snell" {
				return nil, fmt.Errorf("unmapped native field %s", key)
			}
			s, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid Snell obfs_mode")
			}
			obfsMode = s
		case "obfs_host":
			if typeName != "snell" {
				return nil, fmt.Errorf("unmapped native field %s", key)
			}
			s, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid Snell obfs_host")
			}
			obfsHost = s
		case "private_key":
			if typeName != "ssh" {
				return nil, fmt.Errorf("unmapped native field %s", key)
			}
			pk, err := singleStringValue(value, "private_key")
			if err != nil {
				return nil, err
			}
			result["private-key"] = pk
		case "network":
			netStr, ok := value.(string)
			if !ok {
				list, err := strSliceOpt(node.Config, "network")
				if err != nil {
					return nil, err
				}
				netStr = strings.Join(list, ",")
			}
			switch strings.ToLower(netStr) {
			case "":
			case "tcp":
				result["udp"] = false
			case "tcp,udp", "udp,tcp":
				result["udp"] = true
			default:
				return nil, fmt.Errorf("unmapped network list %q", netStr)
			}
		case "heartbeat":
			ms, err := parseDurationMillis(value, "heartbeat")
			if err != nil {
				return nil, err
			}
			result["heartbeat-interval"] = ms
		case "hop_interval":
			sec, err := parseDurationSeconds(value, "hop_interval")
			if err != nil {
				return nil, err
			}
			result["hop-interval"] = sec
		case "idle_session_check_interval", "idle_session_timeout":
			sec, err := parseDurationSeconds(value, key)
			if err != nil {
				return nil, err
			}
			result[strings.ReplaceAll(key, "_", "-")] = sec
		case "server_ports":
			ports, err := convertSingBoxServerPortsToClash(node.Config)
			if err != nil {
				return nil, err
			}
			result["ports"] = ports
		case "obfs":
			if typeName == "hysteria" {
				s, ok := value.(string)
				if !ok {
					return nil, errors.New("invalid Hysteria obfs")
				}
				result["obfs"] = s
				continue
			}
			if typeName == "hysteria2" {
				om, ok := value.(map[string]any)
				if !ok {
					return nil, errors.New("invalid Hysteria2 obfs")
				}
				if err := checkSingBoxMapKeys(om, "type password", "obfs"); err != nil {
					return nil, err
				}
				oType, _ := om["type"].(string)
				oPass, _ := om["password"].(string)
				if (oType != "") != (oPass != "") {
					return nil, errors.New("Hysteria2 obfs requires both type and password")
				}
				result["obfs"] = oType
				result["obfs-password"] = oPass
				continue
			}
			return nil, fmt.Errorf("unmapped obfs on %s", typeName)
		case "udp_over_tcp":
			if b, ok := value.(bool); ok {
				if b {
					result["udp-over-tcp"] = true
				}
				continue
			}
			um, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("invalid udp_over_tcp")
			}
			if err := checkSingBoxMapKeys(um, "enabled version", "udp_over_tcp"); err != nil {
				return nil, err
			}
			if um["enabled"] == true {
				result["udp-over-tcp"] = true
				if ver, ok := parseIntValue(um["version"]); ok && ver != 0 {
					result["udp-over-tcp-version"] = ver
				}
			}
		case "plugin", "plugin_opts":
			// Handled together after the loop for shadowsocks.
			if typeName != "shadowsocks" {
				return nil, fmt.Errorf("unmapped native field %s", key)
			}
		case "tls":
			if err := applySingBoxTLSToClash(value, typeName, result); err != nil {
				return nil, err
			}
		case "transport":
			if err := applySingBoxTransportToClash(value, result); err != nil {
				return nil, err
			}
		case "multiplex":
			mux, err := convertSingBoxMultiplexToClash(value)
			if err != nil {
				return nil, err
			}
			if mux != nil {
				result["smux"] = mux
			}
		default:
			target, ok := singBoxScalarFieldMapping[key]
			if !ok {
				return nil, fmt.Errorf("unmapped native field %s", key)
			}
			if (key == "up_mbps" || key == "down_mbps") && value != nil {
				result[target] = fmt.Sprint(value)
			} else {
				result[target] = cloneValue(value)
			}
		}
	}

	if obfsMode != "" || obfsHost != "" {
		opts := map[string]any{}
		if obfsMode != "" {
			opts["mode"] = obfsMode
		}
		if obfsHost != "" {
			opts["host"] = obfsHost
		}
		result["obfs-opts"] = opts
	}
	if typeName == "shadowsocks" && (node.Config["plugin"] != nil || node.Config["plugin_opts"] != nil) {
		if err := applySingBoxSSPluginToClash(node.Config, result); err != nil {
			return nil, err
		}
	}
	if len(node.Dependencies) == 1 {
		result["dialer-proxy"] = node.Dependencies[0]
	}
	return result, nil
}

func applySingBoxTLSToClash(raw any, typeName string, result map[string]any) error {
	tlsMap, ok := raw.(map[string]any)
	if !ok {
		return errors.New("invalid native TLS")
	}
	tlsFields := map[string]string{
		"enabled":     "tls",
		"server_name": "servername",
		"insecure":    "skip-cert-verify",
		"alpn":        "alpn",
		"disable_sni": "disable-sni",
	}
	for field, v := range tlsMap {
		switch field {
		case "utls":
			m, ok := v.(map[string]any)
			if !ok {
				return errors.New("invalid uTLS")
			}
			if m["enabled"] != true {
				continue
			}
			if err := checkSingBoxMapKeys(m, "enabled fingerprint", "tls.utls"); err != nil {
				return err
			}
			if fp, _ := m["fingerprint"].(string); fp != "" {
				result["client-fingerprint"] = fp
			}
		case "reality":
			m, ok := v.(map[string]any)
			if !ok {
				return errors.New("invalid Reality")
			}
			if m["enabled"] != true {
				continue
			}
			if err := checkSingBoxMapKeys(m, "enabled public_key short_id", "tls.reality"); err != nil {
				return err
			}
			out := map[string]any{}
			for k, rv := range m {
				if k != "enabled" {
					out[strings.ReplaceAll(k, "_", "-")] = rv
				}
			}
			result["reality-opts"] = out
		case "ech":
			m, ok := v.(map[string]any)
			if !ok {
				return errors.New("invalid ECH")
			}
			if m["enabled"] != true {
				continue
			}
			if err := checkSingBoxMapKeys(m, "enabled config query_server_name", "tls.ech"); err != nil {
				return err
			}
			out := map[string]any{"enable": true}
			if m["config"] != nil {
				cfg, err := singleStringValue(m["config"], "tls.ech.config")
				if err != nil {
					return err
				}
				if cfg != "" {
					out["config"] = cfg
				}
			}
			if qsn, _ := m["query_server_name"].(string); qsn != "" {
				out["query-server-name"] = qsn
			}
			result["ech-opts"] = out
		case "certificate":
			if typeName != "hysteria" {
				return errors.New("unmapped TLS field certificate")
			}
			cert, err := singleStringValue(v, "tls.certificate")
			if err != nil {
				return err
			}
			result["ca-str"] = cert
		default:
			target, ok := tlsFields[field]
			if !ok {
				return fmt.Errorf("unmapped TLS field %s", field)
			}
			result[target] = cloneValue(v)
		}
	}
	return nil
}

func applySingBoxTransportToClash(raw any, result map[string]any) error {
	transport, ok := raw.(map[string]any)
	if !ok {
		return errors.New("invalid transport")
	}
	kind, _ := transport["type"].(string)
	switch kind {
	case "ws":
		result["network"] = "ws"
		opts := map[string]any{}
		for k, v := range transport {
			if k == "type" {
				continue
			}
			mapped := map[string]string{
				"path":                   "path",
				"headers":                "headers",
				"max_early_data":         "max-early-data",
				"early_data_header_name": "early-data-header-name",
			}[k]
			if mapped == "" {
				return fmt.Errorf("unmapped WS field %s", k)
			}
			if k == "headers" {
				flat, err := flattenSingleValueHeaders(v)
				if err != nil {
					return err
				}
				opts[mapped] = flat
			} else {
				opts[mapped] = cloneValue(v)
			}
		}
		result["ws-opts"] = opts
	case "httpupgrade":
		if err := checkSingBoxMapKeys(transport, "type host path headers", "transport"); err != nil {
			return err
		}
		result["network"] = "ws"
		opts := map[string]any{"v2ray-http-upgrade": true}
		if path, _ := transport["path"].(string); path != "" {
			opts["path"] = path
		}
		headers := map[string]any{}
		if transport["headers"] != nil {
			flat, err := flattenSingleValueHeaders(transport["headers"])
			if err != nil {
				return err
			}
			headers = flat
		}
		if host, _ := transport["host"].(string); host != "" && headers["Host"] == nil {
			headers["Host"] = host
		}
		if len(headers) > 0 {
			opts["headers"] = headers
		}
		result["ws-opts"] = opts
	case "grpc":
		if err := checkSingBoxMapKeys(transport, "type service_name", "transport"); err != nil {
			return err
		}
		result["network"] = "grpc"
		opts := map[string]any{}
		if svc, _ := transport["service_name"].(string); svc != "" {
			opts["grpc-service-name"] = svc
		}
		result["grpc-opts"] = opts
	case "http":
		if err := checkSingBoxMapKeys(transport, "type host path method headers", "transport"); err != nil {
			return err
		}
		hasMethod := !emptyScalar(transport["method"])
		hasHeaders := !emptyScalar(transport["headers"])
		if result["tls"] == true && !hasMethod && !hasHeaders {
			result["network"] = "h2"
			opts := map[string]any{}
			if path, _ := transport["path"].(string); path != "" {
				opts["path"] = path
			}
			if hosts, err := strSliceOpt(transport, "host"); err != nil {
				return err
			} else if len(hosts) > 0 {
				opts["host"] = hosts
			}
			result["h2-opts"] = opts
			return nil
		}
		result["network"] = "http"
		opts := map[string]any{}
		if path, _ := transport["path"].(string); path != "" {
			opts["path"] = []string{path}
		}
		if method, _ := transport["method"].(string); method != "" {
			opts["method"] = method
		}
		headers, err := headerMapOpt(transport["headers"], true)
		if err != nil {
			return err
		}
		if hosts, err := strSliceOpt(transport, "host"); err != nil {
			return err
		} else if len(hosts) > 0 {
			if headers == nil {
				headers = map[string][]string{}
			}
			if len(headers["Host"]) == 0 {
				headers["Host"] = hosts
			}
		}
		if len(headers) > 0 {
			opts["headers"] = headers
		}
		result["http-opts"] = opts
	default:
		return fmt.Errorf("unmapped transport %s", kind)
	}
	return nil
}

func convertSingBoxMultiplexToClash(raw any) (map[string]any, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("invalid multiplex")
	}
	if err := checkSingBoxMapKeys(m, "enabled protocol max_connections min_streams max_streams padding brutal", "multiplex"); err != nil {
		return nil, err
	}
	if m["enabled"] != true {
		return nil, nil
	}
	out := map[string]any{"enabled": true}
	if proto, _ := m["protocol"].(string); proto != "" {
		out["protocol"] = proto
	}
	if v, ok := parseIntValue(m["max_connections"]); ok && v > 0 {
		out["max-connections"] = v
	}
	if v, ok := parseIntValue(m["min_streams"]); ok && v > 0 {
		out["min-streams"] = v
	}
	if v, ok := parseIntValue(m["max_streams"]); ok && v > 0 {
		out["max-streams"] = v
	}
	if m["padding"] == true {
		out["padding"] = true
	}
	if m["brutal"] != nil {
		bm, ok := m["brutal"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid multiplex.brutal")
		}
		if err := checkSingBoxMapKeys(bm, "enabled up_mbps down_mbps", "multiplex.brutal"); err != nil {
			return nil, err
		}
		if bm["enabled"] == true {
			brutal := map[string]any{"enabled": true}
			if up, ok := parseIntValue(bm["up_mbps"]); ok && up > 0 {
				brutal["up"] = fmt.Sprintf("%d Mbps", up)
			}
			if down, ok := parseIntValue(bm["down_mbps"]); ok && down > 0 {
				brutal["down"] = fmt.Sprintf("%d Mbps", down)
			}
			out["brutal-opts"] = brutal
		}
	}
	return out, nil
}

func applySingBoxSSPluginToClash(config, result map[string]any) error {
	plugin, _ := config["plugin"].(string)
	optsStr, _ := config["plugin_opts"].(string)
	parsed := parseEscapedPluginOpts(optsStr)
	switch plugin {
	case "obfs-local", "simple-obfs":
		result["plugin"] = "obfs"
		opts := map[string]any{}
		for k, v := range parsed {
			switch k {
			case "obfs":
				opts["mode"] = v
			case "obfs-host":
				opts["host"] = v
			default:
				return fmt.Errorf("unmapped obfs-local option %q", k)
			}
		}
		result["plugin-opts"] = opts
	case "v2ray-plugin":
		result["plugin"] = "v2ray-plugin"
		opts := map[string]any{}
		for k, v := range parsed {
			switch k {
			case "tls":
				opts["tls"] = v == "" || v == "true" || v == "1"
			case "mux":
				opts["mux"] = v != "0" && v != "false"
			case "host", "path", "mode":
				opts[k] = v
			default:
				return fmt.Errorf("unmapped v2ray-plugin option %q", k)
			}
		}
		result["plugin-opts"] = opts
	default:
		return fmt.Errorf("unsupported sing-box shadowsocks plugin %q", plugin)
	}
	return nil
}

func parseEscapedPluginOpts(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	var tokens []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			cur.WriteByte(s[i])
			continue
		}
		if s[i] == ';' {
			tokens = append(tokens, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	tokens = append(tokens, cur.String())
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		k, v, hasEq := strings.Cut(tok, "=")
		if !hasEq {
			out[k] = ""
		} else {
			out[k] = v
		}
	}
	return out
}

func wireguardSingBoxToClash(node Node) (map[string]any, error) {
	out := map[string]any{"name": node.Name, "type": "wireguard"}
	for key, value := range node.Config {
		switch key {
		case "type", "tag", "detour":
		case "system":
			if value == true {
				return nil, errors.New("system WireGuard cannot be represented safely")
			}
		case "private_key":
			out["private-key"] = value
		case "mtu":
			out["mtu"] = value
		case "address":
			addresses, ok := value.([]any)
			if !ok {
				if list, okStr := value.([]string); okStr {
					addresses = make([]any, len(list))
					for i, item := range list {
						addresses[i] = item
					}
				} else if text, okOne := value.(string); okOne {
					addresses = []any{text}
				}
			}
			for _, raw := range addresses {
				text, ok := raw.(string)
				if !ok {
					return nil, errors.New("invalid WireGuard address")
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
					return nil, errors.New("multiple WireGuard addresses cannot be mapped")
				}
				out[field] = prefix.String()
			}
		case "peers":
			peers, ok := value.([]any)
			if !ok || len(peers) == 0 {
				return nil, errors.New("WireGuard peers required")
			}
			mapped := make([]any, 0, len(peers))
			fields := map[string]string{
				"address":        "server",
				"port":           "port",
				"public_key":     "public-key",
				"pre_shared_key": "pre-shared-key",
				"allowed_ips":    "allowed-ips",
				"reserved":       "reserved",
			}
			for _, raw := range peers {
				peer, ok := raw.(map[string]any)
				if !ok {
					return nil, errors.New("invalid WireGuard peer")
				}
				m := map[string]any{}
				for k, v := range peer {
					target, ok := fields[k]
					if !ok {
						return nil, fmt.Errorf("unmapped WireGuard peer field %q", k)
					}
					m[target] = cloneValue(v)
				}
				mapped = append(mapped, m)
			}
			out["peers"] = mapped
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

func foldShadowTLSHelperToClash(ssNode, helperNode Node, displayName string, names map[string]string) (map[string]any, error) {
	if err := checkSingBoxMapKeys(ssNode.Config, "type tag method password udp_over_tcp multiplex network detour", "shadowsocks"); err != nil {
		return nil, err
	}
	if err := checkSingBoxMapKeys(helperNode.Config, "type tag server server_port version password tls tcp_fast_open tcp_multi_path detour", "shadowtls"); err != nil {
		return nil, err
	}
	out := map[string]any{
		"name": displayName,
		"type": "ss",
	}
	if srv := helperNode.Config["server"]; srv != nil {
		out["server"] = srv
	}
	if port := helperNode.Config["server_port"]; port != nil {
		out["port"] = port
	}
	if method := ssNode.Config["method"]; method != nil {
		out["cipher"] = method
	}
	if pass := ssNode.Config["password"]; pass != nil {
		out["password"] = pass
	}
	if ssNode.Config["network"] == "tcp" {
		out["udp"] = false
	}
	if ssNode.Config["udp_over_tcp"] != nil {
		tmp := map[string]any{}
		if uot, ok := ssNode.Config["udp_over_tcp"].(map[string]any); ok {
			if uot["enabled"] == true {
				out["udp-over-tcp"] = true
				if v, ok := parseIntValue(uot["version"]); ok && v > 0 {
					out["udp-over-tcp-version"] = v
				}
			}
		} else if ssNode.Config["udp_over_tcp"] == true {
			out["udp-over-tcp"] = true
		}
		_ = tmp
	}
	if ssNode.Config["multiplex"] != nil {
		mux, err := convertSingBoxMultiplexToClash(ssNode.Config["multiplex"])
		if err != nil {
			return nil, err
		}
		if mux != nil {
			out["smux"] = mux
		}
	}
	if helperNode.Config["tcp_fast_open"] == true {
		out["tfo"] = true
	}
	if helperNode.Config["tcp_multi_path"] == true {
		out["mptcp"] = true
	}
	pluginOpts := map[string]any{}
	if ver, ok := parseIntValue(helperNode.Config["version"]); ok && ver > 0 {
		pluginOpts["version"] = ver
	}
	if pass, _ := helperNode.Config["password"].(string); pass != "" {
		pluginOpts["password"] = pass
	}
	if tlsMap, ok := helperNode.Config["tls"].(map[string]any); ok {
		if err := checkSingBoxMapKeys(tlsMap, "enabled server_name alpn insecure utls", "shadowtls.tls"); err != nil {
			return nil, err
		}
		if sni, _ := tlsMap["server_name"].(string); sni != "" {
			pluginOpts["host"] = sni
		}
		if alpn := tlsMap["alpn"]; alpn != nil {
			pluginOpts["alpn"] = cloneValue(alpn)
		}
		if tlsMap["insecure"] == true {
			pluginOpts["skip-cert-verify"] = true
		}
		if utlsMap, ok := tlsMap["utls"].(map[string]any); ok && utlsMap["enabled"] == true {
			if err := checkSingBoxMapKeys(utlsMap, "enabled fingerprint", "shadowtls.tls.utls"); err != nil {
				return nil, err
			}
			if fp, _ := utlsMap["fingerprint"].(string); fp != "" {
				out["client-fingerprint"] = fp
			}
		}
	}
	out["plugin"] = "shadow-tls"
	out["plugin-opts"] = pluginOpts
	if ssNode.Hidden {
		out["hidden"] = true
	}
	if len(helperNode.Dependencies) == 1 {
		out["dialer-proxy"] = names[helperNode.Dependencies[0]]
	}
	return out, nil
}

func checkSingBoxMapKeys(m map[string]any, allowed, scope string) error {
	for k, v := range m {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") && !emptyScalar(v) {
			return fmt.Errorf("unmapped %s field %s", scope, k)
		}
	}
	return nil
}

func singleStringValue(raw any, field string) (string, error) {
	switch v := raw.(type) {
	case string:
		return v, nil
	case []string:
		if len(v) == 0 {
			return "", nil
		}
		if len(v) == 1 {
			return v[0], nil
		}
		return "", fmt.Errorf("multiple values in %s cannot be mapped", field)
	case []any:
		if len(v) == 0 {
			return "", nil
		}
		if len(v) == 1 {
			if s, ok := v[0].(string); ok {
				return s, nil
			}
		}
		return "", fmt.Errorf("invalid or multiple values in %s", field)
	default:
		return "", fmt.Errorf("invalid string field %s", field)
	}
}

func parseDurationMillis(raw any, field string) (int, error) {
	if v, ok := parseIntValue(raw); ok {
		return v, nil
	}
	s, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("invalid duration field %s", field)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q in %s: %w", s, field, err)
	}
	return int(d / time.Millisecond), nil
}

func parseDurationSeconds(raw any, field string) (int, error) {
	if v, ok := parseIntValue(raw); ok {
		return v, nil
	}
	s, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("invalid duration field %s", field)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q in %s: %w", s, field, err)
	}
	if d%time.Second != 0 {
		return 0, fmt.Errorf("sub-second duration %q in %s cannot be represented", s, field)
	}
	return int(d / time.Second), nil
}

func convertSingBoxServerPortsToClash(config map[string]any) (string, error) {
	list, err := strSliceOpt(config, "server_ports")
	if err != nil || len(list) == 0 {
		return "", errors.New("invalid server_ports")
	}
	parts := make([]string, 0, len(list))
	for _, entry := range list {
		start, end, ok := strings.Cut(entry, ":")
		if !ok {
			parts = append(parts, entry)
			continue
		}
		if start == end {
			parts = append(parts, start)
		} else {
			parts = append(parts, start+"-"+end)
		}
	}
	return strings.Join(parts, ","), nil
}

func flattenSingleValueHeaders(raw any) (map[string]any, error) {
	m, err := headerMapOpt(raw, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(m))
	for k, vals := range m {
		if len(vals) > 1 {
			return nil, fmt.Errorf("multiple values for WS header %q cannot be mapped", k)
		}
		if len(vals) == 1 {
			out[k] = vals[0]
		}
	}
	return out, nil
}
