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

	"github.com/xiecang/node-quality/v2/internal/model"
)

func encodeURI(n model.Node) (string, error) {
	if len(n.Dependencies) > 0 {
		return "", errors.New("proxy chains cannot be represented as a single URI")
	}
	c, err := ClashConfig(n)
	if err != nil {
		return "", err
	}
	delete(c, "__type") // Legacy editor metadata is not a transport option.
	typeName, _ := c["type"].(string)
	if typeName == "ssr" || typeName == "mieru" {
		return encodeLegacyURI(n, c)
	}
	allowed := " name type server port hidden "
	tls := " tls sni servername alpn skip-cert-verify client-fingerprint fingerprint "
	transportFields := " network ws-opts grpc-opts http-opts h2-opts xhttp-opts "
	switch typeName {
	case "http", "socks5":
		allowed += " username password " + tls
	case "ss":
		allowed += " cipher password plugin plugin-opts client-fingerprint "
	case "vmess":
		allowed += " uuid cipher alterId " + tls + transportFields
	case "trojan":
		allowed += " password reality-opts " + tls + transportFields
	case "vless":
		allowed += " uuid flow encryption xudp packet-addr packet-encoding reality-opts " + tls + transportFields
	case "wireguard":
		allowed += " ip ipv6 public-key private-key mtu reserved "
	case "hysteria":
		allowed += " auth-str up down obfs protocol fast-open " + tls
	case "hysteria2":
		allowed += " password ports up down obfs obfs-password hop-interval " + tls
	case "tuic":
		allowed += " uuid password congestion-controller udp-relay-mode " + tls
	case "anytls":
		allowed += " password " + tls
	default:
		return "", fmt.Errorf("URI export unsupported for %s", typeName)
	}
	for field, value := range c {
		if !strings.Contains(allowed, " "+field+" ") && !emptyScalar(value) && field != "udp" {
			return "", fmt.Errorf("URI cannot preserve field %q", field)
		}
		if field == "udp" && value == false {
			return "", errors.New("URI cannot preserve explicit UDP disable")
		}
	}
	if typeName == "vmess" {
		if !emptyScalar(c["reality-opts"]) || !emptyScalar(c["fingerprint"]) {
			return "", errors.New("VMess JSON URI cannot preserve Reality or certificate fingerprint")
		}
		network, _ := c["network"].(string)
		if network == "" {
			network = "tcp"
		}
		data := map[string]any{"v": "2", "ps": n.Name, "add": c["server"], "port": c["port"], "id": c["uuid"], "aid": c["alterId"], "scy": c["cipher"], "net": network, "type": "none"}
		if data["aid"] == nil {
			data["aid"] = 0
		}
		if c["tls"] == true {
			data["tls"] = "tls"
		}
		if c["skip-cert-verify"] == true {
			return "", errors.New("vmess JSON URI cannot preserve insecure TLS setting")
		}
		if v := c["servername"]; v != nil {
			data["sni"] = v
		}
		transport, e := encodeURITransport(c)
		if e != nil {
			return "", e
		}
		for key, values := range transport {
			if len(values) > 0 {
				data[key] = values[0]
			}
		}
		if value := transport.Get("type"); value != "" {
			data["net"] = value
			delete(data, "type")
		}
		if value := transport.Get("headerType"); value != "" {
			data["type"] = value
			delete(data, "headerType")
		}
		if value := transport.Get("serviceName"); value != "" {
			data["path"] = value
			delete(data, "serviceName")
		}
		if c["alpn"] != nil {
			text, e := uriStringList(c["alpn"])
			if e != nil {
				return "", e
			}
			data["alpn"] = text
		}
		if c["client-fingerprint"] != nil {
			data["fp"] = c["client-fingerprint"]
		}
		content, err := json.Marshal(data)
		if err != nil {
			return "", err
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(content), nil
	}
	u := url.URL{Scheme: typeName, Host: netJoinHostPort(fmt.Sprint(c["server"]), fmt.Sprint(c["port"])), Fragment: n.Name}
	q := url.Values{}
	switch typeName {
	case "ss":
		u.User = url.User(base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprint(c["cipher"]) + ":" + fmt.Sprint(c["password"]))))
		if c["plugin"] != nil {
			plugin, e := encodeSSPlugin(c)
			if e != nil {
				return "", e
			}
			q.Set("plugin", plugin)
		}
	case "trojan", "hysteria2", "anytls":
		u.User = url.User(fmt.Sprint(c["password"]))
		if typeName == "hysteria2" {
			for field, param := range map[string]string{"ports": "mport", "up": "up", "down": "down", "obfs": "obfs", "obfs-password": "obfs-password", "hop-interval": "hop-interval"} {
				setURIQuery(q, c, field, param)
			}
		}
	case "hysteria":
		if value := c["auth-str"]; value != nil {
			q.Set("auth", fmt.Sprint(value))
		}
		for field, param := range map[string]string{"protocol": "protocol", "sni": "peer", "fast-open": "fastopen"} {
			setURIQuery(q, c, field, param)
		}
		if !emptyScalar(c["obfs"]) {
			q.Set("obfs", "xplus")
			setURIQuery(q, c, "obfs", "obfsParam")
		}
		for _, field := range []string{"up", "down"} {
			if value := c[field]; value != nil {
				q.Set(field, fmt.Sprint(value))
			}
		}
	case "tuic":
		u.User = url.UserPassword(fmt.Sprint(c["uuid"]), fmt.Sprint(c["password"]))
		if value := c["congestion-controller"]; value != nil {
			q.Set("congestion_control", fmt.Sprint(value))
		}
		if value := c["udp-relay-mode"]; value != nil {
			q.Set("udp_relay_mode", fmt.Sprint(value))
		}
	case "vless":
		u.User = url.User(fmt.Sprint(c["uuid"]))
		if c["tls"] == true {
			q.Set("security", "tls")
		}
		setURIQuery(q, c, "encryption", "encryption")
		if c["packet-addr"] == true {
			q.Set("packetEncoding", "packet")
		} else if c["packet-encoding"] == "packetaddr" {
			q.Set("packetEncoding", "packet")
		} else if c["packet-encoding"] == "none" {
			q.Set("packetEncoding", "none")
		} else if c["xudp"] == true || c["packet-encoding"] == "xudp" {
			q.Set("packetEncoding", "xudp")
		}
		if value := c["flow"]; value != nil {
			q.Set("flow", fmt.Sprint(value))
		}
	case "http", "socks5":
		if username, ok := c["username"].(string); ok {
			password, _ := c["password"].(string)
			u.User = url.UserPassword(username, password)
		}
		if c["tls"] == true {
			if typeName == "http" {
				u.Scheme = "https"
			} else {
				u.Scheme = "socks5+tls"
			}
		}
	case "wireguard":
		u.User = url.User(fmt.Sprint(c["private-key"]))
		q.Set("publickey", fmt.Sprint(c["public-key"]))
		var addresses []string
		for _, field := range []string{"ip", "ipv6"} {
			if value := c[field]; value != nil {
				addresses = append(addresses, fmt.Sprint(value))
			}
		}
		q.Set("address", strings.Join(addresses, ","))
		for _, field := range []string{"mtu"} {
			if value := c[field]; value != nil {
				q.Set(field, fmt.Sprint(value))
			}
		}
		if c["reserved"] != nil {
			var values []any
			raw, err := json.Marshal(c["reserved"])
			if err != nil {
				return "", err
			}
			if err = json.Unmarshal(raw, &values); err != nil {
				return "", fmt.Errorf("URI reserved must be an array: %w", err)
			}
			if len(values) != 0 && len(values) != 3 {
				return "", errors.New("URI reserved must have three bytes")
			}
			var encoded []string
			for _, value := range values {
				number, ok := value.(float64)
				if !ok || number < 0 || number > 255 || number != float64(int(number)) {
					return "", errors.New("URI reserved values must be bytes")
				}
				encoded = append(encoded, strconv.Itoa(int(number)))
			}
			q.Set("reserved", strings.Join(encoded, ","))
		}
		if c["peers"] != nil || c["pre-shared-key"] != nil {
			return "", errors.New("WireGuard URI cannot preserve peers or pre-shared key")
		}
	default:
		return "", fmt.Errorf("URI export unsupported for %s", typeName)
	}
	for _, field := range []string{"servername", "sni"} {
		if value := c[field]; value != nil {
			q.Set("sni", fmt.Sprint(value))
		}
	}
	if c["alpn"] != nil {
		text, e := uriStringList(c["alpn"])
		if e != nil {
			return "", e
		}
		q.Set("alpn", text)
	}
	if !emptyScalar(c["fingerprint"]) {
		param := "pcs"
		if typeName == "hysteria2" {
			param = "pinSHA256"
		}
		if typeName == "anytls" {
			param = "hpkp"
		}
		if typeName != "vless" && typeName != "trojan" && typeName != "hysteria2" && typeName != "anytls" {
			return "", errors.New("URI cannot preserve certificate fingerprint")
		}
		setURIQuery(q, c, "fingerprint", param)
	}
	if c["skip-cert-verify"] == true {
		q.Set("insecure", "true")
		q.Set("allowInsecure", "true")
	}
	if value := c["client-fingerprint"]; value != nil {
		q.Set("fp", fmt.Sprint(value))
	}
	if opts, ok := c["reality-opts"].(map[string]any); ok {
		q.Set("security", "reality")
		if err := uriFields(opts, "public-key short-id"); err != nil {
			return "", err
		}
		setURIQuery(q, opts, "public-key", "pbk")
		setURIQuery(q, opts, "short-id", "sid")
	}
	transport, e := encodeURITransport(c)
	if e != nil {
		return "", e
	}
	for key, values := range transport {
		q[key] = values
	}

	u.RawQuery = q.Encode()
	return u.String(), nil
}

func netJoinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + strings.Trim(host, "[]") + "]:" + port
	}
	return host + ":" + port
}

func enabledMux(value any) bool {
	config, ok := value.(map[string]any)
	return ok && config["enabled"] == true
}
