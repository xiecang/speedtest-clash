// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func encodeLegacyURI(node model.Node, c map[string]any) (string, error) {
	kind, _ := c["type"].(string)
	allowed := " name type server port udp password hidden "
	if kind == "ssr" {
		allowed += " cipher protocol obfs protocol-param obfs-param "
	} else {
		allowed += " username transport port-range multiplexing handshake-mode traffic-pattern "
	}
	for key, value := range c {
		if !emptyScalar(value) && !strings.Contains(allowed, " "+key+" ") {
			return "", fmt.Errorf("URI cannot preserve field %q", key)
		}
	}
	if c["udp"] == false {
		return "", errors.New("URI cannot preserve UDP disable")
	}
	value := func(key string) string {
		if v := c[key]; v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	if kind == "ssr" {
		for _, field := range []string{"port", "protocol", "cipher", "obfs"} {
			if strings.Contains(value(field), ":") {
				return "", fmt.Errorf("SSR URI converter cannot preserve colon in %q", field)
			}
		}
		b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		cipher := value("cipher")
		if cipher == "dummy" {
			cipher = "none"
		}
		q := url.Values{"remarks": {b64(node.Name)}}
		for field, param := range map[string]string{"obfs-param": "obfsparam", "protocol-param": "protoparam"} {
			if v := value(field); v != "" {
				q.Set(param, b64(v))
			}
		}
		raw := strings.Join([]string{value("server"), value("port"), value("protocol"), cipher, value("obfs"), b64(value("password"))}, ":") + "/?" + q.Encode()
		return "ssr://" + b64(raw), nil
	}
	port := value("port")
	if port == "" {
		port = value("port-range")
	}
	if port == "" {
		return "", errors.New("Mieru port is required")
	}
	u := url.URL{Scheme: "mierus", Host: value("server"), User: url.UserPassword(value("username"), value("password")), Fragment: node.Name}
	if strings.Contains(u.Host, ":") {
		u.Host = "[" + u.Host + "]"
	}
	q := url.Values{"port": {port}, "protocol": {value("transport")}}
	for _, field := range []string{"multiplexing", "handshake-mode", "traffic-pattern"} {
		if v := value(field); v != "" {
			q.Set(field, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
