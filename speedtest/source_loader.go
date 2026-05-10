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

type ProxyBatchHandler func([]map[string]any) error

const defaultSourceBatchSize = 200

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

// LoadManyStream loads sources and calls fn as proxy batches become available.
// Sources that fail are skipped (error is logged); fn errors abort immediately.
func (l *ProxySourceLoader) LoadManyStream(ctx context.Context, sources []string, fn func([]map[string]any) error) error {
	return l.LoadManyStreamBatched(ctx, sources, l.sourceBatchSize(), fn)
}

func (l *ProxySourceLoader) LoadManyStreamBatched(ctx context.Context, sources []string, batchSize int, fn ProxyBatchHandler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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

	concurrency := l.sourceConcurrency()
	if concurrency > len(parts) {
		concurrency = len(parts)
	}
	partsCh := make(chan string)
	errCh := make(chan error, 1)
	var callbackMu sync.Mutex
	var callbackErr error
	callback := func(batch []map[string]any) error {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if callbackErr != nil {
			return callbackErr
		}
		if err := fn(batch); err != nil {
			callbackErr = err
			cancel()
			select {
			case errCh <- err:
			default:
			}
			return err
		}
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for part := range partsCh {
				if err := l.LoadStream(ctx, part, batchSize, callback); err != nil {
					callbackMu.Lock()
					aborted := callbackErr != nil
					callbackMu.Unlock()
					if aborted || ctx.Err() != nil {
						return
					}
					warnf(l.Options, "load source error (skipped): %s", err)
				}
			}
		}()
	}

	go func() {
		defer close(partsCh)
		for _, part := range parts {
			select {
			case <-ctx.Done():
				return
			case partsCh <- part:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		callbackMu.Lock()
		err := callbackErr
		callbackMu.Unlock()
		if err != nil {
			return err
		}
		return ctx.Err()
	case err, ok := <-errCh:
		if !ok {
			return nil
		}
		return err
	}
}

func (l *ProxySourceLoader) Load(ctx context.Context, source string) ([]map[string]any, error) {
	body, err := l.readSource(ctx, source)
	if err != nil {
		return nil, err
	}
	return l.parse(ctx, body)
}

func (l *ProxySourceLoader) LoadStream(ctx context.Context, source string, batchSize int, fn ProxyBatchHandler) error {
	body, err := l.readSource(ctx, source)
	if err != nil {
		return err
	}
	return l.parseStream(ctx, body, batchSize, fn)
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
	var out []map[string]any
	err := l.parseStream(ctx, body, l.sourceBatchSize(), func(batch []map[string]any) error {
		out = append(out, batch...)
		return nil
	})
	return out, err
}

func (l *ProxySourceLoader) parseStream(ctx context.Context, body []byte, batchSize int, fn ProxyBatchHandler) error {
	rawCfg := &models.RawConfig{
		Proxies: []map[string]any{},
	}
	emit := newProxyBatchEmitter(batchSize, fn)
	if !bytes.Contains(body, []byte("server")) {
		if bytes.Contains(body, []byte("://")) {
			body = []byte(base64.StdEncoding.EncodeToString(body))
		}
		proxyList, err := convert.ConvertsV2Ray(body)
		if err != nil {
			return fmt.Errorf("convert proxies: %w", err)
		}
		for _, proxy := range proxyList {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit.Add(proxy); err != nil {
				return err
			}
		}
		return emit.Flush()
	}
	if err := yaml.Unmarshal(body, rawCfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	for _, proxy := range rawCfg.Proxies {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit.Add(proxy); err != nil {
			return err
		}
	}
	if err := emit.Flush(); err != nil {
		return err
	}
	for name, config := range rawCfg.Providers {
		if name == provider.ReservedName {
			warnf(l.Options, "can not defined a provider called `%s`", provider.ReservedName)
			continue
		}
		if err := l.LoadStream(ctx, config.Url, batchSize, fn); err != nil {
			return err
		}
	}
	return nil
}

func (l *ProxySourceLoader) sourceConcurrency() int {
	if l.Options != nil && l.Options.SourceConcurrency > 0 {
		return l.Options.SourceConcurrency
	}
	return 1
}

func (l *ProxySourceLoader) sourceBatchSize() int {
	if l.Options != nil && l.Options.SourceBatchSize > 0 {
		return l.Options.SourceBatchSize
	}
	return defaultSourceBatchSize
}

type proxyBatchEmitter struct {
	batchSize int
	fn        ProxyBatchHandler
	batch     []map[string]any
}

func newProxyBatchEmitter(batchSize int, fn ProxyBatchHandler) *proxyBatchEmitter {
	if batchSize <= 0 {
		batchSize = defaultSourceBatchSize
	}
	return &proxyBatchEmitter{
		batchSize: batchSize,
		fn:        fn,
		batch:     make([]map[string]any, 0, batchSize),
	}
}

func (e *proxyBatchEmitter) Add(proxy map[string]any) error {
	e.batch = append(e.batch, proxy)
	if len(e.batch) < e.batchSize {
		return nil
	}
	return e.Flush()
}

func (e *proxyBatchEmitter) Flush() error {
	if len(e.batch) == 0 {
		return nil
	}
	batch := e.batch
	e.batch = make([]map[string]any, 0, e.batchSize)
	return e.fn(batch)
}
