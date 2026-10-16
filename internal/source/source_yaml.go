// SPDX-License-Identifier: GPL-3.0-only
package source

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
	"gopkg.in/yaml.v3"
)

// yaml.v3 builds a complete syntax tree even when decoding into an iterator.
// Split independent top-level fields and sequence items before invoking it.
// Cross-item anchors fall back to the strict parser only for bounded documents.
func (s *sourceSpool) decodeYAML(content []byte) (map[string]string, error) {
	beforeSlots, beforeBytes, beforeSummary, beforeEntries := len(s.slots), s.bytes, s.summary, s.entries
	providers, err := s.decodeYAMLParts(content)
	if err == nil {
		return providers, nil
	}
	if len(content) > 2<<20 || errors.Is(err, errSourceBudget) || errors.Is(err, errSourceStorage) {
		return nil, err
	}
	// No callbacks have run. Roll back spool metadata before parsing aliases as a document.
	s.slots = s.slots[:beforeSlots]
	s.bytes = beforeBytes
	s.summary = beforeSummary
	s.entries = beforeEntries
	s.names = map[string][]int{}
	for i, slot := range s.slots {
		s.names[slot.name] = append(s.names[slot.name], i)
	}
	if e := s.file.Truncate(beforeBytes); e != nil {
		return nil, fmt.Errorf("%w: %w", errSourceStorage, e)
	}
	if _, e := s.file.Seek(beforeBytes, 0); e != nil {
		return nil, fmt.Errorf("%w: %w", errSourceStorage, e)
	}
	return s.decodeFullYAML(content)
}

type yamlField struct {
	key              string
	start, headerEnd int
}

func (s *sourceSpool) decodeYAMLParts(content []byte) (map[string]string, error) {
	var fields []yamlField
	offset := 0
	for _, line := range bytes.SplitAfter(content, []byte("\n")) {
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '-' && line[0] != '#' && line[0] != '\n' && line[0] != '\r' {
			if key, _, ok := bytes.Cut(line, []byte(":")); ok {
				key = bytes.TrimSpace(key)
				if !bytes.ContainsAny(key, "{}[]") {
					fields = append(fields, yamlField{strings.Trim(string(key), "\"'"), offset, offset + len(key) + 1})
				}
			}
		}
		offset += len(line)
	}
	if len(fields) == 0 {
		return nil, errors.New("unsupported YAML root; flatten large anchored documents")
	}
	prefix := bytes.TrimSpace(content[:fields[0].start])
	for _, line := range bytes.Split(prefix, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 && !bytes.HasPrefix(line, []byte("#")) && !bytes.Equal(line, []byte("---")) {
			return nil, errors.New("invalid YAML preamble")
		}
	}
	providers := map[string]string{}
	seen := map[string]bool{}
	found := false
	for i, field := range fields {
		end := len(content)
		if i+1 < len(fields) {
			end = fields[i+1].start
		}
		if seen[field.key] {
			return nil, errors.New("duplicate YAML field")
		}
		seen[field.key] = true
		raw := content[field.start:end]
		if field.key == "proxies" || field.key == "Proxy" {
			found = true
			if err := s.decodeYAMLSequence(content[field.headerEnd:end]); err != nil {
				return nil, err
			}
			continue
		}
		if len(raw) > 2<<20 {
			return nil, errors.New("non-proxy YAML field exceeds 2MiB syntax budget")
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, errors.New("invalid YAML field")
		}
		if field.key == "proxy-providers" {
			found = true
			providers = s.providerURLs(doc[field.key])
		}
	}
	if !found {
		return nil, errors.New("source contains no proxy fields")
	}
	return providers, nil
}

func (s *sourceSpool) decodeYAMLSequence(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '[' {
		items, err := yamlFlowItems(trimmed)
		if err != nil {
			return err
		}
		for _, item := range items {
			if len(item) > 64<<10 {
				return errors.New("proxy YAML exceeds 64KiB syntax budget")
			}
			var value any
			if err := yaml.Unmarshal(item, &value); err != nil {
				return errors.New("invalid YAML proxy")
			}
			config, ok := value.(map[string]any)
			if !ok {
				if err := s.countEntry(); err != nil {
					return err
				}
				s.diagnostic("invalid_node")
				continue
			}
			if err := s.add(config, model.ClashYAML); err != nil {
				return err
			}
		}
		return nil
	}
	var starts []int
	indent := -1
	offset := 0
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		text := bytes.TrimLeft(line, " ")
		spaces := len(line) - len(text)
		if len(text) > 0 && text[0] == '-' && (len(text) == 1 || text[1] == ' ' || text[1] == '\n' || text[1] == '\r') {
			if indent < 0 {
				indent = spaces
			}
			if spaces == indent {
				starts = append(starts, offset)
			}
		}
		offset += len(line)
	}
	if len(starts) == 0 {
		return errors.New("proxies must be a sequence")
	}
	for _, line := range bytes.Split(raw[:starts[0]], []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) > 0 && line[0] != '#' {
			return errors.New("unsupported sequence anchor")
		}
	}
	for i, start := range starts {
		end := len(raw)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		if end-start > 64<<10 {
			return errors.New("proxy YAML exceeds 64KiB syntax budget")
		}
		var entries []any
		if err := yaml.Unmarshal(raw[start:end], &entries); err != nil || len(entries) != 1 {
			return errors.New("invalid YAML proxy or cross-item anchor")
		}
		config, ok := entries[0].(map[string]any)
		if !ok {
			if err := s.countEntry(); err != nil {
				return err
			}
			s.diagnostic("invalid_node")
			continue
		}
		if err := s.add(config, model.ClashYAML); err != nil {
			return err
		}
	}
	return nil
}

func yamlFlowItems(raw []byte) ([][]byte, error) {
	var items [][]byte
	start, depth := 1, 1
	quote := byte(0)
	comment := false
	for i := 1; i < len(raw); i++ {
		c := raw[i]
		if comment {
			if c == '\n' {
				comment = false
			}
			continue
		}
		if quote != 0 {
			if c == '\\' && quote == '"' {
				i++
				continue
			}
			if c == quote {
				if quote == '\'' && i+1 < len(raw) && raw[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\n') {
			comment = true
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				if c != ']' {
					return nil, errors.New("invalid YAML flow sequence")
				}
				if item := bytes.TrimSpace(raw[start:i]); len(item) > 0 {
					items = append(items, item)
				}
				tail := bytes.TrimSpace(raw[i+1:])
				if len(tail) > 0 && tail[0] != '#' {
					return nil, errors.New("trailing YAML sequence content")
				}
				return items, nil
			}
		case ',':
			if depth == 1 {
				item := bytes.TrimSpace(raw[start:i])
				if len(item) == 0 {
					return nil, errors.New("empty YAML sequence item")
				}
				items = append(items, item)
				start = i + 1
			}
		}
	}
	return nil, errors.New("unterminated YAML flow sequence")
}
