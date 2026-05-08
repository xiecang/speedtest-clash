package speedtest

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
)

func testspeed(ctx context.Context, proxy models.CProxy, options *models.Options, limiter *models.BandwidthLimiter) (*models.CProxyWithResult, error) {
	var key string
	if options.Cache != nil {
		// 生成缓存键
		key = options.Cache.GenerateKey(&proxy)

		// 尝试从缓存获取
		if cached, exists := options.Cache.Get(ctx, key); exists {
			return cached, nil
		}
	}

	var (
		tp  = proxy.Type()
		err error
	)
	switch tp {
	case C.Shadowsocks, C.ShadowsocksR, C.Snell, C.Socks5, C.Http, C.Vmess, C.Vless, C.Trojan, C.Hysteria, C.Hysteria2, C.WireGuard, C.Tuic:
		name := key
		if n, ok := proxy.SecretConfig["name"].(string); ok && n != "" {
			name = n
		}
		result := TestProxy(ctx, name, proxy, options, limiter)
		if result == nil {
			return nil, fmt.Errorf("test proxy returned nil result")
		}
		var r = &models.CProxyWithResult{
			Result: *result,
			Proxy:  proxy,
		}
		// 存储到缓存
		if options.Cache != nil {
			if err := options.Cache.Set(ctx, key, r); err != nil {
				// 缓存失败不影响返回结果
				warnf(options, "failed to cache result: %v", err)
			}
		}
		return r, nil
	case C.Direct, C.Reject, C.Relay, C.Selector, C.Fallback, C.URLTest, C.LoadBalance:
		return nil, nil
	default:
		err = fmt.Errorf("unsupported proxy type: %s", tp)
	}
	return nil, err
}

type proxyTest struct {
	name             string
	option           *models.Options
	proxy            C.Proxy
	bandwidthLimiter *models.BandwidthLimiter
	//
	client *http.Client
}

func newProxyTest(name string, proxy C.Proxy, option *models.Options, limiter *models.BandwidthLimiter) *proxyTest {
	// Use 0 timeout for the client and rely on the context for timeouts.
	// This is more flexible for bandwidth and latency testing.
	client := requests.GetClient(proxy, 0)
	return &proxyTest{
		name:             name,
		option:           option,
		proxy:            proxy,
		client:           client,
		bandwidthLimiter: limiter,
	}
}

func (s *proxyTest) testBandwidth(ctx context.Context) (time.Duration, float64, int64, error) {
	var (
		client       = s.client
		url          = s.option.LivenessAddr
		downloadSize = s.option.DownloadSize
		concurrency  = s.option.BandwidthConcurrency
	)

	if concurrency <= 0 {
		concurrency = 4
	}

	if downloadSize <= 0 {
		downloadSize = 10 * 1024 * 1024 // 默认 10M
	}
	if strings.Contains(url, "%d") {
		url = fmt.Sprintf(url, downloadSize)
	}

	// Now run multi-threaded download to measure bandwidth and sample TTFB
	var (
		wg          sync.WaitGroup
		totalBytes  int64
		ttfbSamples []time.Duration
		mu          sync.Mutex
		chunkSize   = int64(downloadSize / concurrency)
		downloadErr error
	)

	bandwidthStart := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", convert.RandUserAgent())

			start := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				mu.Lock()
				downloadErr = err
				mu.Unlock()
				return
			}
			defer resp.Body.Close()

			ttfb := time.Since(start)
			mu.Lock()
			ttfbSamples = append(ttfbSamples, ttfb)
			mu.Unlock()

			if resp.StatusCode >= 400 {
				mu.Lock()
				downloadErr = fmt.Errorf("status code: %d", resp.StatusCode)
				mu.Unlock()
				return
			}

			n, err := s.copyLimitedN(ctx, io.Discard, resp.Body, chunkSize)
			mu.Lock()
			// Always tally downloaded bytes, even on a partial read that ended with
			// an error (e.g. context deadline mid-download).  This matches the old
			// io.Copy behaviour and ensures slow proxies produce a bandwidth reading
			// instead of zero.
			if n > 0 {
				totalBytes += n
			}
			if err != nil {
				downloadErr = err
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	downloadTime := time.Since(bandwidthStart)

	var avgTTFB time.Duration
	if len(ttfbSamples) > 0 {
		var totalTTFB time.Duration
		for _, t := range ttfbSamples {
			totalTTFB += t
		}
		avgTTFB = totalTTFB / time.Duration(len(ttfbSamples))
	}

	if downloadErr != nil && totalBytes == 0 {
		return avgTTFB, 0, totalBytes, fmt.Errorf("download failed: %v", downloadErr)
	}

	if totalBytes == 0 {
		if downloadErr != nil {
			return avgTTFB, 0, totalBytes, fmt.Errorf("no data downloaded: %v", downloadErr)
		}
		return avgTTFB, 0, totalBytes, fmt.Errorf("no data downloaded")
	}

	bandwidth := float64(totalBytes) / downloadTime.Seconds() // B/s

	return avgTTFB, bandwidth, totalBytes, nil
}

