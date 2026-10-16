// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/clash2singbox/convert"
	cbmodel "github.com/xiecang/node-quality/v2/internal/clash2singbox/model"
	"github.com/xiecang/node-quality/v2/internal/clash2singbox/model/clash"
	"github.com/xiecang/node-quality/v2/internal/filter"
	"github.com/xiecang/node-quality/v2/internal/model"
	"gopkg.in/yaml.v3"
)

func Encode(nodes []model.Node, format model.Format) ([]byte, error) {
	return EncodeNamed(nodes, format, DisplayNames(nodes))
}

func EncodeNamed(nodes []model.Node, format model.Format, names map[string]string) ([]byte, error) {
	if err := model.ValidateDependencies(nodes); err != nil {
		return nil, err
	}
	switch format {
	case model.ClashYAML:
		configs := make([]map[string]any, 0, len(nodes))
		for _, n := range nodes {
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
		return yaml.Marshal(map[string]any{"proxies": configs})
	case model.SingBoxJSON:
		return encodeSingBox(nodes, names)
	case model.URI:
		var lines []string
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

func encodeSingBox(nodes []model.Node, names map[string]string) ([]byte, error) {
	var configs []map[string]any
	outbounds := []map[string]any{}
	endpoints := []map[string]any{}
	for _, n := range nodes {
		config := model.CloneMap(n.Config)
		if n.Format == model.SingBoxJSON {
			config["tag"] = names[n.Key]
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
		// __type is legacy editor metadata; preserve it in Clash, omit it from native outbounds.
		delete(config, "__type")
		if err := normalizePacketEncoding(config); err != nil {
			return nil, fmt.Errorf("%s: %w", n.Key, err)
		}
		if err := checkClashFields(config); err != nil {
			return nil, fmt.Errorf("%s: %w", n.Key, err)
		}
		config["name"] = names[n.Key]
		if n.Hidden {
			config["hidden"] = true
		}
		if config["type"] == "direct" {
			outbounds = append(outbounds, map[string]any{"type": "direct", "tag": names[n.Key]})
			continue
		}
		configs = append(configs, config)
	}
	if len(configs) > 0 {
		content, err := yaml.Marshal(map[string]any{"proxies": configs})
		if err != nil {
			return nil, err
		}
		var source clash.Clash
		if err = yaml.Unmarshal(content, &source); err != nil {
			return nil, err
		}
		converted, eps, err := convert.Clash2sing(source, cbmodel.SING114)
		if err != nil {
			return nil, fmt.Errorf("Clash to sing-box: %w", err)
		}
		for _, value := range converted {
			content, e := json.Marshal(value)
			if e != nil {
				return nil, e
			}
			var config map[string]any
			if e = json.Unmarshal(content, &config); e != nil {
				return nil, e
			}
			outbounds = append(outbounds, config)
		}
		for _, value := range eps {
			content, e := json.Marshal(value)
			if e != nil {
				return nil, e
			}
			var config map[string]any
			if e = json.Unmarshal(content, &config); e != nil {
				return nil, e
			}
			endpoints = append(endpoints, config)
		}
	}
	byTag := make(map[string]map[string]any)
	for _, config := range append(outbounds, endpoints...) {
		if tag, ok := config["tag"].(string); ok {
			if byTag[tag] != nil {
				return nil, fmt.Errorf("duplicate sing-box tag %q", tag)
			}
			byTag[tag] = config
		}
	}
	for _, node := range nodes {
		if node.Format == model.ClashYAML && node.Config["udp"] == false {
			kind, _ := node.Config["type"].(string)
			if kind == "wireguard" || kind == "direct" || kind == "http" || kind == "anytls" {
				continue
			}
			if outbound := byTag[names[node.Key]]; outbound != nil {
				outbound["network"] = "tcp"
			}
		}
	}
	dependencies := make(map[string]string)
	for _, n := range nodes {
		if n.Format == model.ClashYAML && len(n.Dependencies) == 1 {
			dependencies[names[n.Key]] = names[n.Dependencies[0]]
		}
	}
	for _, config := range append(outbounds, endpoints...) {
		tag, _ := config["tag"].(string)
		if dep := dependencies[tag]; dep != "" {
			// Converted plugins may introduce an internal transport hop. Attach
			// the external dependency to that hop instead of bypassing it.
			if internal, _ := config["detour"].(string); internal != "" {
				helper := byTag[internal]
				if helper == nil {
					return nil, fmt.Errorf("missing converted transport %q", internal)
				}
				config = helper
			}
			config["detour"] = dep
		}
	}
	result := map[string]any{"outbounds": outbounds, "log": map[string]any{"disabled": true}, "dns": map[string]any{"servers": []any{map[string]any{"type": "local", "tag": "local"}}}, "route": map[string]any{"default_domain_resolver": "local"}}
	if len(endpoints) > 0 {
		result["endpoints"] = endpoints
	}
	return json.Marshal(result)
}

func Convert(content []byte, format model.Format) ([]byte, []model.Diagnostic, error) {
	return ConvertSelected(context.Background(), content, format, filter.Options{})
}

func ConvertSelected(ctx context.Context, content []byte, format model.Format, options filter.Options) ([]byte, []model.Diagnostic, error) {
	decoded, err := Decode(content)
	if err != nil {
		return nil, decoded.Diagnostics, err
	}
	if len(decoded.Diagnostics) > 0 {
		return nil, decoded.Diagnostics, errors.New("source contains unsupported entries")
	}
	selector, err := filter.Compile(ctx, options)
	if err != nil {
		return nil, decoded.Diagnostics, err
	}
	nodes, err := selector.Prune(ctx, decoded.Nodes)
	if err != nil {
		return nil, decoded.Diagnostics, err
	}
	output, err := Encode(nodes, format)
	return output, decoded.Diagnostics, err
}
