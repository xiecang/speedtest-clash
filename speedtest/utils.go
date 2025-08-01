package speedtest

import (
	"context"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
	"sync"

	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

var (
	delayTestUrl = "https://cp.cloudflare.com/generate_204"
)

func TestURLAvailable(urls []string, proxy C.Proxy, timeout time.Duration) map[string]bool {
	if len(urls) == 0 {
		return nil
	}
	var res = sync.Map{}
	wg := sync.WaitGroup{}
	wg.Add(len(urls))

	for _, url := range urls {

		go func(url string) {
			defer wg.Done()
			client := requests.GetClient(proxy, timeout)
			resp, err := client.Get(url)
			if err != nil {
				res.Store(url, false)
				return
			}
			if resp.StatusCode-http.StatusOK > 200 {
				res.Store(url, false)
				return
			}
			res.Store(url, true)
		}(url)
	}
	wg.Wait()
	var result = make(map[string]bool)
	res.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(bool)
		return true
	})
	return result
}

func testspeed(ctx context.Context, proxy models.CProxy, options *models.Options) (*models.CProxyWithResult, error) {
	var name = proxy.Name()

	// get from cache
	if cache := getCacheFromResult(name); cache != nil {
		return cache, nil
	}

	var (
		tp  = proxy.Type()
		err error
	)
	switch tp {
	case C.Shadowsocks, C.ShadowsocksR, C.Snell, C.Socks5, C.Http, C.Vmess, C.Vless, C.Trojan, C.Hysteria, C.Hysteria2, C.WireGuard, C.Tuic:
		result := TestProxy(ctx, name, proxy, options)
		var r = &models.CProxyWithResult{
			Result: *result,
			Proxy:  proxy,
		}
		// add cache
		addResultCache(r)
		return r, nil
	case C.Direct, C.Reject, C.Relay, C.Selector, C.Fallback, C.URLTest, C.LoadBalance:
		return nil, nil
	default:
		err = fmt.Errorf("unsupported proxy type: %s", tp)
	}
	return nil, err
}

//func TestSpeed(proxies map[string]models.CProxy, options *models.Options) ([]models.Result, error) {
//	results := make([]models.Result, 0, len(proxies))
//	var err error
//
//	var wg = sync.WaitGroup{}
//	for name, proxy := range proxies {
//		wg.Add(1)
//		go func(name string, proxy models.CProxy) {
//			defer wg.Done()
//
//			// get from cache
//			if cache := getCacheFromResult(name); cache != nil {
//				results = append(results, *cache)
//				return
//			}
//
//			var tp = proxy.Type()
//			switch tp {
//			case C.Shadowsocks, C.ShadowsocksR, C.Snell, C.Socks5, C.Http, C.Vmess, C.Vless, C.Trojan, C.Hysteria, C.Hysteria2, C.WireGuard, C.Tuic:
//				result := TestProxy(name, proxy, options)
//				if result.Alive() {
//					countryR := checkProxy(context.Background(), proxy, []models.CheckType{models.CheckTypeCountry})
//					if len(countryR) > 0 {
//						result.Country = countryR[0].Value
//					}
//					result.CheckResults = checkProxy(context.Background(), proxy, options.CheckTypes)
//
//					if options.URLForTest != nil {
//						result.URLForTest = TestURLAvailable(options.URLForTest, proxy, options.Timeout)
//					}
//				}
//				results = append(results, *result)
//
//				// add cache
//				addResultCache(result)
//			case C.Direct, C.Reject, C.Relay, C.Selector, C.Fallback, C.URLTest, C.LoadBalance:
//				return
//			default:
//				err = fmt.Errorf("unsupported proxy type: %s", tp)
//			}
//		}(name, proxy)
//	}
//	wg.Wait()
//	return results, err
//}

type proxyTest struct {
	name   string
	option *models.Options
	proxy  C.Proxy
	//
	client *http.Client
}

func newProxyTest(name string, proxy C.Proxy, option *models.Options) *proxyTest {
	client := requests.GetClient(proxy, option.Timeout)
	return &proxyTest{
		name:   name,
		option: option,
		proxy:  proxy,
		client: client,
	}
}

