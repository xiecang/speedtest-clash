package speedtest

import (
	"context"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
	"sync"

	"encoding/csv"
	"fmt"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/log"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	delayTestUrl = "https://cp.cloudflare.com/generate_204"
)

func loadProxies(buf []byte, proxyUrl *url.URL) (map[string]models.CProxy, error) {
	rawCfg := &models.RawConfig{
		Proxies: []map[string]any{},
	}
	if err := yaml.Unmarshal(buf, rawCfg); err != nil {
		return nil, err
	}
	proxies := make(map[string]models.CProxy)
	proxiesConfig := rawCfg.Proxies
	providersConfig := rawCfg.Providers

	for i, config := range proxiesConfig {
		proxy, err := adapter.ParseProxy(config)
		if err != nil {
			log.Warnln("ParseProxy error: proxy %d: %s", i, err)
			continue
		}

		var name = proxy.Name()
		if _, exist := proxies[proxy.Name()]; exist {
			// 粗暴重命名, 先不判断配置是否一致了，这里可以全测，结果交给外部程序去处理吧
			name += "_1"
			log.Warnln("names %s is already taken, use the name %s instead.", proxy.Name(), name)
		}
		proxies[name] = models.CProxy{Proxy: proxy, SecretConfig: config}
	}

	var (
		wg   = sync.WaitGroup{}
		lock = sync.Mutex{}
	)
	for name, config := range providersConfig {
		wg.Add(1)
		go func(name string, config models.ProxyProvider) {
			defer wg.Done()
			if name == provider.ReservedName {
				log.Warnln("can not defined a provider called `%s`", provider.ReservedName)
				return
			}
			proxiesFromProvider, err := ReadProxies(config.Url, proxyUrl)
			if err != nil {
				log.Warnln("read provider proxies error. url:%s err: %v", config.Url, err)
				return
			}

			lock.Lock()
			for proxyName, proxy := range proxiesFromProvider {
				// 这里没有处理重名问题
				proxies[fmt.Sprintf("[%s] %s", name, proxyName)] = proxy
			}
			lock.Unlock()
		}(name, config)
	}
	wg.Wait()
	return proxies, nil
}

// ReadProxies 从网络下载或者本地读取配置文件 configPathConfig 是配置地址，多个之间用 | 分割
func ReadProxies(configPathConfig string, proxyUrl *url.URL) (map[string]models.CProxy, error) {
	var (
		allProxies = make(map[string]models.CProxy)
		wg         = sync.WaitGroup{}
		lock       = sync.Mutex{}
	)

	for _, configPath := range strings.Split(configPathConfig, "|") {
		wg.Add(1)
		go func(configPath string) {
			defer wg.Done()
			var body []byte
			if strings.HasPrefix(configPath, "http") {
				resp, err := requests.Request(context.Background(), &requests.RequestOption{
					Method:       http.MethodGet,
					URL:          configPath,
					Headers:      map[string]string{"User-Agent": "clash-meta"},
					Timeout:      60 * time.Second,
					RetryTimes:   3,
					RetryTimeOut: 3 * time.Second,
					ProxyUrl:     proxyUrl,
				})
				if err != nil {
					log.Warnln("failed to fetch config: %s, err: %s", configPath, err)
					return
				}
				if resp.StatusCode != http.StatusOK {
					log.Warnln("failed to fetch config: %s, status code: %d", configPath, resp.StatusCode)
					return
				}
				body = resp.Body
			} else {
				var err error
				body, err = os.ReadFile(configPath)
				if err != nil {
					log.Warnln("failed to read config: %s, err: %s", configPath, err)
					return
				}
			}

			lps, err := loadProxies(body, proxyUrl)
			if err != nil {
				log.Warnln("failed to load proxies: %s, err: %s", configPath, err)
				return
			}

			lock.Lock()
			for k, p := range lps {
				if _, ok := allProxies[k]; !ok {
					allProxies[k] = p
				}
			}
			lock.Unlock()
		}(configPath)
	}
	wg.Wait()
	return allProxies, nil
}

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

