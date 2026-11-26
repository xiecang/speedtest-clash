package source

import (
	"context"
	"strings"
	"testing"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestDetailedSourceAcceptsYAMLFlowMapping(t *testing.T) {
	content := `{proxies: [{name: fixture, type: ss, cipher: aes-128-gcm, password: fixture-secret, server: fixture.invalid, port: 80,},],}`
	var nodes []model.Node
	summary, err := LoadSourceDetailed(context.Background(), "inline:"+content, nil, func(batch []model.Node) error { nodes = append(nodes, batch...); return nil })
	if err != nil || summary.Partial || summary.Total != 1 || summary.Supported != 1 || len(nodes) != 1 {
		t.Fatalf("valid flow YAML rejected: summary=%+v emitted=%d err=%v", summary, len(nodes), err)
	}
}

func TestYAMLHeaderColonWhitespaceAndLargeRulesSection(t *testing.T) {
	var b strings.Builder
	b.WriteString("proxies : [{name: spaced, type: http, server: example.invalid, port: 80}]\n")
	b.WriteString("rules :\n")
	ruleLine := "  - DOMAIN-SUFFIX,very-long-subdomain-entry.example.invalid,DIRECT\n"
	for b.Len() < (2<<20)+(256<<10) {
		b.WriteString(ruleLine)
	}
	var nodes []model.Node
	summary, err := LoadSourceDetailed(context.Background(), "inline:"+b.String(), nil, func(batch []model.Node) error {
		nodes = append(nodes, batch...)
		return nil
	})
	if err != nil || summary.Partial || len(nodes) != 1 || nodes[0].Name != "spaced" {
		t.Fatalf("spaced key or >2MiB rules failed: summary=%+v nodes=%d err=%v", summary, len(nodes), err)
	}

	// Malformed YAML inside a >2MiB rules section must still be rejected before emission.
	malformed := b.String() + "  - [unterminated\n"
	emitted := 0
	if _, err := LoadSourceDetailed(context.Background(), "inline:"+malformed, nil, func(batch []model.Node) error {
		emitted += len(batch)
		return nil
	}); err == nil || emitted != 0 {
		t.Fatalf("malformed >2MiB rules section accepted: emitted=%d err=%v", emitted, err)
	}
}

func TestDetailedSourceRejectsMalformedDocumentsBeforeEmission(t *testing.T) {
	for _, content := range []string{
		`{"proxies":[{"name":"fixture","type":"direct"}]`,
		"proxies:\n  - {name: fixture, type: direct}\nbroken: [",
		`{"outbounds":{}}`,
		"{\"outbounds\":[\nhttp://example.invalid:8080#good",
	} {
		emitted := 0
		_, err := LoadSourceDetailed(context.Background(), "inline:"+content, nil, func(batch []model.Node) error { emitted += len(batch); return nil })
		if err == nil || emitted != 0 {
			t.Fatalf("damaged source accepted: emitted=%d err=%v", emitted, err)
		}
	}
}
