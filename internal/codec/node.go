// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"errors"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func NewNode(config map[string]any, format model.Format) (model.Node, error) {
	config = model.CloneMap(config)
	hidden := false
	if raw, exists := config["hidden"]; exists {
		var ok bool
		hidden, ok = raw.(bool)
		if !ok {
			return model.Node{}, errors.New("node hidden must be boolean")
		}
		delete(config, "hidden")
	}

	if format == model.ClashYAML {
		if kind, ok := config["type"].(string); ok {
			if canonical := map[string]string{"shadowsocks": "ss", "socks": "socks5", "hy2": "hysteria2", "hy": "hysteria"}[strings.ToLower(kind)]; canonical != "" {
				config["type"] = canonical
			}
		}
	}
	if config["type"] == "wireguard" && format == model.ClashYAML {
		if peers, ok := config["peers"].([]any); ok {
			normalized := make([]any, 0, len(peers))
			for _, raw := range peers {
				if peer, ok := raw.(map[string]any); ok {
					p := model.CloneMap(peer)
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
	name, _ := config["name"].(string)
	if format == model.SingBoxJSON {
		name, _ = config["tag"].(string)
	}
	key, err := model.IdentityKey(config, format, nil)
	if err != nil {
		return model.Node{}, err
	}
	if name == "" {
		name = key[:12]
	}
	return model.Node{Key: key, Revision: key, Name: name, Hidden: hidden, Format: format, Config: config}, nil
}
