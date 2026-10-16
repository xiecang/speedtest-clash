// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

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
	// The upstream parser invents a random WS User-Agent, which changes identities on every load.
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
				o["headers"] = map[string]any{"Host": strings.Split(host, ",")}
			case "h2":
				o, _ := c["h2-opts"].(map[string]any)
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

func decodeXHTTPExtra(extra, opts map[string]any, download bool) error {
	for key, value := range extra {
		field := ""
		for f, p := range xhttpFields {
			if key == p {
				field = f
				break
			}
		}
		if key == "noGRPCHeader" || key == "noGrpcHeader" {
			field = "no-grpc-header"
		}
		if key == "uplinkHTTPMethod" {
			field = "uplink-http-method"
		}
		if field != "" {
			opts[field] = value
			continue
		}
		switch key {
		case "path", "host", "headers":
			if !download {
				return errors.New("unsupported XHTTP extra transport field")
			}
			opts[key] = value
		case "xmux":
			xmux, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid XHTTP xmux")
			}
			rs := map[string]any{}
			for k, v := range xmux {
				found := false
				for f, p := range reuseFields {
					if k == p {
						rs[f] = v
						found = true
						break
					}
				}
				if !found {
					return errors.New("unsupported XHTTP xmux setting")
				}
			}
			opts["reuse-settings"] = rs
		case "extra":
			if !download {
				return errors.New("unsupported nested XHTTP extra")
			}
			e, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid XHTTP download extra")
			}
			if err := decodeXHTTPExtra(e, opts, false); err != nil {
				return err
			}
		case "downloadSettings":
			if download {
				return errors.New("nested XHTTP download settings")
			}
			ds, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid XHTTP download settings")
			}
			d := map[string]any{}
			for k, v := range ds {
				switch k {
				case "address":
					d["server"] = v
				case "port":
					d["port"] = v
				case "security":
					if v == "tls" || v == "reality" {
						d["tls"] = true
					} else if v != "none" && v != "" {
						return errors.New("unsupported XHTTP download security")
					}
				case "tlsSettings":
					tls, ok := v.(map[string]any)
					if !ok {
						return errors.New("invalid XHTTP TLS settings")
					}
					for tk, tv := range tls {
						switch tk {
						case "serverName":
							d["servername"] = tv
						case "fingerprint":
							d["client-fingerprint"] = tv
						case "alpn":
							d["alpn"] = tv
						case "allowInsecure":
							d["skip-cert-verify"] = tv
						default:
							return errors.New("unsupported XHTTP TLS setting")
						}
					}
				case "realitySettings":
					r, ok := v.(map[string]any)
					if !ok {
						return errors.New("invalid XHTTP Reality")
					}
					if err := uriFields(r, "publicKey shortId"); err != nil {
						return err
					}
					d["reality-opts"] = map[string]any{"public-key": r["publicKey"], "short-id": r["shortId"]}
				case "xhttpSettings":
					t, ok := v.(map[string]any)
					if !ok {
						return errors.New("invalid XHTTP download transport")
					}
					if err := decodeXHTTPExtra(t, d, true); err != nil {
						return err
					}
				default:
					return errors.New("unsupported XHTTP download field")
				}
			}
			opts["download-settings"] = d
		default:
			return errors.New("unsupported XHTTP extra field")
		}
	}
	return nil
}
