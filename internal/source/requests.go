// SPDX-License-Identifier: GPL-3.0-only
package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/model"
)

var errSourceBudget = errors.New("source resource budget exhausted")
var errSourceStorage = errors.New("source spool storage failed")

type SourceRequest struct {
	Source          string `json:"source"`
	AllowLocalFiles bool   `json:"allow_local_files,omitempty"`
	UserAgent       string `json:"user_agent,omitempty"`
	Proxy           string `json:"proxy,omitempty"`
}

type SourceResult struct {
	Index   int         `json:"index"`
	Error   string      `json:"error,omitempty"`
	Headers http.Header `json:"headers,omitempty"`
	Bytes   int         `json:"bytes"`
}

// LoadSourcesDetailed preserves order and validates all dependencies before emitting.
// Failed source documents are rolled back without losing their healthy siblings.
func LoadSourcesDetailed(ctx context.Context, requests []SourceRequest, client *http.Client, emit func([]model.Node) error) (SourceSummary, error) {
	return loadSources(ctx, requests, client, emit, true)
}

func loadSources(ctx context.Context, requests []SourceRequest, client *http.Client, emit func([]model.Node) error, partial bool) (SourceSummary, error) {
	if len(requests) == 0 || len(requests) > 64 {
		return SourceSummary{}, errors.New("expected 1..64 sources")
	}
	file, err := os.CreateTemp("", "node-quality-source-*")
	if err != nil {
		return SourceSummary{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	spool := sourceSpool{file: file, names: map[string][]int{}}
	totalBytes, allowEmpty := 0, false
	for index, request := range requests {
		if err := ctx.Err(); err != nil {
			return spool.summary, err
		}
		result := SourceResult{Index: index, Headers: http.Header{}}
		source := strings.TrimSpace(request.Source)
		if source == "empty" {
			allowEmpty = true
			spool.summary.Sources = append(spool.summary.Sources, result)
			continue
		}
		c, closeTransport, err := sourceClient(client, request, &result)
		if err != nil {
			return spool.summary, err
		}
		start, offset, before := len(spool.slots), spool.bytes, spool.summary
		visited := map[string]bool{}
		var load func(string, int, string) error
		load = func(address string, depth int, path string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if depth > 8 || visited[address] {
				return errors.New("provider depth or cycle limit exceeded")
			}
			visited[address] = true
			defer delete(visited, address)
			if !request.AllowLocalFiles && !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") && !strings.HasPrefix(address, "data:") && !strings.HasPrefix(address, "inline:") {
				return errors.New("local source files are disabled")
			}
			content, err := readSourceContent(ctx, address, c)
			totalBytes += len(content)
			result.Bytes += len(content)
			if totalBytes > codec.MaxSourceBytes {
				return fmt.Errorf("%w: sources and providers exceed 16MiB", errSourceBudget)
			}
			if err != nil {
				return err
			}
			providers, err := spool.decode(content)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(providers))
			for name := range providers {
				names = append(names, name)
			}
			sort.Strings(names)
			for childIndex, name := range names {
				child := providers[name]
				var childErr error
				if parent, e := url.Parse(address); e == nil && parent.Host != "" {
					relative, e := url.Parse(child)
					if e != nil {
						childErr = errors.New("invalid provider URL")
					} else {
						child = parent.ResolveReference(relative).String()
					}
				}
				childPath := fmt.Sprintf("%s/%d", path, childIndex)
				start, offset, before := len(spool.slots), spool.bytes, spool.summary
				if childErr == nil {
					childErr = load(child, depth+1, childPath)
				}
				if childErr != nil {
					if fatalSourceError(ctx, childErr) {
						return childErr
					}
					if err := spool.rollback(start, offset); err != nil {
						return err
					}
					spool.summary = before
					spool.sourceFailure(childPath, "provider_failed", childErr.Error())
				}
			}
			return nil
		}
		err = load(source, 0, fmt.Sprint(index))
		closeTransport()
		if err != nil {
			if !partial || fatalSourceError(ctx, err) {
				return spool.summary, err
			}
			if e := spool.rollback(start, offset); e != nil {
				return spool.summary, e
			}
			spool.summary = before
			result.Error = err.Error()
			result.Headers = nil
			spool.sourceFailure(fmt.Sprint(index), "source_failed", result.Error)
		}
		spool.summary.Sources = append(spool.summary.Sources, result)
	}
	return spool.finish(ctx, emit, allowEmpty)
}

