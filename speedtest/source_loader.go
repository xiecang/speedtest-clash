package speedtest

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
)

type ProxySourceLoader struct {
	ProxyURL *url.URL
	Options  *models.Options
}

func (l *ProxySourceLoader) LoadMany(ctx context.Context, sources []string) ([]map[string]any, error) {
	var out []map[string]any
	for _, source := range sources {
		for _, part := range strings.Split(source, "|") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			proxies, err := l.Load(ctx, part)
			if err != nil {
				return out, err
			}
			out = append(out, proxies...)
		}
	}
	return out, nil
}

// LoadManyStream concurrently loads all sources and calls fn with each batch of proxies
// as soon as a source finishes, without waiting for the others.
// Sources that fail are skipped (error is logged); fn errors abort immediately.
func (l *ProxySourceLoader) LoadManyStream(ctx context.Context, sources []string, fn func([]map[string]any) error) error {
	var parts []string
	for _, source := range sources {
		for _, part := range strings.Split(source, "|") {
			part = strings.TrimSpace(part)
			if part != "" {
				parts = append(parts, part)
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}

	type result struct {
		proxies []map[string]any
		err     error
	}
	resultCh := make(chan result, len(parts))

	var wg sync.WaitGroup
	for _, part := range parts {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			proxies, err := l.Load(ctx, p)
			select {
			case resultCh <- result{proxies, err}:
			case <-ctx.Done():
			}
		}(part)
	}
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for r := range resultCh {
		if r.err != nil {
			warnf(l.Options, "load source error (skipped): %s", r.err)
			continue
		}
		if len(r.proxies) > 0 {
			if err := fn(r.proxies); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *ProxySourceLoader) Load(ctx context.Context, source string) ([]map[string]any, error) {
	body, err := l.readSource(ctx, source)
	if err != nil {
		return nil, err
	}
	return l.parse(ctx, body)
}

func (l *ProxySourceLoader) readSource(ctx context.Context, source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, err := requests.Request(ctx, &requests.RequestOption{
			Method:             http.MethodGet,
			URL:                source,
			Headers:            map[string]string{"User-Agent": "clash-meta"},
			Timeout:            60 * time.Second,
			RetryTimes:         3,
			RetryTimeOut:       3 * time.Second,
			ProxyUrl:           l.ProxyURL,
			InsecureSkipVerify: true,
			Logger:             loggerFromOptions(l.Options),
		})
		if err != nil {
			return nil, fmt.Errorf("fetch config %s: %w", source, err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch config %s: status code %d", source, resp.StatusCode)
		}
		return resp.Body, nil
	}

	body, err := os.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read local file %s: %w", source, err)
	}
	return body, nil
}

func (l *ProxySourceLoader) parse(ctx context.Context, body []byte) ([]map[string]any, error) {
	rawCfg := &models.RawConfig{
		Proxies: []map[string]any{},
	}
	if !bytes.Contains(body, []byte("server")) {
		if bytes.Contains(body, []byte("://")) {
			body = []byte(base64.StdEncoding.EncodeToString(body))
		}
		proxyList, err := convert.ConvertsV2Ray(body)
		if err != nil {
			return nil, fmt.Errorf("convert proxies: %w", err)
		}
		return proxyList, nil
	}
	if err := yaml.Unmarshal(body, rawCfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	out := append([]map[string]any(nil), rawCfg.Proxies...)
	for name, config := range rawCfg.Providers {
		if name == provider.ReservedName {
			warnf(l.Options, "can not defined a provider called `%s`", provider.ReservedName)
			continue
		}
		proxies, err := l.Load(ctx, config.Url)
		if err != nil {
			return out, err
		}
		out = append(out, proxies...)
	}
	return out, nil
}