func (s *proxyTest) copyLimited(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	return s.copyLimitedN(ctx, dst, src, -1)
}

func (s *proxyTest) copyLimitedN(ctx context.Context, dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	const bufSize = 32 * 1024 // must match models.bandwidthBurstBytes
	buf := make([]byte, bufSize)
	var written int64
	remaining := maxBytes
	for {
		if remaining == 0 {
			return written, nil
		}
		readSize := bufSize
		if remaining > 0 && remaining < int64(readSize) {
			readSize = int(remaining)
		}
		if err := s.bandwidthLimiter.Wait(ctx, int64(readSize)); err != nil {
			return written, err
		}

		nr, er := src.Read(buf[:readSize])
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
				if remaining > 0 {
					remaining -= int64(nw)
				}
			}
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return written, nil
			}
			return written, er
		}
	}
}

func (s *proxyTest) testBandwidthIfEnabled(ctx context.Context) (time.Duration, float64, int64, error) {
	if s.option.DisableBandwidthTest {
		return 0, 0, 0, nil
	}
	return s.testBandwidth(ctx)
}

type urlResult struct {
	url      string
	delay    uint16
	duration time.Duration
	err      error
	ok       bool
}

type latencyStats struct {
	min      uint16
	p50      uint16
	p90      uint16
	p95      uint16
	jitter   uint16
	lossRate float64
}

func (s *proxyTest) requestDelay(ctx context.Context, url string) (uint16, error) {
	expectedStatus, _ := utils.NewUnsignedRanges[uint16]("200,204")
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", convert.RandUserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()

	if !expectedStatus.Check(uint16(resp.StatusCode)) {
		return 0, fmt.Errorf("status code: %d", resp.StatusCode)
	}

	return uint16(time.Since(start).Milliseconds()), nil
}

func (s *proxyTest) probeURLMinDelay(ctx context.Context, url string, tryCount int) (uint16, error) {
	results := make(chan urlResult, tryCount)
	var wg sync.WaitGroup

loop:
	for i := 0; i < tryCount; i++ {
		if i > 0 {
			select {
			case <-time.After(time.Duration(5+rand.Intn(6)) * time.Millisecond):
			case <-ctx.Done():
				break loop
			}
		}

		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			debugf(s.option, "[%s] [%d] 开始探测 URL: %s", s.name, i, url)
			delay, err := s.requestDelay(ctx, url)
			if err != nil {
				debugf(s.option, "[%s] [%d] 探测失败: %v", s.name, i, err)
				results <- urlResult{url: url, err: err, ok: false}
				return
			}

			debugf(s.option, "[%s] [%d] 探测完成: %dms", s.name, i, delay)
			results <- urlResult{url: url, delay: delay, ok: true}
		}(i)
	}

	wg.Wait()
	close(results)

	var (
		successCount int
		minDelay     = uint16(0xFFFF)
		lastErr      error
	)

	for r := range results {
		if r.ok {
			successCount++
			if r.delay < minDelay {
				minDelay = r.delay
			}
			continue
		}
		lastErr = r.err
	}

	if successCount > 0 {
		return minDelay, nil
	}

	debugf(s.option, "[%s] 并行探测全部失败, 尝试串行补偿探测: %s", s.name, url)
	delay, err := s.requestDelay(ctx, url)
	if err == nil {
		debugf(s.option, "[%s] 补偿探测成功: %dms", s.name, delay)
		return delay, nil
	}
	debugf(s.option, "[%s] 补偿探测失败: %v", s.name, err)
	if lastErr == nil {
		lastErr = err
	}

	return 0, lastErr
}

