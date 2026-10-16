// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

var pluginFields = map[string]string{
	"obfs":         "mode host",
	"v2ray-plugin": "mode host path tls mux skip-cert-verify",
	"gost-plugin":  "mode host path tls mux skip-cert-verify",
	"shadow-tls":   "host password version alpn skip-cert-verify",
}

func encodeSSPlugin(c map[string]any) (string, error) {
	plugin, _ := c["plugin"].(string)
	opts, _ := c["plugin-opts"].(map[string]any)
	fields, ok := pluginFields[plugin]
	if !ok {
		return "", fmt.Errorf("unsupported URI plugin %q", plugin)
	}
	if err := uriFields(opts, fields); err != nil {
		return "", err
	}
	if opts["skip-cert-verify"] == true {
		return "", errors.New("plugin URI cannot preserve insecure TLS")
	}
	parts := []string{plugin}
	if plugin == "obfs" {
		parts[0] = "obfs-local"
	}
	keys := make([]string, 0, len(opts))
	for key := range opts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := opts[key]
		if emptyScalar(value) {
			continue
		}
		param := key
		if plugin == "obfs" {
			if key == "mode" {
				param = "obfs"
			} else if key == "host" {
				param = "obfs-host"
			}
		}
		if key == "tls" {
			if value == true {
				parts = append(parts, "tls")
			}
			continue
		}
		if key == "mux" {
			if value == true {
				parts = append(parts, "mux=4")
			}
			continue
		}
		text := fmt.Sprint(value)
		if key == "alpn" {
			var err error
			text, err = uriStringList(value)
			if err != nil {
				return "", err
			}
		}
		if strings.ContainsAny(text, ";\r\n") {
			return "", errors.New("plugin URI cannot preserve separator in value")
		}
		parts = append(parts, param+"="+text)
	}
	return strings.Join(parts, ";"), nil
}

func decodeSSPluginURI(u *url.URL) (string, map[string]any, error) {
	value := u.Query().Get("plugin")
	if value == "" {
		return "", nil, nil
	}
	parts := strings.Split(value, ";")
	plugin := parts[0]
	if plugin == "obfs-local" || plugin == "simple-obfs" {
		plugin = "obfs"
	}
	fields, ok := pluginFields[plugin]
	if !ok {
		return "", nil, fmt.Errorf("unsupported URI plugin %q", plugin)
	}
	opts := map[string]any{}
	if plugin == "v2ray-plugin" || plugin == "gost-plugin" {
		opts["mux"] = false
	}
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		key, text, hasValue := strings.Cut(part, "=")
		if plugin == "obfs" {
			switch key {
			case "obfs":
				key = "mode"
			case "obfs-host":
				key = "host"
			default:
				return "", nil, fmt.Errorf("unsupported obfs parameter %q", key)
			}
		}
		if !strings.Contains(" "+fields+" ", " "+key+" ") {
			return "", nil, fmt.Errorf("URI plugin setting %q cannot be preserved", key)
		}
		switch key {
		case "skip-cert-verify":
			return "", nil, errors.New("plugin URI has no portable certificate verification setting")
		case "tls", "mux":
			enabled := true
			if hasValue {
				var err error
				if key == "mux" && text == "4" {
					enabled = true
				} else {
					enabled, err = strconv.ParseBool(text)
					if err != nil {
						return "", nil, errors.New("invalid plugin flag")
					}
				}
			}
			opts[key] = enabled
		case "version":
			n, err := strconv.Atoi(text)
			if err != nil {
				return "", nil, err
			}
			opts[key] = n
		case "alpn":
			opts[key] = strings.Split(text, ",")
		default:
			opts[key] = text
		}
	}
	return plugin, opts, nil
}

func validateSSPluginURI(u *url.URL) error { _, _, err := decodeSSPluginURI(u); return err }
