package codec

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func Encode(nodes []Node, format Format) ([]byte, error) {
	return EncodeNamed(nodes, format, DisplayNames(nodes))
}

func EncodeNamed(nodes []Node, format Format, names map[string]string) ([]byte, error) {
	switch format {
	case ClashYAML:
		configs, err := EncodeClashProxies(nodes, names)
		if err != nil {
			return nil, err
		}
		return yaml.Marshal(map[string]any{"proxies": configs})
	case SingBoxJSON:
		outbounds, endpoints, err := EncodeSingBoxNodes(nodes, names)
		if err != nil {
			return nil, err
		}
		result := map[string]any{
			"outbounds": outbounds,
			"log":       map[string]any{"disabled": true},
			"dns":       map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "local"}}},
			"route":     map[string]any{"default_domain_resolver": "local"},
		}
		if len(endpoints) > 0 {
			result["endpoints"] = endpoints
		}
		return json.Marshal(result)
	case URI:
		if err := ValidateDependencies(nodes); err != nil {
			return nil, err
		}
		lines := make([]string, 0, len(nodes))
		for _, n := range nodes {
			n.Name = names[n.Key]
			line, err := encodeURI(n)
			if err != nil {
				return nil, err
			}
			lines = append(lines, line)
		}
		return []byte(strings.Join(lines, "\n")), nil
	default:
		return nil, fmt.Errorf("unsupported output format %q", format)
	}
}

// EncodeClashProxies validates dependencies and produces structured Clash/Mihomo proxy maps
// without intermediate YAML serialization, folding native shadowtls helper chains when present.
func EncodeClashProxies(nodes []Node, names map[string]string) ([]map[string]any, error) {
	if err := ValidateDependencies(nodes); err != nil {
		return nil, err
	}
	if names == nil {
		names = DisplayNames(nodes)
	}
	byKey := make(map[string]Node, len(nodes))
	depUsers := make(map[string]int, len(nodes))
	foldedHelpers := make(map[string]bool)
	for _, n := range nodes {
		byKey[n.Key] = n
		for _, dep := range n.Dependencies {
			depUsers[dep]++
		}
	}
	for _, n := range nodes {
		if n.Format == SingBoxJSON && n.Config["type"] == "shadowsocks" && emptyScalar(n.Config["server"]) && len(n.Dependencies) == 1 {
			helper := byKey[n.Dependencies[0]]
			if helper.Format == SingBoxJSON && helper.Config["type"] == "shadowtls" && depUsers[helper.Key] == 1 {
				foldedHelpers[helper.Key] = true
			}
		}
	}

	configs := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		if foldedHelpers[n.Key] {
			continue
		}
		if n.Format == SingBoxJSON && n.Config["type"] == "shadowsocks" && len(n.Dependencies) == 1 && foldedHelpers[n.Dependencies[0]] {
			helper := byKey[n.Dependencies[0]]
			cfg, err := foldShadowTLSHelperToClash(n, helper, names[n.Key], names)
			if err != nil {
				return nil, err
			}
			configs = append(configs, cfg)
			continue
		}
		config, err := ClashConfig(n)
		if err != nil {
			return nil, err
		}
		config["name"] = names[n.Key]
		if n.Hidden {
			config["hidden"] = true
		}
		if len(n.Dependencies) == 1 {
			config["dialer-proxy"] = names[n.Dependencies[0]]
		}
		configs = append(configs, config)
	}
	return configs, nil
}

// EncodeSingBoxNodes validates dependencies and produces structured sing-box 1.14
// outbound and endpoint maps in a single pass without intermediate YAML/JSON roundtrips.
func EncodeSingBoxNodes(nodes []Node, names map[string]string) ([]map[string]any, []map[string]any, error) {
	if err := ValidateDependencies(nodes); err != nil {
		return nil, nil, err
	}
	if names == nil {
		names = DisplayNames(nodes)
	}
	outbounds := make([]map[string]any, 0, len(nodes))
	endpoints := make([]map[string]any, 0)
	for _, n := range nodes {
		tag := names[n.Key]
		if n.Format == SingBoxJSON {
			config := CloneMap(n.Config)
			config["tag"] = tag
			if len(n.Dependencies) == 1 {
				config["detour"] = names[n.Dependencies[0]]
			}
			if config["type"] == "wireguard" {
				endpoints = append(endpoints, config)
			} else {
				outbounds = append(outbounds, config)
			}
			continue
		}
		converted, ep, err := convertClashNodeToSingBox(tag, n.Config)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", n.Key, err)
		}
		if ep != nil {
			endpoints = append(endpoints, ep)
		} else {
			outbounds = append(outbounds, converted...)
		}
	}

	byTag := make(map[string]map[string]any, len(outbounds)+len(endpoints))
	for _, config := range outbounds {
		tag, _ := config["tag"].(string)
		if tag != "" {
			if byTag[tag] != nil {
				return nil, nil, fmt.Errorf("duplicate sing-box tag %q", tag)
			}
			byTag[tag] = config
		}
	}
	for _, config := range endpoints {
		tag, _ := config["tag"].(string)
		if tag != "" {
			if byTag[tag] != nil {
				return nil, nil, fmt.Errorf("duplicate sing-box tag %q", tag)
			}
			byTag[tag] = config
		}
	}

	for _, n := range nodes {
		if n.Format != ClashYAML || len(n.Dependencies) != 1 {
			continue
		}
		tag := names[n.Key]
		dep := names[n.Dependencies[0]]
		config := byTag[tag]
		if config == nil || dep == "" {
			continue
		}
		// ShadowTLS introduces an internal helper outbound; attach upstream detour to that helper.
		if internal, _ := config["detour"].(string); internal != "" {
			helper := byTag[internal]
			if helper == nil {
				return nil, nil, fmt.Errorf("missing converted transport %q", internal)
			}
			config = helper
		}
		config["detour"] = dep
	}
	return outbounds, endpoints, nil
}