func (s *proxyTest) measureLatencyMetrics(ctx context.Context, url string) latencyStats {
	latencySamples := s.option.LatencySamples
	if latencySamples <= 0 {
		latencySamples = 3
	}

	delays := make([]uint16, 0, latencySamples)
	failedSamples := 0

	for i := 0; i < latencySamples; i++ {
		if ctx.Err() != nil {
			failedSamples += latencySamples - i
			break
		}

		delay, err := s.requestDelay(ctx, url)
		if err != nil {
			failedSamples++
			debugf(s.option, "[%s] [metrics-%d] 探测失败: %v", s.name, i, err)
			continue
		}

		debugf(s.option, "[%s] [metrics-%d] 探测完成: %dms", s.name, i, delay)
		delays = append(delays, delay)
	}

	stats := latencyStats{
		lossRate: float64(failedSamples) / float64(latencySamples),
	}
	if len(delays) == 0 {
		return stats
	}

	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	stats.min = delays[0]

	if len(delays) < 2 {
		return stats
	}

	var sumDelay uint32
	for _, d := range delays {
		sumDelay += uint32(d)
	}
	avgDelay := float64(sumDelay) / float64(len(delays))

	var sumDev float64
	for _, d := range delays {
		dev := float64(d) - avgDelay
		if dev < 0 {
			dev = -dev
		}
		sumDev += dev
	}

	stats.p50 = percentileDelay(delays, 0.50)
	stats.p90 = percentileDelay(delays, 0.90)
	stats.p95 = percentileDelay(delays, 0.95)
	stats.jitter = uint16(sumDev / float64(len(delays)))

	return stats
}

func (s *proxyTest) testURL(ctx context.Context, url string, tryCount int) (latencyStats, error) {
	warmupDelay, err := s.probeURLMinDelay(ctx, url, tryCount)
	if err != nil {
		return latencyStats{}, err
	}

	if !s.option.EnableLatencyMetrics {
		return latencyStats{min: warmupDelay}, nil
	}

	metrics := s.measureLatencyMetrics(ctx, url)
	if metrics.min == 0 || warmupDelay < metrics.min {
		metrics.min = warmupDelay
	}

	debugf(s.option, "[%s] 延迟指标汇总: Delay=%d, P50=%d, P90=%d, P95=%d, Jitter=%d, LossRate=%.2f",
		s.name, metrics.min, metrics.p50, metrics.p90, metrics.p95, metrics.jitter, metrics.lossRate)

	return metrics, nil
}

func percentileDelay(sorted []uint16, percentile float64) uint16 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(percentile*float64(len(sorted)-1) + 0.5)
	if idx < 0 {
		idx = 0
	} else if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func (s *proxyTest) testDelay(ctx context.Context) latencyStats {
	stats, _ := s.testURL(ctx, s.option.DelayTestUrl, 3)
	return stats
}

func (s *proxyTest) testURLAvailable(ctx context.Context, urls []string) map[string]bool {
	if len(urls) == 0 {
		return nil
	}
	var wg sync.WaitGroup

	results := make(chan *urlResult, len(urls))
	wg.Add(len(urls))
	for _, url := range urls {
		go func(url string) {
			defer wg.Done()
			delay, okErr := s.probeURLMinDelay(ctx, url, 3)
			results <- &urlResult{
				url:   url,
				delay: delay,
				ok:    okErr == nil && delay > 0,
			}
		}(url)
	}

	wg.Wait()
	close(results)

	urlResults := make(map[string]bool)
	for r := range results {
		urlResults[r.url] = r.ok
	}
	return urlResults
}