func TestSpeed(proxies map[string]models.CProxy, options *models.Options) ([]models.Result, error) {
	results := make([]models.Result, 0, len(proxies))
	var err error

	var wg = sync.WaitGroup{}
	for name, proxy := range proxies {
		wg.Add(1)
		go func(name string, proxy models.CProxy) {
			defer wg.Done()

			// get from cache
			if cache := getCacheFromResult(name); cache != nil {
				results = append(results, *cache)
				return
			}

			var tp = proxy.Type()
			switch tp {
			case C.Shadowsocks, C.ShadowsocksR, C.Snell, C.Socks5, C.Http, C.Vmess, C.Vless, C.Trojan, C.Hysteria, C.Hysteria2, C.WireGuard, C.Tuic:
				result := TestProxyConcurrent(name, proxy, options)
				if result.Alive() {
					countryR := checkProxy(context.Background(), proxy, []models.CheckType{models.CheckTypeCountry})
					if len(countryR) > 0 {
						result.Country = countryR[0].Value
					}
					result.CheckResults = checkProxy(context.Background(), proxy, options.CheckTypes)

					if options.URLForTest != nil {
						result.URLForTest = TestURLAvailable(options.URLForTest, proxy, options.Timeout)
					}
				}
				results = append(results, *result)

				// add cache
				addResultCache(result)
			case C.Direct, C.Reject, C.Relay, C.Selector, C.Fallback, C.URLTest, C.LoadBalance:
				return
			default:
				err = fmt.Errorf("unsupported proxy type: %s", tp)
			}
		}(name, proxy)
	}
	wg.Wait()
	return results, err
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

func (s *proxyTest) test(downloadSize int) (time.Duration, int64, error) {
	var (
		client         = s.client
		livenessObject = s.option.LivenessAddr
	)
	start := time.Now()
	resp, err := client.Get(fmt.Sprintf(livenessObject, downloadSize))
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
	//downloadTime := time.Since(start) - ttfb
	//bandwidth := float64(written) / downloadTime.Seconds()

	return ttfb, written, nil
}

func (s *proxyTest) concurrentTest() *models.Result {
	var (
		name            = s.name
		option          = s.option
		downloadSize    = option.DownloadSize
		concurrentCount = option.Concurrent
	)
	if concurrentCount <= 0 {
		concurrentCount = 1
	}

	var (
		chunkSize  = downloadSize / concurrentCount
		totalTTFB  = int64(0)
		downloaded = int64(0)
		count      = int64(0)
		delay      uint16
	)

	var wg = sync.WaitGroup{}
	start := time.Now()
	for i := 0; i < concurrentCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ttfb, w, err := s.test(chunkSize)
			if err != nil {
				log.Debugln("Test: %s failed, %v", s.name, err)
			} else {
				atomic.AddInt64(&downloaded, w)
				atomic.AddInt64(&totalTTFB, int64(ttfb))
				atomic.AddInt64(&count, 1)
			}
		}(i)

		go func() {
			//	200,204
			expectedStatus, err := utils.NewUnsignedRanges[uint16]("200,204")
			if err != nil {
				return
			}

			if d, err := s.proxy.URLTest(context.Background(), delayTestUrl, expectedStatus); err == nil {
				delay = d
			}

		}()
	}
	wg.Wait()
	downloadTime := time.Since(start)

	var t time.Duration
	if count > 0 {
		t = time.Duration(totalTTFB / count)
	}

	result := &models.Result{
		Name:      name,
		Bandwidth: float64(downloaded) / downloadTime.Seconds(),
		TTFB:      t,
		Delay:     delay,
	}

	return result
}

func TestProxyConcurrent(name string, proxy C.Proxy, option *models.Options) *models.Result {
	p := newProxyTest(name, proxy, option)
	return p.concurrentTest()
}

func writeNodeConfigurationToYAML(filePath string, results []models.Result, proxies map[string]models.CProxy) error {
	fp, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer func(fp *os.File) {
		_ = fp.Close()
	}(fp)

	var sortedProxies []any
	for _, result := range results {
		if v, ok := proxies[result.Name]; ok {
			sortedProxies = append(sortedProxies, v.SecretConfig)
		}
	}

	bytes, err := yaml.Marshal(sortedProxies)
	if err != nil {
		return err
	}

	_, err = fp.Write(bytes)
	return err
}

func writeToCSV(filePath string, results []models.Result) error {
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
			fmt.Sprintf("%.2f", result.Bandwidth/1024/1024),
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
