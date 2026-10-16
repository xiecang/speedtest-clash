// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestConversionSelectionDoesNotRestoreDependency(t *testing.T) {
	content := []byte("proxies:\n- {name: entry, type: http, server: entry.invalid, port: 80}\n- {name: exit, type: http, server: exit.invalid, port: 80, dialer-proxy: entry}\n- {name: good, type: http, server: good.invalid, port: 80}\n")
	for _, format := range []nq.Format{nq.ClashYAML, nq.SingBoxJSON, nq.URI} {
		data, _, err := nq.ConvertSelected(context.Background(), content, format, nq.SelectionOptions{Filter: "name!=entry"})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := nq.Decode(data)
		if err != nil || len(decoded.Nodes) != 1 || decoded.Nodes[0].Name != "good" {
			t.Fatalf("%s: %v %v", format, decoded, err)
		}
	}
	h := nq.Handler(nq.DefaultPlan(), nq.NewBudget(0), nq.ProbeOptions{Token: "secret"})
	request := httptest.NewRequest("POST", "/api/v2/convert?format=clash-yaml&filter="+url.QueryEscape("name!=entry"), strings.NewReader(string(content)))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 || strings.Contains(response.Body.String(), "exit.invalid") {
		t.Fatalf("HTTP filter failed: %d", response.Code)
	}
}
