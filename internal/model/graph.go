// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"errors"
	"fmt"
)

func ResolveDependencies(nodes []Node) error {
	names := make(map[string]int)
	state := make(map[int]int)
	for i, n := range nodes {
		if prior, ok := names[n.Name]; ok && nodes[prior].Key != n.Key {
			return fmt.Errorf("ambiguous node name %q", n.Name)
		}
		names[n.Name] = i
	}
	var visit func(int) error
	visit = func(i int) error {
		if state[i] == 1 {
			return errors.New("proxy dependency cycle")
		}
		if state[i] == 2 {
			return nil
		}
		state[i] = 1
		field := "dialer-proxy"
		if nodes[i].Format == SingBoxJSON {
			field = "detour"
		}
		if name, ok := nodes[i].Config[field].(string); ok && name != "" {
			dep, ok := names[name]
			if !ok {
				return fmt.Errorf("missing dependency %q", name)
			}
			if err := visit(dep); err != nil {
				return err
			}
			nodes[i].Dependencies = []string{nodes[dep].Key}
		}
		key, err := IdentityKey(nodes[i].Config, nodes[i].Format, nodes[i].Dependencies)
		if err != nil {
			return err
		}
		nodes[i].Key = key
		nodes[i].Revision = key
		state[i] = 2
		return nil
	}
	for i := range nodes {
		if err := visit(i); err != nil {
			return err
		}
	}
	return ValidateDependencies(nodes)
}

func ValidateDependencies(nodes []Node) error {
	byKey := make(map[string]Node)
	for _, n := range nodes {
		if err := ValidateIdentity(n); err != nil {
			return err
		}
		if n.Key == "" {
			return errors.New("empty node key")
		}
		byKey[n.Key] = n
	}
	state := make(map[string]int)
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 1 {
			return errors.New("proxy dependency cycle")
		}
		if state[key] == 2 {
			return nil
		}
		n, ok := byKey[key]
		if !ok {
			return fmt.Errorf("missing dependency %s", key)
		}
		state[key] = 1
		for _, dep := range n.Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[key] = 2
		return nil
	}
	for key := range byKey {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

func DependencyClosure(selected, all []Node) []Node {
	index := make(map[string]Node, len(all))
	for _, node := range all {
		index[node.Key] = node
	}
	seen := make(map[string]bool)
	var result []Node
	var add func(Node)
	add = func(node Node) {
		if seen[node.Key] {
			return
		}
		seen[node.Key] = true
		for _, key := range node.Dependencies {
			add(index[key])
		}
		result = append(result, node)
	}
	for _, node := range selected {
		add(node)
	}
	return result
}
