package speedtest

import (
	"context"
	"errors"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
	"net"
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

func testspeed(ctx context.Context, proxy models.CProxy, options *models.Options) (*models.CProxyWithResult, error) {
	var name = cacheKey(&proxy)

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
	req.Header.Set("User-Agent", convert.RandUserAgent())
	start := time.Now()
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

type urlResult struct {
	url   string
	delay uint16
	err   error
	ok    bool
}

func (s *proxyTest) testURL(ctx context.Context, url string, tryCount int) *urlResult {
	results := make(chan urlResult, tryCount)
	var wg sync.WaitGroup

	for i := 0; i < tryCount; i++ {
		wg.Add(1)
		go func(ctx context.Context, url string) {
			defer wg.Done()
			//	200,204
			expectedStatus, err := utils.NewUnsignedRanges[uint16]("200,204")
			if err != nil {
				results <- urlResult{url: url, delay: 0, err: err, ok: false}
				return
			}
			d, err := s.proxy.URLTest(ctx, url, expectedStatus)
			results <- urlResult{
				url:   url,
				delay: d,
				err:   err,
				ok:    err == nil && d > 0,
			}
		}(ctx, url)
	}

	wg.Wait()
	close(results)

	m := uint16(0xFFFF)
	var (
		err1 = fmt.Errorf("failed")
		err  = err1
		ok   bool
	)
	for r := range results {
		if r.err == nil {
			err = nil
		} else if errors.Is(err, err1) {
			err = r.err
		}
		if r.ok {
			ok = true
		}
		d := r.delay
		if d > 0 && d < m {
			m = d
		}
	}
	if m == 0xFFFF || err != nil {
		return &urlResult{
			url:   url,
			delay: 0,
			err:   err,
			ok:    ok,
		}
	} else {
		err = nil
	}
	return &urlResult{
		url:   url,
		delay: m,
		err:   err,
		ok:    ok,
	}
}

func (s *proxyTest) testDelay(ctx context.Context) uint16 {
	r := s.testURL(ctx, delayTestUrl, 3)
	return r.delay
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
			results <- s.testURL(ctx, url, 3)
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

func (s *proxyTest) isReachable() bool {
	var (
		address = s.proxy.Addr()
		timeout = 2 * time.Second
	)
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	defer func(conn net.Conn) {
		_ = conn.Close()
	}(conn)
	return true
}

func (s *proxyTest) Test(ctx context.Context) *models.Result {
	var (
		name       = s.name
		option     = s.option
		proxy      = s.proxy
		chunkSize  = option.DownloadSize
		checkTypes = option.CheckTypes
		URLForTest = option.URLForTest
	)

	reachable := s.isReachable()
	if !reachable {
		return &models.Result{
			Name: name,
		}
	}
	ttfb, bandwidth, err := s.testBandwidth(ctx, chunkSize)
	if err != nil {
		return &models.Result{Name: name}
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

	if ttfb > 0 && bandwidth > 0 {
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
				urlCh <- s.testURLAvailable(ctx, URLForTest)
			}()
		}
		//
		wg.Add(1)
		go func() {
			defer wg.Done()
			delayCh <- s.testDelay(ctx)
		}()

		go func() {
			wg.Wait()
			close(countryCh)
			close(checkCh)
			close(urlCh)
			close(delayCh)
		}()
	}

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

	if country == "" {
		for _, c := range checkResults {
			if c.Type == models.CheckTypeGPTWeb {
				country = c.Value
				break
			}
		}
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
