// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestDetailedSourceKeepsSupportedAndPrunesDependants(t *testing.T) {
	var nodes []nq.Node
	summary, err := nq.LoadSourceDetailed(context.Background(), "inline:http://example.invalid:8080#Tokyo\nunknownproto://example.invalid:9000#bad", nil, func(batch []nq.Node) error { nodes = append(nodes, batch...); return nil })
	if err != nil || len(nodes) != 1 || summary.Unsupported != 1 {
		t.Fatalf("%+v %d %v", summary, len(nodes), err)
	}
	nodes = nil
	summary, err = nq.LoadSourceDetailed(context.Background(), "inline:Proxy: [{name: good, type: http, server: example.invalid, port: 8080}, {name: child, type: http, server: child.invalid, port: 8080, dialer-proxy: missing}]", nil, func(batch []nq.Node) error { nodes = append(nodes, batch...); return nil })
	if err != nil || len(nodes) != 1 || summary.Unsupported != 1 || nodes[0].Name != "good" {
		t.Fatalf("%+v %+v %v", summary, nodes, err)
	}
	nodes = nil
	_, err = nq.LoadSourceDetailed(context.Background(), "data:text/plain,http%3A%2F%2Fexample.invalid%3A8080%23Tokyo", nil, func(batch []nq.Node) error { nodes = append(nodes, batch...); return nil })
	if err != nil || len(nodes) != 1 {
		t.Fatal(nodes, err)
	}
	emitted := false
	_, err = nq.LoadSourceDetailed(context.Background(), "inline:proxies: [broken", nil, func([]nq.Node) error { emitted = true; return nil })
	if err == nil || emitted {
		t.Fatal("invalid document committed")
	}
	if _, _, err := nq.Convert([]byte("http://example.invalid:8080#good\nunknownproto://x"), nq.ClashYAML); err == nil {
		t.Fatal("strict conversion changed")
	}
	if !strings.Contains(summary.Diagnostics[0].Code, "dependency") {
		t.Fatal(summary)
	}
}

func TestDetailedSourceDocumentAndAliasBoundaries(t *testing.T) {
	for _, input := range []string{
		"inline:proxies:\n- &base {name: HK, type: http, server: example.invalid, port: 443}\n- {<<: *base, name: JP, server: other.invalid}\n",
		"inline:\"proxies\": [{name: Tokyo, type: http, server: example.invalid, port: 443}]\n",
		"inline:data:text/plain,http%3A%2F%2Fexample.invalid%3A8080%23Tokyo",
	} {
		var emitted []nq.Node
		_, err := nq.LoadSourceDetailed(context.Background(), input, nil, func(batch []nq.Node) error { emitted = append(emitted, batch...); return nil })
		if err != nil || len(emitted) == 0 {
			t.Fatal("valid compatibility source rejected", err)
		}
	}
	emitted := false
	_, err := nq.LoadSourceDetailed(context.Background(), "inline:proxies:\n- {name: good, type: http, server: example.invalid, port: 443}\ninvalid: [\n", nil, func([]nq.Node) error { emitted = true; return nil })
	if err == nil || emitted {
		t.Fatal("damaged tail committed partial generation")
	}
}

func TestDetailedSourceScalarEntriesAreIsolated(t *testing.T) {
	for _, source := range []string{
		`inline:{"outbounds":["bad",null,{"type":"direct","tag":"good"}]}`,
		`inline:proxies: [bad, null, {name: good, type: http, server: example.invalid, port: 443}]`,
		"inline:proxies:\n- bad\n- null\n- {name: good, type: http, server: example.invalid, port: 443}\n",
	} {
		count := 0
		summary, err := nq.LoadSourceDetailed(context.Background(), source, nil, func(nodes []nq.Node) error { count += len(nodes); return nil })
		if err != nil || count != 1 || summary.Unsupported != 2 {
			t.Fatal(summary, count, err)
		}
	}
}
