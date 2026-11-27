package codec

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ResolveDependencies links dialer-proxy / detour references to target node identities.
// Duplicate display names are permitted on independent nodes and only rejected when an
// upstream dependency reference is actually ambiguous across distinct node keys.
func ResolveDependencies(nodes []Node) error {
	names := make(map[string][]int, len(nodes))
	for i, n := range nodes {
		names[n.Name] = append(names[n.Name], i)
	}
	state := make([]uint8, len(nodes))
	var visit func(int) error
	visit = func(i int) error {
		if state[i] == 1 {
			return errors.New("proxy dependency cycle")
		}
		if state[i] == 2 {
			return nil
		}
		state[i] = 1
		if ref := nodes[i].DialerRef(); ref != "" {
			candidates := names[ref]
			if len(candidates) == 0 {
				return fmt.Errorf("missing dependency %q", ref)
			}
			for _, dep := range candidates {
				if err := visit(dep); err != nil {
					return err
				}
			}
			firstKey := nodes[candidates[0]].Key
			for _, dep := range candidates[1:] {
				if nodes[dep].Key != firstKey {
					return fmt.Errorf("ambiguous node name %q", ref)
				}
			}
			nodes[i].Dependencies = []string{firstKey}
		}
		key, err := IdentityKey(nodes[i].Config, nodes[i].Format, nodes[i].Dependencies)
		if err != nil {
			return err
		}
		nodes[i].Key = key
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
	byKey := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		if err := ValidateIdentity(n); err != nil {
			return err
		}
		if n.Key == "" {
			return errors.New("empty node key")
		}
		byKey[n.Key] = n
	}
	state := make(map[string]uint8, len(byKey))
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

// DependencyClosure returns selected nodes along with all transitive upstream dependencies
// in topological order (dependencies before dependents), failing if any dependency is missing.
func DependencyClosure(selected, all []Node) ([]Node, error) {
	index := make(map[string]Node, len(all))
	for _, node := range all {
		if node.Key != "" {
			index[node.Key] = node
		}
	}
	for _, node := range selected {
		if node.Key != "" {
			if _, exists := index[node.Key]; !exists {
				index[node.Key] = node
			}
		}
	}
	seen := make(map[string]bool, len(selected))
	visiting := make(map[string]bool, len(selected))
	result := make([]Node, 0, len(selected))
	var add func(Node) error
	add = func(node Node) error {
		if node.Key == "" {
			return errors.New("empty node key")
		}
		if seen[node.Key] {
			return nil
		}
		if visiting[node.Key] {
			return errors.New("proxy dependency cycle")
		}
		visiting[node.Key] = true
		for _, key := range node.Dependencies {
			dep, ok := index[key]
			if !ok {
				return fmt.Errorf("missing dependency %s", key)
			}
			if err := add(dep); err != nil {
				return err
			}
		}
		delete(visiting, node.Key)
		seen[node.Key] = true
		result = append(result, node)
		return nil
	}
	for _, node := range selected {
		if err := add(node); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// DisplayNames assigns deterministic, unique export names without mutating node identities.
func DisplayNames(nodes []Node, reserved ...string) map[string]string {
	used := map[string]bool{"DIRECT": true, "REJECT": true, "direct": true, "reject": true}
	for _, name := range reserved {
		used[name] = true
	}
	ordered := append([]Node(nil), nodes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	counts := make(map[string]int, len(ordered))
	for _, node := range ordered {
		counts[node.Name]++
	}
	result := make(map[string]string, len(ordered))
	for _, node := range ordered {
		if _, exists := result[node.Key]; exists {
			continue
		}
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = "Node"
		}
		candidate := name
		if counts[node.Name] > 1 || used[candidate] {
			for length := 8; length <= len(node.Key); length++ {
				candidate = name + " [" + node.Key[:length] + "]"
				if !used[candidate] {
					break
				}
			}
		}
		used[candidate] = true
		result[node.Key] = candidate
	}
	return result
}

func RuntimeNames(nodes []Node) map[string]string {
	result := make(map[string]string, len(nodes))
	for _, node := range nodes {
		result[node.Key] = node.Key
	}
	return result
}
