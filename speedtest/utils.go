package speedtest

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
)

func testspeed(ctx context.Context, proxy models.CProxy, options *models.Options) (*models.CProxyWithResult, error) {
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
		result := TestProxy(ctx, name, proxy, options)
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
				log.Warnln("failed to cache result: %v", err)
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
	name   string
	option *models.Options
	proxy  C.Proxy
	//
	client *http.Client
}

func newProxyTest(name string, proxy C.Proxy, option *models.Options) *proxyTest {
	// Use 0 timeout for the client and rely on the context for timeouts.
	// This is more flexible for bandwidth and latency testing.
	client := requests.GetClient(proxy, 0)
	return &proxyTest{
		name:   name,
		option: option,
		proxy:  proxy,
		client: client,
	}
}

func (s *proxyTest) testBandwidth(ctx context.Context) (time.Duration, float64, error) {
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

			n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, chunkSize))
			mu.Lock()
			totalBytes += n
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
		return avgTTFB, 0, fmt.Errorf("download failed: %v", downloadErr)
	}

	if totalBytes == 0 {
		if downloadErr != nil {
			return avgTTFB, 0, fmt.Errorf("no data downloaded: %v", downloadErr)
		}
		return avgTTFB, 0, fmt.Errorf("no data downloaded")
	}

	bandwidth := float64(totalBytes) / downloadTime.Seconds() // B/s

	return avgTTFB, bandwidth, nil
}

type urlResult struct {
	url      string
	delay    uint16
	duration time.Duration
	err      error
	ok       bool
}

func (s *proxyTest) testURL(ctx context.Context, url string, tryCount int) (uint16, uint16, float64, error) {
	results := make(chan urlResult, tryCount)
	var wg sync.WaitGroup

	latencySamples := s.option.LatencySamples
	if latencySamples <= 0 {
		latencySamples = 3
	}

loop:
	for i := 0; i < tryCount; i++ {
		// Stagger probe launches slightly to reduce instantaneous burst noise in high concurrency
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
			log.Debugln("[%s] [%d] 开始探测 URL: %s", s.name, i, url)
			var (
				successSamples int
				sumDelay       int64
				lastErr        error
				probeStart     = time.Now()
				firstDelay     uint16
				hasFirst       bool
			)

			expectedStatus, _ := utils.NewUnsignedRanges[uint16]("200,204")

			for j := 0; j < latencySamples; j++ {
				if ctx.Err() != nil {
					lastErr = ctx.Err()
					break
				}
				start := time.Now()
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				req.Header.Set("User-Agent", convert.RandUserAgent())
				resp, err := s.client.Do(req)
				if err != nil {
					lastErr = err
					log.Debugln("[%s] [%d-%d] 探测失败: %v", s.name, i, j, err)
					break
				}
				resp.Body.Close()

				if !expectedStatus.Check(uint16(resp.StatusCode)) {
					lastErr = fmt.Errorf("status code: %d", resp.StatusCode)
					log.Debugln("[%s] [%d-%d] 状态码错误: %d", s.name, i, j, resp.StatusCode)
					break
				}

				delay := uint16(time.Since(start).Milliseconds())
				log.Debugln("[%s] [%d-%d] 探测完成: %dms", s.name, i, j, delay)

				if j == 0 {
					firstDelay = delay
					hasFirst = true
				}

				// If LatencySamples > 1, the first request establishes the connection (TCP/TLS handshake).
				// We skip it from the average if we can successfully get more samples on the established link.
				if latencySamples > 1 && j == 0 {
					continue
				}

				successSamples++
				sumDelay += int64(delay)
			}

			// Robustness fallback: if first request worked but avg samples failed (e.g. timeout during 2nd req),
			// use the first request as result instead of marking dead.
			if successSamples == 0 && hasFirst {
				successSamples = 1
				sumDelay = int64(firstDelay)
			}

			if successSamples > 0 {
				avg := uint16(sumDelay / int64(successSamples))
				results <- urlResult{
					url:      url,
					delay:    avg,
					duration: time.Since(probeStart),
					ok:       true,
				}
			} else {
				results <- urlResult{
					url: url,
					err: lastErr,
					ok:  false,
				}
			}
		}(i)
	}

	wg.Wait()
	close(results)

	var (
		successCount int
		minDelay     = uint16(0xFFFF)
		delays       []uint16
		lastErr      error
	)

	for r := range results {
		if r.ok {
			successCount++
			delays = append(delays, r.delay)
			if r.delay < minDelay {
				minDelay = r.delay
			}
		} else {
			lastErr = r.err
		}
	}

	if successCount == 0 {
		// Last-ditch effort: If all parallel probes failed (perhaps due to concurrency noise),
		// try one sequential probe to confirm "Dead" status.
		log.Debugln("[%s] 并行探测全部失败, 尝试串行补偿探测: %s", s.name, url)
		expectedStatus, _ := utils.NewUnsignedRanges[uint16]("200,204")
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("User-Agent", convert.RandUserAgent())
		resp, err := s.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if expectedStatus.Check(uint16(resp.StatusCode)) {
				delay := uint16(time.Since(start).Milliseconds())
				log.Debugln("[%s] 补偿探测成功: %dms", s.name, delay)
				return delay, 0, 0.0, nil
			}
			log.Debugln("[%s] 补偿探测状态码错误: %d", s.name, resp.StatusCode)
		} else {
			log.Debugln("[%s] 补偿探测失败: %v", s.name, err)
		}
		return 0, 0, 1.0, lastErr
	}

	// Calculate Jitter (Average deviation from mean)
	var sumDelay uint32
	for _, d := range delays {
		sumDelay += uint32(d)
	}
	avgDelay := float64(sumDelay) / float64(successCount)

	var sumDev float64
	for _, d := range delays {
		dev := float64(d) - avgDelay
		if dev < 0 {
			dev = -dev
		}
		sumDev += dev
	}
	jitter := uint16(sumDev / float64(successCount))
	lossRate := 1.0 - (float64(successCount) / float64(tryCount))

	log.Debugln("[%s] 探测结果汇总: Delay=%d, Jitter=%d, LossRate=%.2f", s.name, minDelay, jitter, lossRate)

	return minDelay, jitter, lossRate, nil
}

