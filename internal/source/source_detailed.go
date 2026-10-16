// SPDX-License-Identifier: GPL-3.0-only
package source

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
	"gopkg.in/yaml.v3"
)

type SourceSummary struct {
	Partial     bool               `json:"partial,omitempty"`
	Total       int                `json:"total"`
	Supported   int                `json:"supported"`
	Unsupported int                `json:"unsupported"`
	Diagnostics []model.Diagnostic `json:"diagnostics,omitempty"`
	Sources     []SourceResult     `json:"sources,omitempty"`
}

// ErrNoNodes means a complete source generation contains no supported nodes.
var ErrNoNodes = errors.New("source contains no supported nodes")

type sourceSlot struct {
	offset                int64
	size                  int
	name, dependency, key string
	format                model.Format
	state                 int
	valid                 bool
	hidden                bool
}

type sourceSpool struct {
	file    *os.File
	slots   []sourceSlot
	names   map[string][]int
	bytes   int64
	entries int
	summary SourceSummary
	err     error
}

// LoadSourceDetailed tolerates isolated unsupported entries. It validates the
// surviving dependency graph before emitting any batch, using a bounded disk
// spool rather than retaining 50,000 nested configuration maps in the heap.
func LoadSourceDetailed(ctx context.Context, source string, client *http.Client, emit func([]model.Node) error) (SourceSummary, error) {
	return loadSources(ctx, []SourceRequest{{Source: source, AllowLocalFiles: true}}, client, emit, false)
}

// LoadSourceRequestDetailed applies request options and isolates failed providers.
func LoadSourceRequestDetailed(ctx context.Context, request SourceRequest, client *http.Client, emit func([]model.Node) error) (SourceSummary, error) {
	return loadSources(ctx, []SourceRequest{request}, client, emit, false)
}

func (spool *sourceSpool) finish(ctx context.Context, emit func([]model.Node) error, allowEmpty bool) (SourceSummary, error) {
	var err error
	for i := range spool.slots {
		if err = ctx.Err(); err != nil {
			return spool.summary, err
		}
		spool.resolve(i, 0)
		if spool.err != nil {
			return spool.summary, spool.err
		}
	}
	spool.summary.Supported = 0
	for _, slot := range spool.slots {
		if slot.valid {
			spool.summary.Supported++
		}
	}
	if spool.summary.Supported == 0 && (!allowEmpty || spool.summary.Partial) {
		if spool.summary.Partial {
			return spool.summary, errors.New("incomplete source has no supported nodes")
		}
		return spool.summary, ErrNoNodes
	}
	if err = spool.emit(ctx, emit); err != nil {
		return spool.summary, err
	}
	return spool.summary, nil
}

func readSourceContent(ctx context.Context, source string, client *http.Client) ([]byte, error) {
	if strings.HasPrefix(source, "inline:data:") {
		source = strings.TrimPrefix(source, "inline:")
	}
	var reader io.ReadCloser
	switch {
	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, errors.New("invalid source URL")
		}
		request.Header.Set("User-Agent", "ClashMeta")
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("source fetch failed")
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			return nil, fmt.Errorf("source HTTP status %d", response.StatusCode)
		}
		reader = response.Body
	case strings.HasPrefix(source, "data:"):
		header, data, ok := strings.Cut(strings.TrimPrefix(source, "data:"), ",")
		if !ok {
			return nil, errors.New("invalid data URL")
		}
		decoded, err := url.PathUnescape(data)
		if err != nil {
			return nil, errors.New("invalid data URL escape")
		}
		if strings.HasSuffix(strings.ToLower(header), ";base64") {
			raw, e := base64.StdEncoding.DecodeString(decoded)
			if e != nil {
				return nil, errors.New("invalid data URL base64")
			}
			decoded = string(raw)
		}
		reader = io.NopCloser(strings.NewReader(decoded))
	case strings.HasPrefix(source, "inline:"):
		reader = io.NopCloser(strings.NewReader(strings.TrimPrefix(source, "inline:")))
	default:
		file, err := os.Open(source)
		if err != nil {
			return nil, errors.New("source file open failed")
		}
		reader = file
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, codec.MaxSourceBytes+1))
	if err != nil {
		return content, errors.New("source read failed")
	}
	if len(content) > codec.MaxSourceBytes {
		return content, fmt.Errorf("%w: source exceeds 16MiB", errSourceBudget)
	}
	return content, nil
}

