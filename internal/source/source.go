// SPDX-License-Identifier: GPL-3.0-only
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

// LoadSource bounds input size and recursive providers. URLs are never included
// in errors, because subscriptions commonly contain credentials.
func LoadSource(ctx context.Context, source string, client *http.Client, emit func([]model.Node) error) error {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	visited := make(map[string]bool)
	var all []model.Node
	var sourceBytes int
	collected := make(map[string]bool)
	var load func(string, int) error
	load = func(source string, depth int) error {
		if depth > 8 {
			return errors.New("provider depth exceeds 8")
		}
		if visited[source] {
			return errors.New("provider cycle detected")
		}
		visited[source] = true
		defer delete(visited, source)
		if err := ctx.Err(); err != nil {
			return err
		}
		var reader io.ReadCloser
		if strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://") {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
			if err != nil {
				return errors.New("invalid source URL")
			}
			response, err := client.Do(request)
			if err != nil {
				return errors.New("source fetch failed")
			}
			if response.StatusCode != http.StatusOK {
				response.Body.Close()
				return fmt.Errorf("source HTTP status %d", response.StatusCode)
			}
			reader = response.Body
		} else if strings.HasPrefix(source, "inline:") {
			reader = io.NopCloser(bytes.NewBufferString(strings.TrimPrefix(source, "inline:")))
		} else {
			file, err := os.Open(source)
			if err != nil {
				return errors.New("source file open failed")
			}
			reader = file
		}
		content, err := io.ReadAll(io.LimitReader(reader, codec.MaxSourceBytes+1))
		closeErr := reader.Close()
		if err != nil {
			return errors.New("source read failed")
		}
		if closeErr != nil {
			return closeErr
		}
		sourceBytes += len(content)
		if len(content) > codec.MaxSourceBytes || sourceBytes > codec.MaxSourceBytes {
			return errors.New("source and recursive providers exceed 16MiB")
		}
		decoded, err := codec.Decode(content)
		if err != nil {
			return err
		}
		if len(decoded.Diagnostics) > 0 {
			return fmt.Errorf("source has %d unsupported entries; first reason: %s", len(decoded.Diagnostics), decoded.Diagnostics[0].Message)
		}
		for _, node := range decoded.Nodes {
			if !collected[node.Key] {
				all = append(all, node)
				collected[node.Key] = true
			}
			if len(all) > 50000 {
				return errors.New("source exceeds 50000 normalized nodes")
			}
		}
		providerNames := make([]string, 0, len(decoded.Providers))
		for name := range decoded.Providers {
			providerNames = append(providerNames, name)
		}
		sort.Strings(providerNames)
		for _, name := range providerNames {
			address := decoded.Providers[name]
			if parent, err := url.Parse(source); err == nil && parent.Host != "" {
				relative, e := url.Parse(address)
				if e != nil {
					return errors.New("invalid provider URL")
				}
				address = parent.ResolveReference(relative).String()
			}
			if err = load(address, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := load(source, 0); err != nil {
		return err
	}
	if len(all) == 0 {
		return errors.New("source contains no nodes")
	}
	if err := model.ValidateDependencies(all); err != nil {
		return err
	}
	// Parse the whole generation before emitting relationships. Build the index once.
	index := make(map[string]model.Node, len(all))
	for _, n := range all {
		index[n.Key] = n
	}
	for start := 0; start < len(all); {
		included := make(map[string]bool, 200)
		batch := make([]model.Node, 0, 200)
		end := start
		for end < len(all) {
			if err := ctx.Err(); err != nil {
				return err
			}
			added := make([]model.Node, 0)
			pending := make(map[string]bool)
			var add func(model.Node) bool
			add = func(n model.Node) bool {
				if included[n.Key] || pending[n.Key] {
					return true
				}
				if len(included)+len(pending) >= 200 {
					return false
				}
				pending[n.Key] = true
				for _, dep := range n.Dependencies {
					if !add(index[dep]) {
						return false
					}
				}
				added = append(added, n)
				return true
			}
			if !add(all[end]) {
				break
			}
			for _, n := range added {
				included[n.Key] = true
			}
			batch = append(batch, added...)
			end++
		}
		if end == start {
			return errors.New("proxy chain exceeds 200 nodes")
		}
		if err := emit(batch); err != nil {
			return err
		}
		start = end
	}

	return nil
}
