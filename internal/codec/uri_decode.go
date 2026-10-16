// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	uri "github.com/metacubex/mihomo/common/convert"
)

func DecodeURI(line string) (map[string]any, error) {
	if scheme, rest, ok := strings.Cut(line, "://"); ok {
		line = strings.ToLower(scheme) + "://" + rest
	}
	if strings.HasPrefix(line, "ssr://") {
		return decodeSSRURI(line)
	}
	u, err := url.Parse(line)
	if err != nil {
		return nil, errors.New("invalid node URI")
	}
	aliases := map[string]string{"hy": "hysteria", "hy2": "hysteria2", "trojan-go": "trojan", "mieru": "mierus", "socks5+tls": "socks"}
	originalScheme := u.Scheme
	if alias, ok := aliases[u.Scheme]; ok {
		u.Scheme = alias
		line = u.String()
	}
	if err := normalizeLegacyURIQuery(u); err != nil {
		return nil, err
	}
	line = u.String()
	if err := validateURIQuery(u); err != nil {
		return nil, err
	}
	if u.Scheme == "wireguard" || u.Scheme == "wg" {
		return decodeWireGuardURI(u)
	}
	if u.Scheme == "ss" {
		if err := validateSSPluginURI(u); err != nil {
			return nil, err
		}
	}
	configs, err := uri.ConvertsV2Ray([]byte(base64.StdEncoding.EncodeToString([]byte(line))))
	if err != nil || len(configs) != 1 {
		return nil, fmt.Errorf("unsupported or invalid URI protocol %q", originalScheme)
	}
	config := configs[0]
	if u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "socks" || u.Scheme == "socks5" || u.Scheme == "socks5h" {
		config["skip-cert-verify"] = false
		if u.User != nil {
			config["username"] = u.User.Username()
			if password, ok := u.User.Password(); ok {
				config["password"] = password
			} else {
				delete(config, "password")
			}
		}
	}
	if u.Scheme == "https" || originalScheme == "socks5+tls" {
		query := u.Query()
		insecure := query.Get("allowInsecure")
		if insecure == "" {
			insecure = query.Get("insecure")
		}
		config["skip-cert-verify"] = insecure == "1" || insecure == "true"
		config["tls"] = true
	}
	if err := normalizeURIConfig(u, config); err != nil {
		return nil, err
	}
	return config, nil
}

func decodeWireGuardURI(u *url.URL) (map[string]any, error) {
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid WireGuard port")
	}
	q := u.Query()
	private := q.Get("privatekey")
	if private == "" && u.User != nil {
		private = u.User.Username()
	}
	public := q.Get("publickey")
	if public == "" {
		public = q.Get("public-key")
	}
	if private == "" || public == "" {
		return nil, errors.New("WireGuard keys are required")
	}
	config := map[string]any{"name": u.Fragment, "type": "wireguard", "server": u.Hostname(), "port": port, "private-key": private, "public-key": public}
	for _, address := range strings.Split(q.Get("address"), ",") {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		prefix, e := netip.ParsePrefix(address)
		if e != nil {
			ip, e := netip.ParseAddr(address)
			if e != nil {
				return nil, errors.New("invalid WireGuard address")
			}
			prefix = netip.PrefixFrom(ip, ip.BitLen())
		}
		field := "ip"
		if prefix.Addr().Is6() {
			field = "ipv6"
		}
		if _, exists := config[field]; exists {
			return nil, errors.New("multiple WireGuard addresses of one family cannot be represented")
		}
		config[field] = address
	}
	if value := q.Get("mtu"); value != "" {
		mtu, e := strconv.Atoi(value)
		if e != nil || mtu < 1 || mtu > 65535 {
			return nil, errors.New("invalid WireGuard MTU")
		}
		config["mtu"] = mtu
	}
	if value := q.Get("reserved"); value != "" {
		var reserved []int
		for _, part := range strings.Split(value, ",") {
			n, e := strconv.Atoi(part)
			if e != nil || n < 0 || n > 255 {
				return nil, errors.New("invalid WireGuard reserved bytes")
			}
			reserved = append(reserved, n)
		}
		if len(reserved) != 3 {
			return nil, errors.New("WireGuard reserved must have three bytes")
		}
		config["reserved"] = reserved
	}
	return config, nil
}

// Unknown connection parameters must be diagnosed instead of silently discarded.
func validateURIQuery(u *url.URL) error {
	common := " remarks sni alpn fp insecure allowInsecure "
	fields := map[string]string{
		"http": "", "https": "", "socks": "", "socks5": "", "socks5h": "",
		"ss":        "plugin uot udp-over-tcp",
		"vless":     "encryption flow packetEncoding security pbk sid support-x25519mlkem768 pcs type headerType path host serviceName grpcUserAgent method ed eh mode extra",
		"trojan":    "security pbk sid pcs type headerType path host serviceName grpcUserAgent method ed eh mode extra",
		"hysteria":  "peer auth auth_str obfs obfsParam protocol up down upmbps downmbps fastopen",
		"hysteria2": "auth obfs obfs-password mport ports up down hop-interval pinSHA256 security",
		"tuic":      "congestion_control udp_relay_mode disable_sni",
		"anytls":    "hpkp",
		"mierus":    "port protocol multiplexing handshake-mode traffic-pattern profile",
		"wireguard": "privatekey publickey public-key address mtu reserved", "wg": "privatekey publickey public-key address mtu reserved",
	}[u.Scheme]
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("invalid node URI query")
	}
	allowed := strings.Fields(common + " " + fields)
	for key, values := range query {
		if !slices.Contains(allowed, key) || len(values) != 1 {
			return fmt.Errorf("unsupported or repeated URI parameter %q", key)
		}
	}
	return nil
}
