// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	nq "github.com/xiecang/node-quality/v2"
)

func TestNativeCheckLargeRulesPreservesValidation(t *testing.T) {
	rules := make([]any, 2000)
	for i := range rules {
		rules[i] = map[string]any{"domain_suffix": fmt.Sprintf("%d.example.invalid", i), "action": "reject"}
	}
	document := map[string]any{
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"rules": rules, "final": "direct"},
	}
	check := func(wantError bool) {
		t.Helper()
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		// CheckSingBox has always accepted the native parser's JSON comments.
		data = []byte(strings.Replace(string(data), "{", "{ // native config\n", 1))
		if err = nq.CheckSingBox(context.Background(), data); (err != nil) != wantError {
			t.Fatalf("native validation error=%v, wantError=%v", err, wantError)
		}
	}
	check(false)
	rules[len(rules)-1] = map[string]any{"domain_suffix": "invalid.example", "action": "unsupported-action"}
	check(true)
	rules[len(rules)-1] = map[string]any{"domain_regex": "[", "action": "reject"}
	check(true)
	rules[len(rules)-1] = map[string]any{"domain_suffix": "invalid.example", "action": "reject"}
	document["unknown_section"] = true
	check(true)
}
