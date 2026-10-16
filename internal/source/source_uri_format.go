// SPDX-License-Identifier: GPL-3.0-only
package source

import (
	"bytes"
	"errors"
	"net/url"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/codec"
)

func trimSourceContent(content []byte) []byte {
	content = bytes.TrimSpace(content)
	for bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		content = bytes.TrimSpace(content[3:])
	}
	return content
}

// Announcements may precede URI entries; recognizable documents stay strict.
func sourceURIList(content []byte) bool {
	found := false
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if ok && !strings.HasPrefix(value, "//") {
			switch strings.Trim(strings.TrimSpace(key), "\"'") {
			case "proxies", "Proxy", "proxy-providers", "outbounds", "endpoints":
				return false
			}
		}
		found = found || sourceURILine(line)
	}
	return found
}

// Preserve V1 import behavior for hints its converter did not apply, and expose
// that limitation separately from unsupported entries. Strict Decode is unchanged.
func sourceURIConfig(line string) (map[string]any, bool, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, false, err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, false, err
	}
	metadata := false
	if strings.EqualFold(u.Scheme, "vless") || strings.EqualFold(u.Scheme, "trojan") {
		for _, key := range []string{"spx", "ech"} {
			if values, ok := q[key]; ok {
				if len(values) != 1 {
					return nil, false, errors.New("repeated legacy URI hint")
				}
				q.Del(key)
				metadata = true
			}
		}
	}
	if metadata {
		u.RawQuery = q.Encode()
		line = u.String()
	}
	config, err := codec.DecodeURI(line)
	return config, metadata, err
}
