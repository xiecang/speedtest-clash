// SPDX-License-Identifier: GPL-3.0-only
package filter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func TestCVTAndLegacySemantics(t *testing.T) {
	for _, tc := range []struct {
		expression, name string
		want             bool
	}{
		{"name:HK", "hk", true}, {"name=HK", "hk", false},
		{"name:^(?=HK)HK", "HK01", true}, {"name:(?<=HK)01", "HK01", true},
		{`name:^(HK)\1$`, "HKHK", true}, {`name:\p{Script=Han}+`, "香港", true},
		{"name:k", "K", true}, {"unknown!=x", "香港", true},
		{"name:Hong[ ]Kong", "Hong Kong", true},
		{"name:HK or name:JP and not name:bad", "HKbad", true},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			s, err := Compile(context.Background(), Options{Filter: tc.expression})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Match(context.Background(), []model.Node{{Name: tc.name}})
			if err != nil || got[0] != tc.want {
				t.Fatalf("got %v, err %v", got, err)
			}
		})
	}
	for _, pattern := range []string{"HK", "(?i)HK"} {
		s, err := Compile(context.Background(), Options{NameRegexContain: pattern})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Match(context.Background(), []model.Node{{Name: "hk"}})
		if err != nil || got[0] != (pattern == "(?i)HK") {
			t.Fatalf("legacy %s: %v %v", pattern, got, err)
		}
	}
}

func TestNestedFieldsAndJSONObjects(t *testing.T) {
	for _, tc := range []struct {
		expression string
		config     map[string]any
		want       bool
	}{
		{"tags[-1]=last", map[string]any{"tags": []any{"first", "last"}}, true},
		{"ws-opts.headers.length=2", map[string]any{"ws-opts": map[string]any{"headers": map[string]any{"Host": "host", "Extra": "x"}}}, true},
		{"$:香港", map[string]any{"nested": map[string]any{"value": "香港"}}, true},
		{`["a.b"]=value`, map[string]any{"a.b": "value"}, true},
		{"password:secret", map[string]any{"__proto__": map[string]any{"password": "secret"}}, false},
		{"length=1", map[string]any{"__proto__": map[string]any{"password": "secret"}}, false},
	} {
		s, err := Compile(context.Background(), Options{Filter: tc.expression})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Match(context.Background(), []model.Node{{Config: tc.config}})
		if err != nil || got[0] != tc.want {
			t.Fatalf("%s: %v %v", tc.expression, got, err)
		}
	}
}

func TestPruneCompletePathsAndIdentity(t *testing.T) {
	nodes := []model.Node{{Key: "entry", Revision: "r1", Name: "drop"}, {Key: "exit", Revision: "r2", Name: "keep", Dependencies: []string{"entry"}}, {Key: "chain", Name: "keep", Dependencies: []string{"exit"}}, {Key: "good", Revision: "r3", Name: "keep"}}
	s, err := Compile(context.Background(), Options{Filter: "name=keep"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Prune(context.Background(), nodes)
	if err != nil || len(got) != 1 || got[0].Key != "good" || got[0].Revision != "r3" || nodes[0].Name != "drop" {
		t.Fatalf("prune changed graph/identity: %v %v", got, err)
	}
}

func TestTimeoutAndConcurrentIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := runJS(ctx, "while(true) {}"); err == nil {
		t.Fatal("unbounded expression was not stopped")
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("node%d", i)
			s, err := Compile(context.Background(), Options{Filter: "name=" + name})
			if err != nil {
				t.Error(err)
				return
			}
			got, err := s.Match(context.Background(), []model.Node{{Name: name}, {Name: "foreign"}})
			if err != nil || !got[0] || got[1] {
				t.Errorf("isolation: %v %v", got, err)
			}
		}(i)
	}
	wg.Wait()
	for _, bad := range []string{"name:[", "name:(?i)HK", "(name:HK and)", strings.Repeat("a", 4097)} {
		if _, err := Compile(context.Background(), Options{Filter: bad}); err == nil {
			t.Fatal("invalid filter accepted")
		}
	}
}