func (s *proxyTest) testBandwidth(ctx context.Context, downloadSize int) (time.Duration, float64, error) {
	var (
		client         = s.client
		livenessObject = s.option.LivenessAddr
	)
	url := fmt.Sprintf(livenessObject, downloadSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("create request failed, err: %v", err)
	}
	start := time.Now()
	req.Header.Set("User-Agent", convert.RandUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("connect test domain failed, err: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode-http.StatusOK > 100 {
		return 0, 0, fmt.Errorf("test domain status not ok, status_code=%d", resp.StatusCode)
	}
	ttfb := time.Since(start)

	written, _ := io.Copy(io.Discard, resp.Body)
	if written == 0 {
		return 0, 0, fmt.Errorf("test domain return empty body")
	}
	downloadTime := time.Since(start) - ttfb
	bandwidth := (float64(written) * 8) / downloadTime.Seconds() / 1e3 // 转换为 Kbps

	return ttfb, bandwidth, nil
}

func (s *proxyTest) testDelay() uint16 {
	const tryCount = 3
	results := make(chan uint16, tryCount)
	var wg sync.WaitGroup

	for i := 0; i < tryCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			//	200,204
			expectedStatus, _ := utils.NewUnsignedRanges[uint16]("200,204")
			d, _ := s.proxy.URLTest(context.Background(), delayTestUrl, expectedStatus)
			results <- d
		}()
	}

	wg.Wait()
	close(results)

	m := uint16(0xFFFF)
	for d := range results {
		if d > 0 && d < m {
			m = d
		}
	}
	if m == 0xFFFF {
		return 0
	}
	return m
}

func (s *proxyTest) Test(ctx context.Context) *models.Result {
	var (
		name       = s.name
		option     = s.option
		proxy      = s.proxy
		chunkSize  = option.DownloadSize
		checkTypes = option.CheckTypes
		URLForTest = option.URLForTest
		timeout    = option.Timeout
	)

	ttfb, bandwidth, err := s.testBandwidth(ctx, chunkSize)
	if err != nil {
		return &models.Result{
			Name: name,
		}
	}

	var (
		country      string
		checkResults []models.CheckResult
		urlResults   map[string]bool
		delay        uint16
	)
	var wg sync.WaitGroup
	countryCh := make(chan string, 1)
	checkCh := make(chan []models.CheckResult, 1)
	urlCh := make(chan map[string]bool, 1)
	delayCh := make(chan uint16, 1)

	if bandwidth > 0 || ttfb > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			countryR := checkProxy(ctx, proxy, []models.CheckType{models.CheckTypeCountry})
			if len(countryR) > 0 {
				countryCh <- countryR[0].Value
			} else {
				countryCh <- ""
			}
		}()
		//
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkCh <- checkProxy(ctx, proxy, checkTypes)
		}()
		//
		if URLForTest != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				urlCh <- TestURLAvailable(URLForTest, proxy, timeout)
			}()
		}
		//
		wg.Add(1)
		go func() {
			defer wg.Done()
			delayCh <- s.testDelay()
		}()
	}
	go func() {
		wg.Wait()
		close(countryCh)
		close(checkCh)
		close(urlCh)
		close(delayCh)
	}()
	for c := range countryCh {
		country = c
	}
	for cr := range checkCh {
		checkResults = cr
	}
	for ur := range urlCh {
		urlResults = ur
	}
	for d := range delayCh {
		delay = d
	}

	result := &models.Result{
		Name:         name,
		Bandwidth:    bandwidth,
		TTFB:         ttfb,
		Delay:        delay,
		Country:      country,
		CheckResults: checkResults,
		URLForTest:   urlResults,
	}

	return result
}

func TestProxy(ctx context.Context, name string, proxy C.Proxy, option *models.Options) *models.Result {
	p := newProxyTest(name, proxy, option)
	return p.Test(ctx)
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
			fmt.Sprintf("%.2f", result.Bandwidth/1024),
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
