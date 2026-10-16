// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"encoding/base64"
	"reflect"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestDetailedSourceAnnouncementDoesNotDiscardURIEntries(t *testing.T) {
	const uri = "ss://YWVzLTEyOC1nY206Zml4dHVyZS1zZWNyZXQ@example.invalid:443#Tokyo"
	var baseline []nq.Node
	_, err := nq.LoadSourceDetailed(context.Background(), "inline:"+uri, nil, func(nodes []nq.Node) error {
		baseline = append(baseline, nodes...)
		return nil
	})
	if err != nil || len(baseline) != 1 {
		t.Fatal(baseline, err)
	}
	text := "订阅公告：请及时更新\n# comment\n\nunknown://example.invalid:99\n" + uri + "\n"
	for _, input := range []string{text, base64.StdEncoding.EncodeToString([]byte(text)), base64.RawURLEncoding.EncodeToString([]byte(text))} {
		var got []nq.Node
		summary, err := nq.LoadSourceDetailed(context.Background(), "inline:"+input, nil, func(nodes []nq.Node) error {
			got = append(got, nodes...)
			return nil
		})
		if err != nil || !reflect.DeepEqual(got, baseline) || summary.Unsupported != 2 {
			t.Fatalf("announcement changed node parameters: nodes=%d summary=%+v err=%v", len(got), summary, err)
		}
	}
}

func TestDetailedSourceDamagedDocumentCannotFallBackToURIList(t *testing.T) {
	for _, input := range []string{
		"proxies: [broken\nhttp://example.invalid:8080#good",
		"\"proxies\": [broken\nhttp://example.invalid:8080#good",
		"{\"outbounds\":[\nhttp://example.invalid:8080#good",
		"---\n proxies: [broken\nhttp://example.invalid:8080#good",
		"# comment\n proxies: [broken\nhttp://example.invalid:8080#good",
		"proxies : [broken\nhttp://example.invalid:8080#good",
		"\"proxies\" : [broken\nhttp://example.invalid:8080#good",
		base64.StdEncoding.EncodeToString([]byte(" \n{\"outbounds\":[broken\nhttp://example.invalid:8080#good")),
		"\ufeffproxies: [broken\nhttp://example.invalid:8080#good",
		base64.StdEncoding.EncodeToString([]byte("\ufeff{\"outbounds\":[broken\nhttp://example.invalid:8080#good")),
	} {
		emitted := false
		_, err := nq.LoadSourceDetailed(context.Background(), "inline:"+input, nil, func([]nq.Node) error { emitted = true; return nil })
		if err == nil || emitted {
			t.Fatal("damaged document emitted a partial generation", err)
		}
	}
}
