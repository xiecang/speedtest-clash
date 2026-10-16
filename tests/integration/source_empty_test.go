// SPDX-License-Identifier: GPL-3.0-only
package integration

import (
	"context"
	"errors"
	nq "github.com/xiecang/node-quality/v2"
	"testing"
)

func TestEmptySourceIsDistinctFromLoadFailure(t *testing.T) {
	for _, test := range []struct {
		input string
		empty bool
	}{{"proxies: []", true}, {`{"outbounds":[]}`, true}, {"proxies: [broken", false}, {"proxy-providers: {bad: {url: /private}}", false}} {
		_, err := nq.LoadSourceRequestDetailed(context.Background(), nq.SourceRequest{Source: "inline:" + test.input}, nil, func([]nq.Node) error { return nil })
		if err == nil || errors.Is(err, nq.ErrNoNodes) != test.empty {
			t.Fatal(test.input, err)
		}
	}
}
