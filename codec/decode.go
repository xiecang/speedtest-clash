package codec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/metacubex/mihomo/common/structure"
	_ "github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/dns"
	"gopkg.in/yaml.v3"
)

func Decode(content []byte) (DecodeResult, error) {
	result, err := DecodeUnresolved(content)
	if err != nil {
		return result, err
	}
	if err := ResolveDependencies(result.Nodes); err != nil {
		return result, err
	}
	seen := make(map[string]bool, len(result.Nodes))
	unique := result.Nodes[:0]
	for _, n := range result.Nodes {
		if !seen[n.Key] {
			unique = append(unique, n)
			seen[n.Key] = true
		}
	}
	result.Nodes = unique
	return result, nil
}

func DecodeUnresolved(content []byte) (DecodeResult, error) {
	result := DecodeResult{SchemaVersion: SchemaVersion, Nodes: []Node{}}
	if len(content) > MaxSourceBytes {
		return result, fmt.Errorf("source exceeds %d bytes", MaxSourceBytes)
	}
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return result, errors.New("empty node source")
	}
	var document map[string]any
	var configs []map[string]any
	format := ClashYAML
	if json.Unmarshal(content, &document) == nil && (document["outbounds"] != nil || document["endpoints"] != nil) {
		format = SingBoxJSON
		for _, field := range []string{"outbounds", "endpoints"} {
			list, _ := document[field].([]any)
			for _, raw := range list {
				config, ok := raw.(map[string]any)
				if !ok {
					return result, fmt.Errorf("invalid %s entry", field)
				}
				typeName, _ := config["type"].(string)
				if typeName == "selector" || typeName == "urltest" || typeName == "block" || typeName == "dns" {
					continue
				}
				configs = append(configs, config)
			}
		}
	} else {
		var source struct {
			Proxies       []map[string]any `yaml:"proxies"`
			LegacyProxies []map[string]any `yaml:"Proxy"`
			Providers     map[string]struct {
				URL string `yaml:"url"`
			} `yaml:"proxy-providers"`
		}
		if yaml.Unmarshal(content, &source) == nil && (source.Proxies != nil || source.LegacyProxies != nil || source.Providers != nil) {
			configs = source.Proxies
			if configs == nil {
				configs = source.LegacyProxies
			}
			result.Providers = make(map[string]string, len(source.Providers))
			for name, p := range source.Providers {
				result.Providers[name] = p.URL
			}
		} else {
			text := string(content)
			if !strings.Contains(text, "://") {
				var err error
				for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
					var decoded []byte
					decoded, err = encoding.DecodeString(strings.Join(strings.Fields(text), ""))
					if err == nil {
						text = string(decoded)
						break
					}
				}
				if err != nil {
					return result, fmt.Errorf("unrecognized node source")
				}
			}
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				config, err := DecodeURI(line)
				if err != nil {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{Code: "unsupported_uri", Message: err.Error()})
					continue
				}
				configs = append(configs, config)
			}
		}
	}
	for _, config := range configs {
		node, err := NewNode(config, format)
		if err != nil {
			return result, err
		}
		result.Nodes = append(result.Nodes, node)
	}
	if len(result.Nodes) == 0 && len(result.Providers) == 0 {
		return result, errors.New("source contains no supported nodes")
	}
	return result, nil
}

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

func validateWireGuardDNS(config map[string]any) error {
	if config["type"] != "wireguard" {
		return nil
	}
	var option struct {
		RemoteDNS bool     `proxy:"remote-dns-resolve,omitempty"`
		DNS       []string `proxy:"dns,omitempty"`
	}
	decoder := structure.NewDecoder(structure.Option{TagName: "proxy", WeaklyTypedInput: true, KeyReplacer: structure.DefaultKeyReplacer})
	if err := decoder.Decode(config, &option); err != nil {
		return err
	}
	if !option.RemoteDNS || len(option.DNS) == 0 {
		return nil
	}
	if _, err := dns.ParseNameServer(option.DNS); err != nil {
		return fmt.Errorf("invalid WireGuard DNS: %w", err)
	}
	return nil
}

func emptyScalar(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case bool:
		return !v
	case int:
		return v == 0
	case int64:
		return v == 0
	case int32:
		return v == 0
	case uint:
		return v == 0
	case uint64:
		return v == 0
	case uint32:
		return v == 0
	case float64:
		return v == 0
	}
	return false
}