func (s *proxyTest) Test(ctx context.Context) *models.Result {
	testStart := time.Now()
	var (
		name       = s.name
		option     = s.option
		proxy      = s.proxy
		checkTypes = option.CheckTypes
		URLForTest = option.URLForTest
	)

	probeCtx := ctx
	var cancel context.CancelFunc
	if option.ProbeTimeout > 0 {
		probeCtx, cancel = context.WithTimeout(ctx, option.ProbeTimeout)
		defer cancel()
	}
	delayStats := s.testDelay(probeCtx)
	if delayStats.min == 0 {
		debugf(s.option, "[%s] 节点不可用 (Delay: %v), 跳过后续测试", name, delayStats.min)
		return &models.Result{
			Name:         name,
			Delay:        delayStats.min,
			DelayP50:     delayStats.p50,
			DelayP90:     delayStats.p90,
			DelayP95:     delayStats.p95,
			Jitter:       delayStats.jitter,
			LossRate:     delayStats.lossRate,
			TestDuration: time.Since(testStart),
		}
	}

	var (
		country       string
		checkResults  []models.CheckResult
		urlResults    map[string]bool
		ttfb          time.Duration
		bandwidth     float64
		downloadBytes int64
		bandwidthErr  error
		mu            sync.Mutex
		wg            sync.WaitGroup
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		t, b, bytes, err := s.testBandwidthIfEnabled(ctx)
		mu.Lock()
		ttfb, bandwidth, downloadBytes, bandwidthErr = t, b, bytes, err
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		countryR := checkProxy(ctx, proxy, []models.CheckType{models.CheckTypeCountry}, loggerFromOptions(option))
		mu.Lock()
		if len(countryR) > 0 {
			country = countryR[0].Value
		}
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		results := checkProxy(ctx, proxy, checkTypes, loggerFromOptions(option))
		mu.Lock()
		checkResults = results
		mu.Unlock()
	}()

	if URLForTest != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results := s.testURLAvailable(ctx, URLForTest)
			mu.Lock()
			urlResults = results
			mu.Unlock()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		debugf(s.option, "[%s] 测试流程超时或取消", name)
		<-done
	}

	if bandwidthErr != nil {
		debugf(s.option, "[%s] 带宽测试失败: %v", name, bandwidthErr)
	}

	if country == "" {
		for _, c := range checkResults {
			if c.Type == models.CheckTypeGPTWeb {
				country = c.Value
				break
			}
		}
	}

	return &models.Result{
		Name:          name,
		Bandwidth:     bandwidth,
		TTFB:          ttfb,
		Delay:         delayStats.min,
		DelayP50:      delayStats.p50,
		DelayP90:      delayStats.p90,
		DelayP95:      delayStats.p95,
		Jitter:        delayStats.jitter,
		LossRate:      delayStats.lossRate,
		Country:       country,
		CheckResults:  checkResults,
		URLForTest:    urlResults,
		TestDuration:  time.Since(testStart),
		DownloadBytes: downloadBytes,
	}
}

func TestProxy(ctx context.Context, name string, proxy C.Proxy, option *models.Options, limiter *models.BandwidthLimiter) *models.Result {
	timeout := option.Timeout
	proxyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p := newProxyTest(name, proxy, option, limiter)
	return p.Test(proxyCtx)
}

func writeNodeConfigurationToYAML(filePath string, results []models.CProxyWithResult) error {
	fp, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer func(fp *os.File) {
		_ = fp.Close()
	}(fp)

	var proxies []any
	for _, result := range results {
		proxies = append(proxies, result.Proxy.SecretConfig)
	}

	bytes, err := yaml.Marshal(proxies)
	if err != nil {
		return err
	}

	_, err = fp.Write(bytes)
	return err
}

func writeToCSV(filePath string, results []models.CProxyWithResult) error {
	csvFile, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer csvFile.Close()

	// 写入 UTF-8 BOM 头
	csvFile.WriteString("\xEF\xBB\xBF")

	csvWriter := csv.NewWriter(csvFile)
	err = csvWriter.Write([]string{"节点", "带宽 (MB/s)", "延迟 (ms)"})
	if err != nil {
		return err
	}
	for _, result := range results {
		line := []string{
			result.Name,
			fmt.Sprintf("%.2f", result.Bandwidth/(1024*1024)),
			strconv.FormatInt(result.TTFB.Milliseconds(), 10),
		}
		err = csvWriter.Write(line)
		if err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return nil
}
