// SPDX-License-Identifier: GPL-3.0-only
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	type rawNodeKey struct {
		name, key, dep string
	}
	var rawNodes []model.Node
	var sourceBytes int
	seenRaw := make(map[rawNodeKey]bool)
	var load func(string, int) error
	load = func(source string, depth int) error {
		if depth > 8 {
			return errors.New("provider depth exceeds 8")
		}
		if !isRemoteHTTPURL(source) && !strings.HasPrefix(source, "inline:") {
			source = filepath.Clean(source)
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
			request.Header.Set("User-Agent", ClashUserAgent(""))
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
		decoded, err := codec.DecodeUnresolved(content)
		if err != nil {
			return err
		}
		if len(decoded.Diagnostics) > 0 {
			return fmt.Errorf("source has %d unsupported entries; first reason: %s", len(decoded.Diagnostics), decoded.Diagnostics[0].Message)
		}
		for _, node := range decoded.Nodes {
			depField := "dialer-proxy"
			if node.Format == model.SingBoxJSON {
				depField = "detour"
			}
			dep, _ := node.Config[depField].(string)
			rk := rawNodeKey{name: node.Name, key: node.Key, dep: dep}
			if !seenRaw[rk] {
				rawNodes = append(rawNodes, node)
				seenRaw[rk] = true
			}
			if len(rawNodes) > 50000 {
				return errors.New("source exceeds 50000 normalized nodes")
			}
		}
		providerNames := make([]string, 0, len(decoded.Providers))
		for name := range decoded.Providers {
			providerNames = append(providerNames, name)
		}
		sort.Strings(providerNames)
		for _, name := range providerNames {
			address, err := resolveProviderAddress(source, decoded.Providers[name])
			if err != nil {
				return err
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
	if len(rawNodes) == 0 {
		return errors.New("source contains no nodes")
	}
	if err := model.ResolveDependencies(rawNodes); err != nil {
		return err
	}
	collected := make(map[string]bool, len(rawNodes))
	all := make([]model.Node, 0, len(rawNodes))
	for _, node := range rawNodes {
		if !collected[node.Key] {
			all = append(all, node)
			collected[node.Key] = true
		}
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