func (s *proxyTest) testDelay(ctx context.Context) (uint16, uint16, float64) {
	d, jitter, loss, _ := s.testURL(ctx, s.option.DelayTestUrl, 3)
	return d, jitter, loss
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
			d, _, _, okErr := s.testURL(ctx, url, 3)
			results <- &urlResult{
				url:   url,
				delay: d,
				ok:    okErr == nil && d > 0,
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
	var (
		name       = s.name
		option     = s.option
		proxy      = s.proxy
		checkTypes = option.CheckTypes
		URLForTest = option.URLForTest
	)

	// 优先测试延迟和丢包率，实现快速失败
	delay, jitter, lossRate := s.testDelay(ctx)
	if delay == 0 || lossRate >= 1.0 {
		log.Debugln("[%s] 节点不可用 (Delay: %v, Loss: %.2f), 跳过后续测试", name, delay, lossRate)
		return &models.Result{
			Name:     name,
			Delay:    delay,
			Jitter:   jitter,
			LossRate: lossRate,
		}
	}

	var (
		country      string
		checkResults []models.CheckResult
		urlResults   map[string]bool
		ttfb         time.Duration
		bandwidth    float64
		bandwidthErr error
		mu           sync.Mutex
		wg           sync.WaitGroup
	)

	// 1. 带宽测试
	wg.Add(1)
	go func() {
		defer wg.Done()
		t, b, err := s.testBandwidth(ctx)
		mu.Lock()
		ttfb, bandwidth, bandwidthErr = t, b, err
		mu.Unlock()
	}()

	// 2. 国家/地区检查
	wg.Add(1)
	go func() {
		defer wg.Done()
		countryR := checkProxy(ctx, proxy, []models.CheckType{models.CheckTypeCountry})
		mu.Lock()
		if len(countryR) > 0 {
			country = countryR[0].Value
		}
		mu.Unlock()
	}()

	// 3. 其它类型检查 (Netflix, Disney+ etc.)
	wg.Add(1)
	go func() {
		defer wg.Done()
		results := checkProxy(ctx, proxy, checkTypes)
		mu.Lock()
		checkResults = results
		mu.Unlock()
	}()

	// 4. URL 可用性检查
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

	// 等待所有异步测试完成 (遵守 ctx 超时)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 正常完成
	case <-ctx.Done():
		// 超时或取消，尽量保留已测得的部分数据
		log.Debugln("[%s] 测试流程超时或取消", name)
		<-done // 等待子协程清理并更新变量
	}

	if bandwidthErr != nil {
		log.Debugln("[%s] 带宽测试失败: %v", name, bandwidthErr)
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
		Name:         name,
		Bandwidth:    bandwidth,
		TTFB:         ttfb,
		Delay:        delay,
		Jitter:       jitter,
		LossRate:     lossRate,
		Country:      country,
		CheckResults: checkResults,
		URLForTest:   urlResults,
	}
}

func TestProxy(ctx context.Context, name string, proxy C.Proxy, option *models.Options) *models.Result {
	timeout := option.Timeout
	proxyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	p := newProxyTest(name, proxy, option)
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
