package filter

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/xiecang/node-quality/v2/codec"
)

// Env defines the normalized evaluation environment exposed to filter expressions,
// hiding raw format differences between Clash YAML and sing-box JSON configurations.
type Env struct {
	Name    string         `expr:"name"`
	RawName string         `expr:"raw_name"`
	Type    string         `expr:"type"`
	Server  string         `expr:"server"`
	Port    int            `expr:"port"`
	Network string         `expr:"network"`
	TLS     bool           `expr:"tls"`
	UDP     bool           `expr:"udp"`
	Relay   bool           `expr:"relay"`
	Hidden  bool           `expr:"hidden"`
	Config  map[string]any `expr:"config"`
}

// NewEnv constructs a normalized expression environment for a node.
func NewEnv(n codec.Node, displayName string) Env {
	if displayName == "" {
		displayName = n.Name
	}
	cfg := n.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return Env{
		Name:    displayName,
		RawName: n.Name,
		Type:    n.Protocol(),
		Server:  n.Server(),
		Port:    n.Port(),
		Network: n.Network(),
		TLS:     n.HasTLS(),
		UDP:     n.SupportsUDP(),
		Relay:   len(n.Dependencies) > 0,
		Hidden:  n.Hidden || cfg["hidden"] == true,
		Config:  cfg,
	}
}

type Options struct {
	Filter              string `json:"filter,omitempty" yaml:"filter"`
	NameRegexContain    string `json:"name_regex_contain,omitempty" yaml:"name_regex_contain"`
	NameRegexNotContain string `json:"name_regex_not_contain,omitempty" yaml:"name_regex_not_contain"`
}

// Selector is an immutable, concurrency-safe compiled node selector backed by
// Go RE2 regular expressions and github.com/expr-lang/expr bytecode.
type Selector struct {
	expression string
	program    *vm.Program
	include    *regexp.Regexp
	exclude    *regexp.Regexp
}

func Compile(options Options) (*Selector, error) {
	s := &Selector{expression: strings.TrimSpace(options.Filter)}
	var err error
	for _, field := range []struct {
		value string
		out   **regexp.Regexp
	}{
		{options.NameRegexContain, &s.include},
		{options.NameRegexNotContain, &s.exclude},
	} {
		if len(field.value) > 4096 {
			return nil, errors.New("name regex exceeds 4096 bytes")
		}
		if field.value != "" {
			*field.out, err = regexp.Compile(field.value)
			if err != nil {
				return nil, fmt.Errorf("invalid name regex: %w", err)
			}
		}
	}
	if len(s.expression) > 4096 {
		return nil, errors.New("filter expression exceeds 4096 bytes")
	}
	if s.expression != "" {
		prog, err := expr.Compile(
			s.expression,
			expr.Env(Env{}),
			expr.AsBool(),
			expr.MaxNodes(256),
		)
		if err != nil {
			return nil, fmt.Errorf("invalid filter expression: %w", err)
		}
		s.program = prog
	}
	return s, nil
}

func (s *Selector) Expression() string {
	if s == nil {
		return ""
	}
	return s.expression
}

func (s *Selector) Active() bool {
	return s != nil && (s.program != nil || s.include != nil || s.exclude != nil)
}

// Match evaluates each node independently against name regexes and the compiled expression.
func (s *Selector) Match(nodes []codec.Node) ([]bool, error) {
	return s.MatchDisplay(nodes, nil)
}

// MatchDisplay evaluates name regexes against raw node names and the filter expression
// against display names when provided.
func (s *Selector) MatchDisplay(nodes []codec.Node, display map[string]string) ([]bool, error) {
	result := make([]bool, len(nodes))
	for i, n := range nodes {
		if s == nil {
			result[i] = true
			continue
		}
		if s.include != nil && !s.include.MatchString(n.Name) {
			continue
		}
		if s.exclude != nil && s.exclude.MatchString(n.Name) {
			continue
		}
		if s.program == nil {
			result[i] = true
			continue
		}
		displayName := n.Name
		if name, ok := display[n.Key]; ok && name != "" {
			displayName = name
		}
		out, err := expr.Run(s.program, NewEnv(n, displayName))
		if err != nil {
			return nil, fmt.Errorf("filter evaluation failed on node %q: %w", n.Name, err)
		}
		matched, ok := out.(bool)
		if !ok {
			return nil, errors.New("filter expression did not evaluate to bool")
		}
		result[i] = matched
	}
	return result, nil
}

// Prune removes rejected nodes and all downstream paths depending on them in O(N+E)
// without ever restoring a rejected upstream dependency.
func (s *Selector) Prune(nodes []codec.Node) ([]codec.Node, error) {
	mask, err := s.Match(nodes)
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
	result := make([]codec.Node, 0, len(nodes)-len(rejected))
	for i, node := range nodes {
		if mask[i] {
			result = append(result, node)
		}
	}
	return result, nil
}
