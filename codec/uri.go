package codec

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	uri "github.com/metacubex/mihomo/common/convert"
	mlog "github.com/metacubex/mihomo/log"
)

func init() {
	mlog.SetLevel(mlog.SILENT)
}

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
	aliases := map[string]string{
		"hy":         "hysteria",
		"hy2":        "hysteria2",
		"trojan-go":  "trojan",
		"mieru":      "mierus",
		"socks5+tls": "socks",
	}
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

func normalizeLegacyURIQuery(u *url.URL) error {
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("invalid node URI query")
	}
	changed := false
	for alias, canonical := range map[string]string{
		"servername":             "sni",
		"peer":                   "sni",
		"max_early_data":         "ed",
		"early_data_header_name": "eh",
	} {
		values, ok := q[alias]
		if !ok {
			continue
		}
		if len(values) != 1 {
			return errors.New("repeated URI alias")
		}
		if existing, ok := q[canonical]; ok && (len(existing) != 1 || existing[0] != values[0]) {
			return errors.New("conflicting URI aliases")
		}
		q.Set(canonical, values[0])
		q.Del(alias)
		changed = true
	}
	if u.Scheme == "hysteria2" && q.Has("security") && q.Get("security") != "tls" {
		return errors.New("Hysteria2 requires TLS")
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return nil
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
	config := map[string]any{
		"name":        u.Fragment,
		"type":        "wireguard",
		"server":      u.Hostname(),
		"port":        port,
		"private-key": private,
		"public-key":  public,
	}
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
		"wireguard": "privatekey publickey public-key address mtu reserved",
		"wg":        "privatekey publickey public-key address mtu reserved",
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

func decodeSSRURI(line string) (map[string]any, error) {
	decode := func(value string) (string, error) {
		for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
			if data, err := enc.DecodeString(value); err == nil {
				return string(data), nil
			}
		}
		return "", errors.New("invalid SSR Base64")
	}
	raw, err := decode(strings.TrimPrefix(line, "ssr://"))
	if err != nil {
		return nil, err
	}
	base, query, _ := strings.Cut(raw, "/?")
	parts := make([]string, 6)
	for i := 5; i > 0; i-- {
		index := strings.LastIndexByte(base, ':')
		if index < 0 {
			return nil, errors.New("invalid SSR fields")
		}
		parts[i] = base[index+1:]
		base = base[:index]
	}
	parts[0] = base
	port, err := strconv.Atoi(parts[1])
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid SSR port")
	}
	password, err := decode(parts[5])
	if err != nil {
		return nil, err
	}
	c := map[string]any{
		"type":     "ssr",
		"server":   parts[0],
		"port":     port,
		"protocol": parts[2],
		"cipher":   parts[3],
		"obfs":     parts[4],
		"password": password,
		"udp":      true,
	}
	if c["cipher"] == "none" {
		c["cipher"] = "dummy"
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return nil, err
	}
	for key, field := range map[string]string{"remarks": "name", "obfsparam": "obfs-param", "protoparam": "protocol-param"} {
		if value := q.Get(key); value != "" {
			text, err := decode(value)
			if err != nil {
				return nil, err
			}
			c[field] = text
		}
	}
	return c, nil
}

var uriPluginFields = map[string]string{
	"obfs":         "mode host",
	"v2ray-plugin": "mode host path tls mux skip-cert-verify",
	"gost-plugin":  "mode host path tls mux skip-cert-verify",
	"shadow-tls":   "host password version alpn skip-cert-verify",
}

func encodeSSPlugin(c map[string]any) (string, error) {
	plugin, _ := c["plugin"].(string)
	opts, _ := c["plugin-opts"].(map[string]any)
	fields, ok := uriPluginFields[plugin]
	if !ok {
		return "", fmt.Errorf("unsupported URI plugin %q", plugin)
	}
	if err := uriFields(opts, fields); err != nil {
		return "", err
	}
	if opts["skip-cert-verify"] == true {
		return "", errors.New("plugin URI cannot preserve insecure TLS")
	}
	parts := []string{plugin}
	if plugin == "obfs" {
		parts[0] = "obfs-local"
	}
	keys := make([]string, 0, len(opts))
	for key := range opts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := opts[key]
		if emptyScalar(value) {
			continue
		}
		param := key
		if plugin == "obfs" {
			if key == "mode" {
				param = "obfs"
			} else if key == "host" {
				param = "obfs-host"
			}
		}
		if key == "tls" {
			if value == true {
				parts = append(parts, "tls")
			}
			continue
		}
		if key == "mux" {
			if value == true {
				parts = append(parts, "mux=4")
			}
			continue
		}
		text := fmt.Sprint(value)
		if key == "alpn" {
			var err error
			text, err = uriStringList(value)
			if err != nil {
				return "", err
			}
		}
		if strings.ContainsAny(text, ";\r\n") {
			return "", errors.New("plugin URI cannot preserve separator in value")
		}
		parts = append(parts, param+"="+text)
	}
	return strings.Join(parts, ";"), nil
}

