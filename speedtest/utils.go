package speedtest

import (
	"context"
	C "github.com/metacubex/mihomo/constant"
	"gopkg.in/yaml.v3"
	"net"
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

func loadProxies(buf []byte, proxyUrl *url.URL) (map[string]CProxy, error) {
	rawCfg := &RawConfig{
		Proxies: []map[string]any{},
	}
	if err := yaml.Unmarshal(buf, rawCfg); err != nil {
		return nil, err
	}
	proxies := make(map[string]CProxy)
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
		proxies[name] = CProxy{Proxy: proxy, SecretConfig: config}
	}

	var (
		wg   = sync.WaitGroup{}
		lock = sync.Mutex{}
	)
	for name, config := range providersConfig {
		wg.Add(1)
		go func(name string, config ProxyProvider) {
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
func ReadProxies(configPathConfig string, proxyUrl *url.URL) (map[string]CProxy, error) {
	var (
		allProxies = make(map[string]CProxy)
		wg         = sync.WaitGroup{}
		lock       = sync.Mutex{}
	)

	for _, configPath := range strings.Split(configPathConfig, "|") {
		wg.Add(1)
		go func(configPath string) {
			defer wg.Done()
			var body []byte
			if strings.HasPrefix(configPath, "http") {
				resp, err := Request(&RequestOption{
					Method:       http.MethodGet,
					URL:          configPath,
					Headers:      map[string]string{"User-Agent": "clash-meta"},
					Timeout:      5 * time.Second,
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

func TestSpeed(proxies map[string]CProxy, options *Options) ([]Result, error) {
	results := make([]Result, 0, len(proxies))
	var err error

	var wg = sync.WaitGroup{}
	for name, proxy := range proxies {
		wg.Add(1)
		go func(name string, proxy CProxy) {
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
					if options.TestGPT {
						result.GPTResult = TestChatGPTAccess(proxy, options.Timeout)
					}

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
	option *Options
	proxy  C.Proxy
	//
	client *http.Client
}

func newProxyTest(name string, proxy C.Proxy, option *Options) *proxyTest {
	client := getClient(proxy, option.Timeout)
	return &proxyTest{
		name:   name,
		option: option,
		proxy:  proxy,
		client: client,
	}
}

func (s *proxyTest) test(downloadSize int) (time.Duration, int64, bool) {
	var (
		client         = s.client
		livenessObject = s.option.LivenessAddr
	)
	start := time.Now()
	resp, err := client.Get(fmt.Sprintf(livenessObject, downloadSize))
	if err != nil {
		//log.Debugln("failed to test proxy: %s", err)
		return 0, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode-http.StatusOK > 100 {
		return 0, 0, false
	}
	ttfb := time.Since(start)

	written, _ := io.Copy(io.Discard, resp.Body)
	if written == 0 {
		return 0, 0, false
	}
	//downloadTime := time.Since(start) - ttfb
	//bandwidth := float64(written) / downloadTime.Seconds()

	return ttfb, written, true
}

func (s *proxyTest) concurrentTest() *Result {
	var (
		name            = s.name
		option          = s.option
		downloadSize    = option.DownloadSize
		concurrentCount = option.Concurrent
	)
	if concurrentCount <= 0 {
		concurrentCount = 1
	}

	chunkSize := downloadSize / concurrentCount
	totalTTFB := int64(0)
	downloaded := int64(0)

	var wg = sync.WaitGroup{}
	start := time.Now()
	for i := 0; i < concurrentCount; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ttfb, w, ok := s.test(chunkSize)
			if ok {
				atomic.AddInt64(&downloaded, w)
				atomic.AddInt64(&totalTTFB, int64(ttfb))
			}
		}(i)
	}
	wg.Wait()
	downloadTime := time.Since(start)

	result := &Result{
		Name:      name,
		Bandwidth: float64(downloaded) / downloadTime.Seconds(),
		TTFB:      time.Duration(totalTTFB / int64(concurrentCount)),
	}

	return result
}

func TestProxyConcurrent(name string, proxy C.Proxy, option *Options) *Result {
	p := newProxyTest(name, proxy, option)
	return p.concurrentTest()
}

func getClient(proxy C.Proxy, timeout time.Duration) *http.Client {
	client := http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				var u16Port uint16
				if port, err := strconv.ParseUint(port, 10, 16); err == nil {
					u16Port = uint16(port)
				}
				return proxy.DialContext(ctx, &C.Metadata{
					Host:    host,
					DstPort: u16Port,
				})
			},
		},
	}
	return &client
}

func writeNodeConfigurationToYAML(filePath string, results []Result, proxies map[string]CProxy) error {
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

func writeToCSV(filePath string, results []Result) error {
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
