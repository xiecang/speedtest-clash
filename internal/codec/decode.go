// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
	"gopkg.in/yaml.v3"
)

const MaxSourceBytes = 16 << 20

func Decode(content []byte) (model.DecodeResult, error) {
	result := model.DecodeResult{SchemaVersion: model.SchemaVersion, Nodes: []model.Node{}}
	if len(content) > MaxSourceBytes {
		return result, fmt.Errorf("source exceeds %d bytes", MaxSourceBytes)
	}
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return result, errors.New("empty node source")
	}
	var document map[string]any
	var configs []map[string]any
	format := model.ClashYAML
	if json.Unmarshal(content, &document) == nil && (document["outbounds"] != nil || document["endpoints"] != nil) {
		format = model.SingBoxJSON
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
			result.Providers = make(map[string]string)
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
					result.Diagnostics = append(result.Diagnostics, model.Diagnostic{Code: "unsupported_uri", Message: err.Error()})
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
	if err := model.ResolveDependencies(result.Nodes); err != nil {
		return result, err
	}
	seen := make(map[string]bool)
	unique := result.Nodes[:0]
	for _, n := range result.Nodes {
		if !seen[n.Key] {
			unique = append(unique, n)
			seen[n.Key] = true
		}
	}
	result.Nodes = unique
	if len(result.Nodes) == 0 && len(result.Providers) == 0 {
		return result, errors.New("source contains no supported nodes")
	}
	return result, nil
}