func decodeSSPluginURI(u *url.URL) (string, map[string]any, error) {
	value := u.Query().Get("plugin")
	if value == "" {
		return "", nil, nil
	}
	parts := strings.Split(value, ";")
	plugin := parts[0]
	if plugin == "obfs-local" || plugin == "simple-obfs" {
		plugin = "obfs"
	}
	fields, ok := uriPluginFields[plugin]
	if !ok {
		return "", nil, fmt.Errorf("unsupported URI plugin %q", plugin)
	}
	opts := map[string]any{}
	if plugin == "v2ray-plugin" || plugin == "gost-plugin" {
		opts["mux"] = false
	}
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		key, text, hasValue := strings.Cut(part, "=")
		if plugin == "obfs" {
			switch key {
			case "obfs":
				key = "mode"
			case "obfs-host":
				key = "host"
			default:
				return "", nil, fmt.Errorf("unsupported obfs parameter %q", key)
			}
		}
		if !strings.Contains(" "+fields+" ", " "+key+" ") {
			return "", nil, fmt.Errorf("URI plugin setting %q cannot be preserved", key)
		}
		switch key {
		case "skip-cert-verify":
			return "", nil, errors.New("plugin URI has no portable certificate verification setting")
		case "tls", "mux":
			enabled := true
			if hasValue {
				var err error
				if key == "mux" && text == "4" {
					enabled = true
				} else {
					enabled, err = strconv.ParseBool(text)
					if err != nil {
						return "", nil, errors.New("invalid plugin flag")
					}
				}
			}
			opts[key] = enabled
		case "version":
			n, err := strconv.Atoi(text)
			if err != nil {
				return "", nil, err
			}
			opts[key] = n
		case "alpn":
			opts[key] = strings.Split(text, ",")
		default:
			opts[key] = text
		}
	}
	return plugin, opts, nil
}

func validateSSPluginURI(u *url.URL) error {
	_, _, err := decodeSSPluginURI(u)
	return err
}