func fatalSourceError(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, errSourceBudget) || errors.Is(err, errSourceStorage)
}

func (s *sourceSpool) sourceFailure(path, code, message string) {
	s.summary.Partial = true
	if len(s.summary.Diagnostics) < 100 {
		s.summary.Diagnostics = append(s.summary.Diagnostics, model.Diagnostic{Node: path, Code: code, Message: message})
	}
}

func (s *sourceSpool) providerURLs(value any) map[string]string {
	providers := map[string]string{}
	entries, ok := value.(map[string]any)
	if !ok {
		s.sourceFailure("", "provider_failed", "providers must be a mapping")
		return providers
	}
	for name, entry := range entries {
		config, _ := entry.(map[string]any)
		address, _ := config["url"].(string)
		if strings.TrimSpace(address) == "" {
			s.sourceFailure("", "provider_failed", "provider URL required")
			continue
		}
		providers[name] = address
	}
	return providers
}

func (s *sourceSpool) rollback(start int, offset int64) error {
	for _, slot := range s.slots[start:] {
		indices := s.names[slot.name]
		for len(indices) > 0 && indices[len(indices)-1] >= start {
			indices = indices[:len(indices)-1]
		}
		if len(indices) == 0 {
			delete(s.names, slot.name)
		} else {
			s.names[slot.name] = indices
		}
	}
	s.slots = s.slots[:start]
	s.bytes = offset
	if err := s.file.Truncate(offset); err != nil {
		return fmt.Errorf("%w: %w", errSourceStorage, err)
	}
	_, err := s.file.Seek(offset, 0)
	if err != nil {
		return fmt.Errorf("%w: %w", errSourceStorage, err)
	}
	return err
}

type sourceTransport struct {
	base     http.RoundTripper
	ua       string
	result   *SourceResult
	captured bool
}

func (t *sourceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header = req.Header.Clone()
	if t.ua != "" {
		request.Header.Set("User-Agent", t.ua)
	}
	response, err := t.base.RoundTrip(request)
	if err == nil && response.StatusCode == http.StatusOK && !t.captured {
		t.captured = true
		for _, name := range []string{"Subscription-Userinfo", "Profile-Update-Interval", "Profile-Web-Page-Url", "Content-Disposition"} {
			if v := response.Header.Get(name); len(v) <= 4096 && !strings.ContainsAny(v, "\r\n") && v != "" {
				t.result.Headers.Set(name, v)
			}
		}
	}
	return response, err
}

func sourceClient(client *http.Client, request SourceRequest, result *SourceResult) (*http.Client, func(), error) {
	c := http.Client{Timeout: 30 * time.Second}
	if client != nil {
		c = *client
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	closeTransport := func() {}
	if request.Proxy != "" {
		proxy, err := url.Parse(request.Proxy)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") {
			return nil, nil, errors.New("invalid source proxy")
		}
		base, ok := transport.(*http.Transport)
		if !ok {
			return nil, nil, errors.New("source proxy requires HTTP transport")
		}
		cloned := base.Clone()
		cloned.Proxy = http.ProxyURL(proxy)
		transport = cloned
		closeTransport = cloned.CloseIdleConnections
	}
	c.Transport = &sourceTransport{base: transport, ua: request.UserAgent, result: result}
	return &c, closeTransport, nil
}
