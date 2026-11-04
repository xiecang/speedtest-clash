package source

import (
	"context"
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