func normalizeURIConfig(u *url.URL, c map[string]any) error {
	q := u.Query()
	kind, _ := c["type"].(string)
	for _, parameter := range []string{"allowInsecure", "insecure"} {
		if q.Has(parameter) {
			enabled, err := strconv.ParseBool(q.Get(parameter))
			if err != nil {
				return errors.New("invalid URI TLS verification flag")
			}
			c["skip-cert-verify"] = enabled
			break
		}
	}
	if kind == "vless" && q.Has("packetEncoding") {
		delete(c, "xudp")
		delete(c, "packet-addr")
		switch q.Get("packetEncoding") {
		case "none":
			c["packet-encoding"] = "none"
		case "packet":
			c["packet-addr"] = true
			c["packet-encoding"] = "packetaddr"
		case "xudp":
			c["xudp"] = true
			c["packet-encoding"] = "xudp"
		default:
			return errors.New("unsupported packet encoding")
		}
	}
	if sni := q.Get("sni"); sni != "" {
		field := "sni"
		if kind == "vless" || kind == "vmess" {
			field = "servername"
		}
		c[field] = sni
	}
	if alpn := q.Get("alpn"); alpn != "" {
		c["alpn"] = strings.Split(alpn, ",")
	}
	if fp := q.Get("fp"); fp != "" {
		c["client-fingerprint"] = fp
	}
	if name := q.Get("remarks"); name != "" {
		c["name"] = name
	}
	if kind == "tuic" && q.Has("disable_sni") {
		enabled, err := strconv.ParseBool(q.Get("disable_sni"))
		if err != nil {
			return errors.New("invalid TUIC disable_sni flag")
		}
		c["disable-sni"] = enabled
	}
	if kind == "anytls" {
		if u.User != nil {
			if pass, hasPass := u.User.Password(); hasPass && u.User.Username() != pass {
				return errors.New("AnyTLS URI cannot preserve distinct username and password")
			}
		}
		username, _ := c["username"].(string)
		password, _ := c["password"].(string)
		if password == "" && username != "" {
			c["password"] = username
			password = username
		}
		if username != "" && username != password {
			return errors.New("AnyTLS URI cannot preserve distinct username and password")
		}
		delete(c, "username")
	}
	if kind == "ss" {
		plugin, opts, err := decodeSSPluginURI(u)
		if err != nil {
			return err
		}
		if plugin != "" {
			c["plugin"] = plugin
			c["plugin-opts"] = opts
		}
	}
	if kind == "hysteria" {
		if c["auth_str"] != nil {
			c["auth-str"] = c["auth_str"]
			delete(c, "auth_str")
		}
		if q.Get("auth_str") != "" {
			c["auth-str"] = q.Get("auth_str")
		}
		if q.Get("obfsParam") != "" {
			c["obfs"] = q.Get("obfsParam")
		}
		if q.Get("fastopen") == "1" || q.Get("fastopen") == "true" {
			c["fast-open"] = true
		}
	}
	if kind == "hysteria2" {
		if q.Get("mport") != "" {
			c["ports"] = q.Get("mport")
		} else if q.Get("ports") != "" {
			c["ports"] = q.Get("ports")
		}
		if q.Get("hop-interval") != "" {
			n, err := strconv.Atoi(q.Get("hop-interval"))
			if err != nil || n <= 0 {
				return errors.New("invalid hop interval")
			}
			c["hop-interval"] = n
		}
		if u.User != nil {
			auth := u.User.Username()
			if password, ok := u.User.Password(); ok {
				auth += ":" + password
			}
			c["password"] = auth
		}
	}
	if network, _ := c["network"].(string); network == "httpupgrade" {
		c["network"] = "ws"
		opts, _ := c["ws-opts"].(map[string]any)
		if opts == nil {
			opts = map[string]any{}
			c["ws-opts"] = opts
		}
		opts["v2ray-http-upgrade"] = true
	}
	if opts, ok := c["ws-opts"].(map[string]any); ok {
		if h, ok := opts["headers"].(map[string]any); ok {
			delete(h, "User-Agent")
		}
	}
	if kind == "vless" || kind == "trojan" {
		if err := decodeURITransport(q, c); err != nil {
			return err
		}
	}
	if kind == "vmess" {
		raw := strings.SplitN(u.String(), "://", 2)
		if len(raw) == 2 {
			data, err := base64.StdEncoding.DecodeString(raw[1])
			if err != nil {
				data, err = base64.RawURLEncoding.DecodeString(raw[1])
			}
			if err == nil {
				var v map[string]any
				if json.Unmarshal(data, &v) == nil {
					if fp, ok := v["fp"].(string); ok && fp != "" {
						c["client-fingerprint"] = fp
					}
					if text, ok := v["alpn"].(string); ok && text != "" {
						c["alpn"] = strings.Split(text, ",")
					}
					transport := url.Values{}
					for _, key := range []string{"host", "path", "mode", "extra", "method", "ed", "eh", "grpcUserAgent"} {
						if v[key] != nil {
							transport.Set(key, fmt.Sprint(v[key]))
						}
					}
					if v["net"] != nil {
						transport.Set("type", fmt.Sprint(v["net"]))
					}
					if v["type"] == "http" {
						transport.Set("headerType", "http")
					}
					if v["net"] == "grpc" {
						transport.Set("serviceName", transport.Get("path"))
					}
					if err := decodeURITransport(transport, c); err != nil {
						return err
					}
				}
			}
		}
	}
	if opts, ok := c["grpc-opts"].(map[string]any); ok && q.Get("grpcUserAgent") != "" {
		opts["grpc-user-agent"] = q.Get("grpcUserAgent")
	}
	if kind == "vless" || kind == "trojan" {
		if host := q.Get("host"); host != "" {
			switch c["network"] {
			case "http":
				o, _ := c["http-opts"].(map[string]any)
				if o == nil {
					o = map[string]any{}
					c["http-opts"] = o
				}
				o["headers"] = map[string]any{"Host": strings.Split(host, ",")}
			case "h2":
				o, _ := c["h2-opts"].(map[string]any)
				if o == nil {
					o = map[string]any{}
					c["h2-opts"] = o
				}
				o["host"] = strings.Split(host, ",")
			}
		}
	}
	if c["network"] == "xhttp" && q.Get("extra") != "" {
		var extra map[string]any
		if json.Unmarshal([]byte(q.Get("extra")), &extra) != nil {
			return errors.New("invalid XHTTP extra JSON")
		}
		opts, _ := c["xhttp-opts"].(map[string]any)
		if opts == nil {
			opts = map[string]any{}
			c["xhttp-opts"] = opts
		}
		if err := decodeXHTTPExtra(extra, opts, false); err != nil {
			return err
		}
	}
	return nil
}
