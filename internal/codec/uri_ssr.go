// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

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
	c := map[string]any{"type": "ssr", "server": parts[0], "port": port, "protocol": parts[2], "cipher": parts[3], "obfs": parts[4], "password": password, "udp": true}
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
