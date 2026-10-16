// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"encoding/json"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
	"gopkg.in/yaml.v3"
)

func TestExportPreservesNamesAndDependencyReferences(t *testing.T) {
	decoded, err := nq.Decode([]byte("proxies:\n- {name: '🇯🇵 自建', type: http, server: first.invalid, port: 80}\n- {name: child, type: http, server: second.invalid, port: 80, dialer-proxy: '🇯🇵 自建'}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []nq.Format{nq.ClashYAML, nq.SingBoxJSON} {
		content, err := nq.Encode(decoded.Nodes, format)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if format == nq.ClashYAML {
			err = yaml.Unmarshal(content, &document)
		} else {
			err = json.Unmarshal(content, &document)
		}
		if err != nil {
			t.Fatal(err)
		}
		field, tag, dep := "proxies", "name", "dialer-proxy"
		if format == nq.SingBoxJSON {
			field, tag, dep = "outbounds", "tag", "detour"
		}
		nodes := document[field].([]any)
		if nodes[0].(map[string]any)[tag] != "🇯🇵 自建" || nodes[1].(map[string]any)[dep] != "🇯🇵 自建" {
			t.Fatalf("%s lost name or dependency: %s", format, content)
		}
		roundtrip, err := nq.Decode(content)
		if err != nil {
			t.Fatal(err)
		}
		if format == nq.ClashYAML && roundtrip.Nodes[1].Key != decoded.Nodes[1].Key {
			t.Fatal("display mapping changed identity")
		}
	}
}

func TestDisplayNamesResolveDuplicatesAndReservedGroups(t *testing.T) {
	var nodes []nq.Node
	for i, name := range []string{"🇯🇵 自建", "🇯🇵 自建", "DIRECT", "personal group"} {
		node, err := nq.NewNode(map[string]any{"name": name, "type": "http", "server": "fixture.invalid", "port": 8000 + i}, nq.ClashYAML)
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
	}
	names := nq.DisplayNames(nodes, "personal group")
	reversed := append([]nq.Node(nil), nodes...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	reverseNames := nq.DisplayNames(reversed, "personal group")
	used := map[string]bool{"DIRECT": true, "personal group": true}
	for _, node := range nodes {
		name := names[node.Key]
		if name == "" || used[name] || reverseNames[node.Key] != name {
			t.Fatal("export names are ambiguous or order-dependent")
		}
		used[name] = true
	}
}