func Test50000NodesThreeRounds(t *testing.T) {
	nodes := make([]model.Node, 50000)
	for i := range nodes {
		nodes[i] = model.Node{Key: fmt.Sprint(i), Revision: "stable", Name: fmt.Sprintf("香港 %05d", i), Config: map[string]any{"type": "vless", "server": "example.invalid", "port": 443, "uuid": "00000000-0000-0000-0000-000000000000", "tls": true, "network": "ws", "ws-opts": map[string]any{"path": "/proxy", "headers": map[string]any{"Host": "edge.invalid"}}}}
	}
	for _, opts := range []Options{{Filter: "name:香港 and type=vless and ws-opts.headers.Host=edge[.]invalid"}, {NameRegexContain: "香港", NameRegexNotContain: "does-not-match"}} {
		s, err := Compile(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		for round := 0; round < 3; round++ {
			start := time.Now()
			mask, err := s.Match(context.Background(), nodes)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, matched := range mask {
				if matched {
					count++
				}
			}
			if count != 50000 {
				t.Fatalf("round %d: %d", round, count)
			}
			t.Logf("expression=%t round=%d nodes=%d elapsed=%s", opts.Filter != "", round, count, time.Since(start))
		}
	}
}

func TestResultCacheTracksContentExpressionAndDisplayName(t *testing.T) {
	ctx := context.Background()
	node := model.Node{Key: "same-key", Revision: "same-revision", Name: "JP-new", Config: map[string]any{"server": "first.invalid"}}
	selector, err := Compile(ctx, Options{Filter: "name:🇯🇵", NameRegexContain: "^JP"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		want bool
	}{{"🇯🇵 JP-new", true}, {"🇯🇵 JP-new", true}, {"🇭🇰 JP-new", false}} {
		mask, err := selector.MatchDisplay(ctx, []model.Node{node}, map[string]string{node.Key: tc.name})
		if err != nil || mask[0] != tc.want {
			t.Fatalf("display cache: %v %v", mask, err)
		}
	}
	for _, tc := range []struct {
		expression, server string
		want               bool
	}{{"server=first[.]invalid", "first.invalid", true}, {"server=first[.]invalid", "second.invalid", false}, {"server!=first[.]invalid", "second.invalid", true}} {
		selector, err = Compile(ctx, Options{Filter: tc.expression})
		if err != nil {
			t.Fatal(err)
		}
		node.Config["server"] = tc.server
		mask, err := selector.Match(ctx, []model.Node{node})
		if err != nil || mask[0] != tc.want {
			t.Fatalf("content/expression cache: %v %v", mask, err)
		}
	}
	cache := newResultCache(2)
	a, b, c := resultKey("a", nil), resultKey("b", nil), resultKey("c", nil)
	cache.put(a, false)
	cache.put(b, true)
	cache.put(c, true)
	if _, ok := cache.get(a); ok {
		t.Fatal("cache did not evict oldest entry")
	}
	if value, ok := cache.get(b); !ok || !value || len(cache.values) != 2 {
		t.Fatal("cache lost retained entry or exceeded bound")
	}
}

func TestBatchSplitsLargeValidConfigurations(t *testing.T) {
	nodes := make([]model.Node, 130)
	for i := range nodes {
		nodes[i] = model.Node{Name: fmt.Sprintf("allow-%d", i), Config: map[string]any{"type": "http", "server": "example.invalid", "port": i + 1, "password": strings.Repeat("x", 9<<10)}}
	}
	selector, err := Compile(context.Background(), Options{Filter: "name:allow"})
	if err != nil {
		t.Fatal(err)
	}
	mask, err := selector.Match(context.Background(), nodes)
	if err != nil || len(mask) != len(nodes) {
		t.Fatalf("valid large batch rejected: %v", err)
	}
	for _, match := range mask {
		if !match {
			t.Fatal("large config was dropped")
		}
	}
	if _, err = selector.Match(context.Background(), []model.Node{{Name: "allow-big", Config: map[string]any{"value": strings.Repeat("x", 1<<20)}}}); err == nil {
		t.Fatal("oversized individual object accepted")
	}
}
