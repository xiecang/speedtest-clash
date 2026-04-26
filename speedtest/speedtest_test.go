package speedtest

import (
	"context"
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

	ttfb, bandwidth, err := p.testBandwidthIfEnabled(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), ttfb)
	assert.Equal(t, float64(0), bandwidth)
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
	//log.SetLevel(log.DEBUG)
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
