// SPDX-License-Identifier: GPL-3.0-only
package filter

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
)

type Options struct {
	Filter              string `json:"filter,omitempty" yaml:"filter"`
	NameRegexContain    string `json:"name_regex_contain,omitempty" yaml:"name_regex_contain"`
	NameRegexNotContain string `json:"name_regex_not_contain,omitempty" yaml:"name_regex_not_contain"`
}

// Selector is immutable and safe for concurrent use. Legacy conditions use RE2.
type Selector struct {
	expression string
	include    *regexp.Regexp
	exclude    *regexp.Regexp
}

func Compile(ctx context.Context, options Options) (*Selector, error) {
	s := &Selector{expression: strings.TrimSpace(options.Filter)}
	var err error
	for _, field := range []struct {
		value string
		out   **regexp.Regexp
	}{{options.NameRegexContain, &s.include}, {options.NameRegexNotContain, &s.exclude}} {
		if len(field.value) > 4096 {
			return nil, errors.New("legacy selector exceeds 4096 bytes")
		}
		if field.value != "" {
			*field.out, err = regexp.Compile(field.value)
			if err != nil {
				return nil, errors.New("invalid legacy name regex")
			}
		}
	}
	if len(s.expression) > 4096 {
		return nil, errors.New("filter exceeds 4096 bytes")
	}
	if s.expression != "" {
		expr, _ := json.Marshal(s.expression)
		if _, err = runJS(ctx, "compileFilter("+string(expr)+");'valid'"); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Selector) Active() bool {
	return s != nil && (s.expression != "" || s.include != nil || s.exclude != nil)
}

// Match selects nodes independently. Callers choose whether to prune paths or
// retain their dependencies, according to the stage that owns the selection.
func (s *Selector) Match(ctx context.Context, nodes []model.Node) ([]bool, error) {
	return s.MatchDisplay(ctx, nodes, nil)
}

// MatchDisplay lets presentation expressions use display names while legacy
// Go conditions keep matching the original names.
func (s *Selector) MatchDisplay(ctx context.Context, nodes []model.Node, display map[string]string) ([]bool, error) {
	result := make([]bool, len(nodes))
	for start := 0; start < len(nodes); start += 200 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+200, len(nodes))
		var objects []json.RawMessage
		var indices []int
		var keys [][32]byte
		bytes := 2
		for i := start; i < end; i++ {
			n := nodes[i]
			result[i] = s == nil || (s.include == nil || s.include.MatchString(n.Name)) && (s.exclude == nil || !s.exclude.MatchString(n.Name))
			if !result[i] || s == nil || s.expression == "" {
				continue
			}
			object := model.CloneMap(n.Config)
			object["name"] = n.Name
			object["hidden"] = n.Hidden || n.Config["hidden"] == true
			if name, ok := display[n.Key]; ok {
				object["name"] = name
			}
			data, err := json.Marshal(object)
			if err != nil || len(data)+2 > 1<<20 {
				return nil, errors.New("filter batch exceeds JSON or 1MiB limit")
			}
			key := resultKey(s.expression, data)
			if value, ok := results.get(key); ok {
				result[i] = value
				continue
			}
			if bytes+len(data)+1 > 1<<20 {
				if err = s.matchBatch(ctx, objects, indices, keys, result); err != nil {
					return nil, err
				}
				objects, indices, keys = nil, nil, nil
				bytes = 2
			}
			bytes += len(data) + 1
			objects = append(objects, data)
			indices = append(indices, i)
			keys = append(keys, key)
		}
		if err := s.matchBatch(ctx, objects, indices, keys, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Selector) matchBatch(ctx context.Context, objects []json.RawMessage, indices []int, keys [][32]byte, result []bool) error {
	if len(objects) == 0 {
		return nil
	}
	data, err := json.Marshal(objects)
	if err != nil || len(data) > 1<<20 {
		return errors.New("filter batch exceeds JSON or 1MiB limit")
	}
	expr, _ := json.Marshal(s.expression)
	quotedData, _ := json.Marshal(string(data))
	output, err := runJS(ctx, "matchFilter("+string(expr)+",JSON.parse("+string(quotedData)+"))")
	if err != nil {
		return err
	}
	var matches []bool
	if json.Unmarshal([]byte(output), &matches) != nil || len(matches) != len(indices) {
		return errors.New("invalid filter result length")
	}
	for j, i := range indices {
		result[i] = matches[j]
		results.put(keys[j], matches[j])
	}
	return nil
}

// Prune removes rejected nodes and all paths depending on them in O(N+E).
// It never restores a rejected dependency by computing a graph closure.
func (s *Selector) Prune(ctx context.Context, nodes []model.Node) ([]model.Node, error) {
	mask, err := s.Match(ctx, nodes)
	if err != nil {
		return nil, err
	}
	reverse := make(map[string][]int, len(nodes))
	known := make(map[string]bool, len(nodes))
	var rejected []int
	for i, n := range nodes {
		known[n.Key] = true
		for _, dep := range n.Dependencies {
			reverse[dep] = append(reverse[dep], i)
		}
		if !mask[i] {
			rejected = append(rejected, i)
		}
	}
	for i, n := range nodes {
		for _, dep := range n.Dependencies {
			if !known[dep] && mask[i] {
				mask[i] = false
				rejected = append(rejected, i)
			}
		}
	}
	for head := 0; head < len(rejected); head++ {
		for _, child := range reverse[nodes[rejected[head]].Key] {
			if mask[child] {
				mask[child] = false
				rejected = append(rejected, child)
			}
		}
	}
	result := make([]model.Node, 0, len(nodes)-len(rejected))
	for i, node := range nodes {
		if mask[i] {
			result = append(result, node)
		}
	}
	return result, nil
}
