// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"sort"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
)

// DisplayNames assigns deterministic, unique export names without changing identity.
// Reserved names include route actions and the caller's group names.
func DisplayNames(nodes []model.Node, reserved ...string) map[string]string {
	used := map[string]bool{"DIRECT": true, "REJECT": true, "direct": true, "reject": true}
	for _, name := range reserved {
		used[name] = true
	}
	ordered := append([]model.Node(nil), nodes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	counts := make(map[string]int)
	for _, node := range ordered {
		counts[node.Name]++
	}
	result := make(map[string]string)
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

func RuntimeNames(nodes []model.Node) map[string]string {
	result := make(map[string]string, len(nodes))
	for _, node := range nodes {
		result[node.Key] = node.Key
	}
	return result
}