func (s *sourceSpool) diagnostic(code string) {
	s.summary.Unsupported++
	if len(s.summary.Diagnostics) < 100 {
		s.summary.Diagnostics = append(s.summary.Diagnostics, model.Diagnostic{Node: fmt.Sprint(s.summary.Total), Code: code, Message: code})
	}
}

func (s *sourceSpool) countEntry() error {
	s.summary.Total++
	s.entries++
	if s.entries > 50000 {
		return fmt.Errorf("%w: source exceeds 50000 entries", errSourceBudget)
	}
	return nil
}

func (s *sourceSpool) add(config map[string]any, format model.Format) error {
	if err := s.countEntry(); err != nil {
		return err
	}
	node, err := codec.NewNode(config, format)
	if err != nil {
		s.diagnostic("invalid_node")
		return nil
	}
	kind, _ := node.Config["type"].(string)
	if !supportedSourceKind(kind, format) {
		s.diagnostic("unsupported_type")
		return nil
	}
	content, err := json.Marshal(node.Config)
	if err != nil {
		return err
	}
	if s.bytes+int64(len(content)) > 64<<20 {
		return fmt.Errorf("%w: source spool exceeds 64MiB", errSourceBudget)
	}
	slot := sourceSlot{offset: s.bytes, size: len(content), name: node.Name, key: node.Key, format: format, valid: true, hidden: node.Hidden}
	field := "dialer-proxy"
	if format == model.SingBoxJSON {
		field = "detour"
	}
	slot.dependency, _ = config[field].(string)
	if _, err = s.file.Write(content); err != nil {
		return fmt.Errorf("%w: %w", errSourceStorage, err)
	}
	s.bytes += int64(len(content))
	s.names[slot.name] = append(s.names[slot.name], len(s.slots))
	s.slots = append(s.slots, slot)
	return nil
}

func (s *sourceSpool) decode(content []byte) (map[string]string, error) {
	content = trimSourceContent(content)
	if len(content) == 0 {
		return nil, errors.New("empty node source")
	}
	if !bytes.Contains(content, []byte("://")) {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if decoded, err := enc.DecodeString(strings.Join(strings.Fields(string(content)), "")); err == nil && len(decoded) > 0 {
				content = trimSourceContent(decoded)
				break
			}
		}
	}
	if bytes.HasPrefix(content, []byte("{")) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(content, &doc); err != nil {
			return nil, errors.New("invalid JSON source")
		}
		if doc["outbounds"] != nil || doc["endpoints"] != nil {
			for _, field := range []string{"outbounds", "endpoints"} {
				var entries []json.RawMessage
				if err := json.Unmarshal(doc[field], &entries); err != nil && doc[field] != nil {
					return nil, errors.New("invalid native source entries")
				}
				for _, raw := range entries {
					var config map[string]any
					if json.Unmarshal(raw, &config) != nil || config == nil {
						if err := s.countEntry(); err != nil {
							return nil, err
						}
						s.diagnostic("invalid_node")
						continue
					}
					kind, _ := config["type"].(string)
					if kind == "selector" || kind == "urltest" || kind == "block" || kind == "dns" {
						continue
					}
					if err := s.add(config, model.SingBoxJSON); err != nil {
						return nil, err
					}
				}
			}
			return nil, nil
		}
	}
	// URI input does not build a YAML syntax tree.
	if sourceURIList(content) {
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			config, metadata, err := sourceURIConfig(line)
			if err != nil {
				if err := s.countEntry(); err != nil {
					return nil, err
				}
				s.diagnostic("unsupported_uri")
				continue
			}
			if err = s.add(config, model.ClashYAML); err != nil {
				return nil, err
			}
			if metadata && len(s.summary.Diagnostics) < 100 {
				s.summary.Diagnostics = append(s.summary.Diagnostics, model.Diagnostic{Code: "legacy_uri_metadata", Message: "V1 URI converter does not apply spx/ech hints"})
			}
		}
		return nil, nil
	}
	return s.decodeYAML(content)
}

