// SPDX-License-Identifier: GPL-3.0-only
package codec

import "fmt"

// Mihomo aliases must be normalized on the conversion copy, preserving identity.
func normalizePacketEncoding(config map[string]any) error {
	if config["type"] != "vmess" && config["type"] != "vless" {
		return nil
	}
	encoding := ""
	for _, key := range []string{"packet-encoding", "packet_encoding"} {
		if raw, exists := config[key]; exists {
			value, ok := raw.(string)
			if !ok {
				return fmt.Errorf("invalid packet encoding")
			}
			if encoding != "" && value != "" && encoding != value {
				return fmt.Errorf("conflicting packet encoding")
			}
			if value != "" {
				encoding = value
			}
		}
	}
	for _, alias := range []struct{ key, encoding string }{{"xudp", "xudp"}, {"packet-addr", "packetaddr"}} {
		if raw, exists := config[alias.key]; exists {
			enabled, ok := raw.(bool)
			if !ok {
				return fmt.Errorf("invalid %s flag", alias.key)
			}
			if enabled {
				if encoding != "" && encoding != alias.encoding {
					return fmt.Errorf("conflicting packet encoding")
				}
				encoding = alias.encoding
			}
			delete(config, alias.key)
		}
	}
	delete(config, "packet_encoding")
	if encoding != "" {
		config["packet-encoding"] = encoding
	}
	return nil
}
