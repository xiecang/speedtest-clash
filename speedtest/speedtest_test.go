package speedtest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/xiecang/speedtest-clash/speedtest/models"
)

type mockCache struct {
	results map[string]*models.CProxyWithResult
}

func (m *mockCache) Get(ctx context.Context, key string) (*models.CProxyWithResult, bool) {
	res, ok := m.results[key]
	return res, ok
}

func (m *mockCache) Set(ctx context.Context, key string, result *models.CProxyWithResult) error {
	m.results[key] = result
	return nil
}

func (m *mockCache) GenerateKey(proxy *models.CProxy) string {
	return proxy.Name()
}

func (m *mockCache) Close() error { return nil }

func TestProxyDisableBandwidthTestSkipsDownload(t *testing.T) {
	p := &proxyTest{
		option: &models.Options{DisableBandwidthTest: true},
	}

	ttfb, bandwidth, downloadBytes, err := p.testBandwidthIfEnabled(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), ttfb)
	assert.Equal(t, float64(0), bandwidth)
	assert.Equal(t, int64(0), downloadBytes)
}

type firstReadRecorder struct {
	start     time.Time
	firstRead time.Time
	once      sync.Once
	data      []byte
}

func (r *firstReadRecorder) Read(p []byte) (int, error) {
	r.once.Do(func() {
		r.firstRead = time.Now()
	})
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestCopyLimitedThrottlesBeforeReadingFirstChunk(t *testing.T) {
	reader := &firstReadRecorder{
		start: time.Now(),
		data:  make([]byte, 32*1024),
	}
	p := &proxyTest{
		option:           &models.Options{},
		bandwidthLimiter: models.NewBandwidthLimiter(128 * 1024),
	}

	n, err := p.copyLimited(context.Background(), io.Discard, reader)

	assert.NoError(t, err)
	assert.Equal(t, int64(32*1024), n)
	if elapsed := reader.firstRead.Sub(reader.start); elapsed < 100*time.Millisecond {
		t.Fatalf("first Read happened after %s, want it throttled before network read", elapsed)
	}
}

func TestPercentileDelay(t *testing.T) {
	delays := []uint16{10, 20, 30, 40, 50}

	assert.Equal(t, uint16(30), percentileDelay(delays, 0.50))
	assert.Equal(t, uint16(50), percentileDelay(delays, 0.90))
	assert.Equal(t, uint16(50), percentileDelay(delays, 0.95))
}

type scriptedLatencyStep struct {
	delay  time.Duration
	status int
	err    error
}

type scriptedLatencyTransport struct {
	steps []scriptedLatencyStep
	index int
}

func enableLatencyMetrics(t *testing.T, opts *models.Options) {
	t.Helper()

	value := reflect.ValueOf(opts).Elem().FieldByName("EnableLatencyMetrics")
	if !value.IsValid() {
		t.Fatal("Options.EnableLatencyMetrics field is missing")
	}
	if value.Kind() != reflect.Bool || !value.CanSet() {
		t.Fatal("Options.EnableLatencyMetrics must be a settable bool")
	}

	value.SetBool(true)
}

func (t *scriptedLatencyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.index >= len(t.steps) {
		return nil, fmt.Errorf("unexpected request %d", t.index)
	}
	step := t.steps[t.index]
	t.index++

	if step.delay > 0 {
		timer := time.NewTimer(step.delay)
		defer timer.Stop()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}

	if step.err != nil {
		return nil, step.err
	}

	return &http.Response{
		StatusCode: step.status,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestTestURLDefaultModeLeavesLatencyMetricsUnset(t *testing.T) {
	p := &proxyTest{
		name:   "proxy",
		option: &models.Options{},
		client: &http.Client{Transport: &scriptedLatencyTransport{steps: []scriptedLatencyStep{
			{delay: 20 * time.Millisecond, status: http.StatusNoContent},
		}}},
	}

	stats, err := p.testURL(context.Background(), "https://example.com/generate_204", 1)

	assert.NoError(t, err)
	assert.NotZero(t, stats.min)
	assert.Zero(t, stats.p50)
	assert.Zero(t, stats.p90)
	assert.Zero(t, stats.p95)
	assert.Zero(t, stats.jitter)
	assert.Zero(t, stats.lossRate)
}

func TestTestURLOmitsPercentilesWhenMeasuredLatencySamplesAreInsufficient(t *testing.T) {
	opts := &models.Options{LatencySamples: 2}
	enableLatencyMetrics(t, opts)

	p := &proxyTest{
		name:   "proxy",
		option: opts,
		client: &http.Client{Transport: &scriptedLatencyTransport{steps: []scriptedLatencyStep{
			{delay: 20 * time.Millisecond, status: http.StatusNoContent},
			{delay: 40 * time.Millisecond, status: http.StatusNoContent},
			{err: context.DeadlineExceeded},
		}}},
	}

	stats, err := p.testURL(context.Background(), "https://example.com/generate_204", 1)

	assert.NoError(t, err)
	assert.NotZero(t, stats.min)
	assert.Zero(t, stats.p50)
	assert.Zero(t, stats.p90)
	assert.Zero(t, stats.p95)
	assert.Zero(t, stats.jitter)
	assert.InDelta(t, 0.5, stats.lossRate, 0.001)
}

func TestTestURLComputesPercentilesFromMeasuredLatencySamples(t *testing.T) {
	opts := &models.Options{LatencySamples: 3}
	enableLatencyMetrics(t, opts)

	p := &proxyTest{
		name:   "proxy",
		option: opts,
		client: &http.Client{Transport: &scriptedLatencyTransport{steps: []scriptedLatencyStep{
			{delay: 10 * time.Millisecond, status: http.StatusNoContent},
			{delay: 40 * time.Millisecond, status: http.StatusNoContent},
			{delay: 80 * time.Millisecond, status: http.StatusNoContent},
			{delay: 120 * time.Millisecond, status: http.StatusNoContent},
		}}},
	}

	stats, err := p.testURL(context.Background(), "https://example.com/generate_204", 1)

	assert.NoError(t, err)
	assert.NotZero(t, stats.min)
	assert.Greater(t, stats.p50, uint16(0))
	assert.Greater(t, stats.p90, stats.p50)
	assert.Greater(t, stats.p95, stats.p50)
	assert.Greater(t, stats.jitter, uint16(0))
	assert.Zero(t, stats.lossRate)
}

func TestNewTestDefaultsLoggerAndExposesAliveCount(t *testing.T) {
	tester, err := NewTest(models.Options{
		Proxies: []map[string]any{
			{"name": "proxy1", "type": "ss", "server": "1.1.1.1", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
		},
	})
	if err != nil {
		t.Fatalf("NewTest returned error: %v", err)
	}
	defer tester.Close()

	field := reflect.ValueOf(tester.options).Elem().FieldByName("Logger")
	if !field.IsValid() {
		t.Fatal("Options.Logger field is missing")
	}
	if field.IsNil() {
		t.Fatal("Options.Logger should default to a non-nil logger")
	}
	if got := tester.options.Progress.ProgressInterval; got != 3*time.Second {
		t.Fatalf("ProgressInterval = %s, want %s", got, 3*time.Second)
	}

	method, ok := reflect.TypeOf(tester).MethodByName("AliveCount")
	if !ok {
		t.Fatal("AliveCount method is missing")
	}
	values := method.Func.Call([]reflect.Value{reflect.ValueOf(tester)})
	if len(values) != 1 {
		t.Fatalf("AliveCount returned %d values, want 1", len(values))
	}
	if got := values[0].Interface().(int32); got != 0 {
		t.Fatalf("AliveCount() = %d, want 0", got)
	}
}

func TestOptionsUseMBPerSecLimitAndRemoveKeepOpen(t *testing.T) {
	optionType := reflect.TypeOf(models.Options{})
	if _, ok := optionType.FieldByName("KeepOpen"); ok {
		t.Fatal("Options.KeepOpen should be removed")
	}
	if _, ok := optionType.FieldByName("MaxBandwidthBytesPerSec"); ok {
		t.Fatal("Options.MaxBandwidthBytesPerSec should be replaced")
	}
	field, ok := optionType.FieldByName("MaxBandwidthMBPerSec")
	if !ok {
		t.Fatal("Options.MaxBandwidthMBPerSec is missing")
	}
	if field.Type.Kind() != reflect.Float64 {
		t.Fatalf("MaxBandwidthMBPerSec kind = %s, want float64", field.Type.Kind())
	}
	if got := field.Tag.Get("json"); got != "max_bandwidth_mb_per_sec" {
		t.Fatalf("MaxBandwidthMBPerSec json tag = %q", got)
	}
}

func TestRunStreamAcceptsProxiesAddedAfterRun(t *testing.T) {
	cache := &mockCache{results: map[string]*models.CProxyWithResult{
		"stream-proxy": {Result: models.Result{Name: "stream-proxy", Delay: 100}},
	}}
	tester, err := NewTest(models.Options{
		Concurrent: 1,
		Cache:      cache,
		Timeout:    time.Second,
	})
	assert.NoError(t, err)
	defer tester.Close()

	resultsCh, err := tester.RunStream(context.Background())
	assert.NoError(t, err)

	err = tester.AddProxies(context.Background(), []map[string]any{
		{"name": "stream-proxy", "type": "ss", "server": "1.1.1.1", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
	})
	assert.NoError(t, err)
	tester.CloseInput()

	var results []*models.CProxyWithResult
	for result := range resultsCh {
		results = append(results, result)
	}

	assert.Len(t, results, 1)
	assert.Equal(t, "stream-proxy", results[0].Result.Name)
}

func TestRunStreamAcceptsProxyURLAddedAfterRun(t *testing.T) {
	cache := &mockCache{results: map[string]*models.CProxyWithResult{
		"url-proxy": {Result: models.Result{Name: "url-proxy", Delay: 100}},
	}}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "proxies.yaml")
	err := os.WriteFile(configPath, []byte(`
proxies:
  - name: url-proxy
    type: ss
    server: 1.1.1.1
    port: 8388
    cipher: aes-128-gcm
    password: pass
`), 0o600)
	assert.NoError(t, err)

	tester, err := NewTest(models.Options{
		Concurrent: 1,
		Cache:      cache,
		Timeout:    time.Second,
	})
	assert.NoError(t, err)
	defer tester.Close()

	resultsCh, err := tester.RunStream(context.Background())
	assert.NoError(t, err)
	assert.NoError(t, tester.AddProxyURL(context.Background(), configPath))
	tester.CloseInput()

	var results []*models.CProxyWithResult
	for result := range resultsCh {
		results = append(results, result)
	}

	assert.Len(t, results, 1)
	assert.Equal(t, "url-proxy", results[0].Result.Name)
}

func TestCandidateLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cache := &mockCache{results: map[string]*models.CProxyWithResult{
		"proxy1": {Result: models.Result{Name: "proxy1", Delay: 100}},
		"proxy2": {Result: models.Result{Name: "proxy2", Delay: 200}},
	}}
	tester, err := NewTest(models.Options{
		Proxies: []map[string]any{
			{"name": "proxy1", "type": "ss", "server": "1.1.1.1", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
			{"name": "proxy2", "type": "ss", "server": "1.1.1.2", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
		},
		Concurrent:   2,
		Cache:        cache,
		Timeout:      time.Second,
		ProbeTimeout: time.Second,
	})
	assert.NoError(t, err)
	defer tester.Close()

	results, err := tester.TestSpeed(ctx)
	assert.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestTestSpeedTableDriven(t *testing.T) {
	tests := []struct {
		name            string
		proxies         []map[string]any
		containRegex    string
		nonContainRegex string
		mockResults     map[string]*models.CProxyWithResult
		wantAliveCount  int32
		wantTotalCount  int32
		wantResultCount int32
	}{
		{
			name: "Basic test - all alive",
			proxies: []map[string]any{
				{"name": "proxy1", "type": "ss", "server": "1.1.1.1", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
				{"name": "proxy2", "type": "ss", "server": "1.1.1.2", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
			},
			mockResults: map[string]*models.CProxyWithResult{
				"proxy1": {Result: models.Result{Name: "proxy1", Delay: 100}},
				"proxy2": {Result: models.Result{Name: "proxy2", Delay: 200}},
			},
			wantAliveCount:  2,
			wantTotalCount:  2,
			wantResultCount: 2,
		},
		{
			name: "Filter - contain regex",
			proxies: []map[string]any{
				{"name": "hk-01", "type": "ss", "server": "1.1.1.3", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
				{"name": "us-01", "type": "ss", "server": "1.1.1.4", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
			},
			containRegex: "hk",
			mockResults: map[string]*models.CProxyWithResult{
				"hk-01": {Result: models.Result{Name: "hk-01", Delay: 100}},
			},
			wantAliveCount:  1,
			wantTotalCount:  2,
			wantResultCount: 1,
		},
		{
			name: "Filter - non-contain regex",
			proxies: []map[string]any{
				{"name": "hk-01", "type": "ss", "server": "1.1.1.5", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
				{"name": "us-01", "type": "ss", "server": "1.1.1.6", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
			},
			nonContainRegex: "us",
			mockResults: map[string]*models.CProxyWithResult{
				"hk-01": {Result: models.Result{Name: "hk-01", Delay: 100}},
			},
			wantAliveCount:  1,
			wantTotalCount:  2,
			wantResultCount: 1,
		},
		{
			name: "Dead proxies",
			proxies: []map[string]any{
				{"name": "alive", "type": "ss", "server": "1.1.1.7", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
				{"name": "dead", "type": "ss", "server": "1.1.1.8", "port": 8388, "cipher": "aes-128-gcm", "password": "pass"},
			},
			mockResults: map[string]*models.CProxyWithResult{
				"alive": {Result: models.Result{Name: "alive", Delay: 100}},
				"dead":  {Result: models.Result{Name: "dead", Delay: 0}}, // Delay 0 means dead
			},
			wantAliveCount:  1,
			wantTotalCount:  2,
			wantResultCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			cache := &mockCache{results: tt.mockResults}
			opts := models.Options{
				Proxies:             tt.proxies,
				Concurrent:          2,
				Cache:               cache,
				NameRegexContain:    tt.containRegex,
				NameRegexNonContain: tt.nonContainRegex,
				Timeout:             time.Second,
			}

			tester, err := NewTest(opts)
			assert.NoError(t, err)
			defer tester.Close()

			results, err := tester.TestSpeed(ctx)
			assert.NoError(t, err)

			assert.Equal(t, tt.wantTotalCount, tester.TotalCount())
			assert.Equal(t, tt.wantTotalCount, tester.ProcessCount()) // 处理完后 count 应该等于 totalCount
			assert.Equal(t, tt.wantAliveCount, int32(len(tester.aliveProxies)))
			// results 只包含通过过滤且测速结果不为 nil 的节点
			assert.Equal(t, tt.wantResultCount, int32(len(results)))
		})
	}
}

func TestTestSpeedWithFile(t *testing.T) {
	if os.Getenv("SPEEDTEST_CLASH_RUN_NETWORK_TESTS") != "1" {
		t.Skip("skipping network integration test; set SPEEDTEST_CLASH_RUN_NETWORK_TESTS=1 to run")
	}

	tests := []struct {
		name       string
		configPath string
	}{
		{
			name:       "Load from test.yaml",
			configPath: "../test.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			opts := models.Options{
				ConfigPath:      tt.configPath,
				Concurrent:      20,
				Timeout:         30 * time.Second,
				ForceCertVerify: true,
			}

			tester, err := NewTest(opts)
			assert.NoError(t, err)
			defer tester.Close()

			results, err := tester.TestSpeed(context.Background())
			assert.NoError(t, err)

			t.Logf("results: %+v", results)
			tester.LogNum()
			tester.LogAlive()

		})
	}
}

func TestTestSpeedWithLargeInlineProxiesDoesNotBlock(t *testing.T) {
	proxyCount := cpuCount*10 + 25
	proxies := make([]map[string]any, 0, proxyCount)
	mockResults := make(map[string]*models.CProxyWithResult, proxyCount)

	for i := 0; i < proxyCount; i++ {
		name := "proxy-large-" + time.Unix(0, int64(i)).Format("150405.000000000")
		proxies = append(proxies, map[string]any{
			"name":     name,
			"type":     "ss",
			"server":   "1.1.1.1",
			"port":     8388,
			"cipher":   "aes-128-gcm",
			"password": "pass",
		})
		mockResults[name] = &models.CProxyWithResult{Result: models.Result{Name: name, Delay: 100}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tester, err := NewTest(models.Options{
		Proxies:    proxies,
		Concurrent: 4,
		Timeout:    time.Second,
		Cache:      &mockCache{results: mockResults},
	})
	assert.NoError(t, err)
	defer tester.Close()

	results, err := tester.TestSpeed(ctx)
	assert.NoError(t, err)
	assert.Len(t, results, proxyCount)
	assert.Equal(t, int32(proxyCount), tester.ProcessCount())
}

// TestNoHangWithManyConfigErrors verifies that loading goroutines always call
// wg.Done() even when errors occur, preventing a hang in the dispatch loop.
// Previously, errCh sends could block indefinitely when the buffer was full,
// preventing wg.Wait() → CloseProxies() → close(proxiesCh) from completing.
func TestNoHangWithManyConfigErrors(t *testing.T) {
	proxyCount := cpuCount*10 + 25
	proxies := make([]map[string]any, 0, proxyCount)
	mockResults := make(map[string]*models.CProxyWithResult, proxyCount)

	for i := 0; i < proxyCount; i++ {
		name := "proxy-errch-" + time.Unix(0, int64(i)).Format("150405.000000000")
		proxies = append(proxies, map[string]any{
			"name":     name,
			"type":     "ss",
			"server":   "1.1.1.1",
			"port":     8388,
			"cipher":   "aes-128-gcm",
			"password": "pass",
		})
		mockResults[name] = &models.CProxyWithResult{Result: models.Result{Name: name, Delay: 100}}
	}

	// Include a config path that will fail to load instantly (triggering error paths).
	// The test uses context.Background() so any goroutine blocked on errCh (before fix)
	// would hang forever; now errors are just logged and wg.Done() is called immediately.
	tester, err := NewTest(models.Options{
		Proxies:    proxies,
		ConfigPath: "/nonexistent/path/config1.yaml|/nonexistent/path/config2.yaml",
		Concurrent: 4,
		Timeout:    time.Second,
		Cache:      &mockCache{results: mockResults},
	})
	assert.NoError(t, err)
	defer tester.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		tester.TestSpeed(context.Background()) //nolint:errcheck
	}()

	select {
	case <-done:
		// success: completed without hanging
	case <-time.After(10 * time.Second):
		t.Fatal("TestSpeed hung — loading goroutines likely blocked on errCh send")
	}
}

// TestNoPanicOnCtxCancelDuringAddProxies ensures that cancelling the context while
// AddProxies is dispatching goroutines does not cause a "send on closed channel" panic.
// Previously, AddProxies returned early on ctx.Done() without waiting for already-launched
// goroutines, which could race with CloseProxies() closing proxiesCh.
func TestNoPanicOnCtxCancelDuringAddProxies(t *testing.T) {
	proxyCount := 200
	proxies := make([]map[string]any, 0, proxyCount)
	mockResults := make(map[string]*models.CProxyWithResult, proxyCount)

	for i := 0; i < proxyCount; i++ {
		name := fmt.Sprintf("proxy-cancel-%d", i)
		proxies = append(proxies, map[string]any{
			"name":     name,
			"type":     "ss",
			"server":   "1.1.1.1",
			"port":     8388,
			"cipher":   "aes-128-gcm",
			"password": "pass",
		})
		mockResults[name] = &models.CProxyWithResult{Result: models.Result{Name: name, Delay: 100}}
	}

	// Cancel the context almost immediately to trigger early-return path in AddProxies.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	tester, err := NewTest(models.Options{
		Proxies:    proxies,
		Concurrent: 2,
		Timeout:    time.Second,
		Cache:      &mockCache{results: mockResults},
	})
	assert.NoError(t, err)
	defer tester.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		tester.TestSpeed(ctx) //nolint:errcheck
	}()

	select {
	case <-done:
		// success: no panic, no hang
	case <-time.After(5 * time.Second):
		t.Fatal("TestSpeed hung after ctx cancellation")
	}
}