func (s *sourceSpool) decodeFullYAML(content []byte) (map[string]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, errors.New("invalid YAML source")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("unrecognized node source")
	}
	providers := map[string]string{}
	root := doc.Content[0]
	found := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		field, value := root.Content[i].Value, root.Content[i+1]
		switch field {
		case "proxies", "Proxy":
			found = true
			if value.Kind != yaml.SequenceNode {
				return nil, errors.New("proxies must be an array")
			}
			for j, item := range value.Content {
				var config map[string]any
				if item.Decode(&config) != nil {
					if err := s.countEntry(); err != nil {
						return nil, err
					}
					s.diagnostic("invalid_node")
					continue
				}
				if err := s.add(config, model.ClashYAML); err != nil {
					return nil, err
				}
				value.Content[j] = nil
			}
		case "proxy-providers":
			found = true
			var p any
			if value.Decode(&p) != nil {
				return nil, errors.New("invalid providers")
			}
			providers = s.providerURLs(p)
		}
	}
	if !found {
		return nil, errors.New("source contains no proxy fields")
	}
	return providers, nil
}

func (s *sourceSpool) node(i int) (model.Node, error) {
	slot := s.slots[i]
	raw := make([]byte, slot.size)
	if _, err := s.file.ReadAt(raw, slot.offset); err != nil {
		return model.Node{}, err
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return model.Node{}, err
	}
	n := model.Node{Key: slot.key, Revision: slot.key, Name: slot.name, Hidden: slot.hidden, Format: slot.format, Config: config}
	if slot.dependency != "" {
		indices := s.names[slot.dependency]
		if len(indices) > 0 {
			n.Dependencies = []string{s.slots[indices[0]].key}
		}
	}
	return n, nil
}

func (s *sourceSpool) resolve(i, depth int) bool {
	slot := &s.slots[i]
	if slot.state == 2 {
		return slot.valid
	}
	if slot.state == 1 || depth >= 200 {
		slot.valid = false
		return false
	}
	slot.state = 1
	if slot.dependency != "" {
		indices := s.names[slot.dependency]
		ambiguous := len(indices) == 0
		for _, j := range indices {
			if !s.resolve(j, depth+1) {
				ambiguous = true
			}
		}
		for j := 1; j < len(indices); j++ {
			ambiguous = ambiguous || s.slots[indices[j]].key != s.slots[indices[0]].key
		}
		if ambiguous || !s.resolve(indices[0], depth+1) {
			slot.valid = false
		} else {
			node, err := s.node(i)
			if err != nil {
				s.err = err
				slot.valid = false
			} else {
				slot.key, err = model.IdentityKey(node.Config, node.Format, node.Dependencies)
				slot.valid = err == nil
				if err != nil {
					s.err = err
				}
			}
		}
	}
	if !slot.valid {
		s.diagnostic("unsupported_dependency")
	}
	slot.state = 2
	return slot.valid
}

func (s *sourceSpool) emit(ctx context.Context, emit func([]model.Node) error) error {
	emitted := map[string]bool{}
	var batch []model.Node
	included := map[string]bool{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := emit(batch); err != nil {
			return err
		}
		batch = nil
		included = map[string]bool{}
		return nil
	}
	var add func(int) error
	add = func(i int) error {
		slot := s.slots[i]
		if included[slot.key] {
			return nil
		}
		if slot.dependency != "" {
			if err := add(s.names[slot.dependency][0]); err != nil {
				return err
			}
		}
		node, err := s.node(i)
		if err != nil {
			return err
		}
		batch = append(batch, node)
		included[slot.key] = true
		return nil
	}
	for i, slot := range s.slots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !slot.valid || emitted[slot.key] {
			continue
		}
		// A complete chain is at most 200 nodes. Flush before adding a closure.
		size := 0
		for j := i; !included[s.slots[j].key]; {
			size++
			if s.slots[j].dependency == "" {
				break
			}
			j = s.names[s.slots[j].dependency][0]
		}
		if len(batch)+size > 200 {
			if err := flush(); err != nil {
				return err
			}
		}
		if err := add(i); err != nil {
			return err
		}
		emitted[slot.key] = true
	}
	return flush()
}

func supportedSourceKind(kind string, format model.Format) bool {
	if format == model.ClashYAML {
		switch kind {
		case "ss", "ssr", "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "wireguard", "socks5", "http", "snell", "mieru", "anytls", "direct", "ssh", "sudoku", "masque", "trusttunnel":
			return true
		}
	} else {
		switch kind {
		case "shadowsocks", "shadowtls", "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "wireguard", "socks", "http", "ssh", "anytls", "direct", "naive", "tor":
			return true
		}
	}
	return false
}

func sourceURILine(line string) bool {
	u, err := url.Parse(line)
	return err == nil && u.Scheme != "" && strings.HasPrefix(line, u.Scheme+"://")
}
