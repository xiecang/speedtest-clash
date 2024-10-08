package speedtest

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/provider"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type CProxy struct {
	C.Proxy
	SecretConfig map[string]any
}
type GPTResult struct {
	Android bool `json:"android"`
	IOS     bool `json:"ios"`
	Web     bool `json:"web"`
}

type Result struct {
	Name      string
	Bandwidth float64
	TTFB      time.Duration
	GPTResult
	URLForTest map[string]bool
}

func (r *Result) Alive() bool {
	return r.Bandwidth > 0 && r.TTFB > 0
}

type SortField string

const (
	SortFieldBandwidth  SortField = "b"         // 带宽
	SortFieldBandwidth2 SortField = "bandwidth" // 带宽
	SortFieldTTFB       SortField = "t"         // 延迟
	SortFieldTTFB2      SortField = "ttfb"

	GPTTestURLAndroid = "https://android.chat.openai.com"
	GPTTestURLIOS     = "https://ios.chat.openai.com/"
	GPTTestURLWeb     = "https://chatgpt.com/"
	GPTTrace          = "https://chat.openai.com/cdn-cgi/trace"

	errMsg              = "Something went wrong. You may be connected to a disallowed ISP. "
	DefaultLivenessAddr = "https://speed.cloudflare.com/__down?bytes=%d"
)

var (
	gptSupportCountry = []string{"AL", "DZ", "AF", "AD", "AO", "AG", "AR", "AM", "AU", "AT", "AZ", "BS", "BH", "BD", "BB", "BE", "BZ", "BJ", "BT", "BO", "BA", "BW", "BR", "BN", "BG", "BF", "BI", "CV", "KH", "CM", "CA", "CF", "TD", "CL", "CO", "KM", "CG", "CD", "CR", "CI", "HR", "CY", "CZ", "DK", "DJ", "DM", "DO", "EC", "EG", "SV", "GQ", "ER", "EE", "SZ", "ET", "FJ", "FI", "FR", "GA", "GM", "GE", "DE", "GH", "GR", "GD", "GT", "GN", "GW", "GY", "HT", "VA", "HN", "HU", "IS", "IN", "ID", "IQ", "IE", "IL", "IT", "JM", "JP", "JO", "KZ", "KE", "KI", "KW", "KG", "LA", "LV", "LB", "LS", "LR", "LY", "LI", "LT", "LU", "MG", "MW", "MY", "MV", "ML", "MT", "MH", "MR", "MU", "MX", "FM", "MD", "MC", "MN", "ME", "MA", "MZ", "MM", "NA", "NR", "NP", "NL", "NZ", "NI", "NE", "NG", "MK", "NO", "OM", "PK", "PW", "PS", "PA", "PG", "PY", "PE", "PH", "PL", "PT", "QA", "RO", "RW", "KN", "LC", "VC", "WS", "SM", "ST", "SA", "SN", "RS", "SC", "SL", "SG", "SK", "SI", "SB", "SO", "ZA", "KR", "SS", "ES", "LK", "SR", "SE", "CH", "SD", "TW", "TJ", "TZ", "TH", "TL", "TG", "TO", "TT", "TN", "TR", "TM", "TV", "UG", "UA", "AE", "GB", "US", "UY", "UZ", "VU", "VN", "YE", "ZM", "ZW"}
)

type Options struct {
	LivenessAddr string        `json:"liveness_addr"` // 测速时调用的地址，格式如 https://speed.cloudflare.com/__down?bytes=%d
	DownloadSize int           `json:"download_size"` // 测速时下载的文件大小，单位为 bit(使用默认cloudflare的话)，默认下载10M
	Timeout      time.Duration `json:"timeout"`       // 每个代理测速的超时时间
	ConfigPath   string        `json:"config_path"`   // 配置文件地址，可以为 URL 或者本地路径，多个使用 | 分隔
	FilterRegex  string        `json:"filter_regex"`  // 通过名字过滤代理，只测试过滤部分，格式为正则，默认全部测
	SortField    SortField     `json:"sort_field"`    // 排序方式，b 带宽 t 延迟
	Concurrent   int           `json:"concurrent"`    // 下载并发数
	TestGPT      bool          `json:"test_gpt"`      // 是否检测节点支持 GPT
	URLForTest   []string      `json:"url_for_test"`  // 测试 URL 是否可访问
	ProxyUrl     string        `json:"proxy_url"`     // ConfigPath 为网络链接时可使用指定代理下载
}

var (
	red   = "\033[31m"
	green = "\033[32m"

	ErrSpeedNotTest = errors.New("请先测速")
	resultCaches    = sync.Map{}
	cacheTimeout    = 30 * time.Minute
)

func SetCacheTimeout(t time.Duration) {
	cacheTimeout = t
}

type resultCache struct {
	result    *Result
	cacheTime time.Time
}

func addResultCache(result *Result) {
	resultCaches.Store(result.Name, resultCache{
		result:    result,
		cacheTime: time.Now(),
	})
}

func getCacheFromResult(name string) *Result {
	if cache, ok := resultCaches.Load(name); ok {
		r := cache.(resultCache)
		if time.Since(r.cacheTime) < cacheTimeout {
			return r.result
		} else {
			resultCaches.Delete(name)
		}
	}
	return nil
}

func clearResultCache() {
	resultCaches.Range(func(key, value interface{}) bool {
		if time.Since(value.(resultCache).cacheTime) > cacheTimeout {
			resultCaches.Delete(key)
		}
		return true
	})
}

func init() {
	go func() {
		for {
			time.Sleep(cacheTimeout)
			clearResultCache()
		}
	}()
}

// https://wiki.metacubex.one/en/config/proxy-providers/
// proxy-providers:
//  provider1:
//    type: http
//    url: "http://test.com"
//    path: ./proxy_providers/provider1.yaml

type ProxyProvider struct {
	Url string `json:"url"`
	// 支持自定义 header，后续再加吧
}

type RawConfig struct {
	Providers map[string]ProxyProvider `yaml:"proxy-providers"`
	Proxies   []map[string]any         `yaml:"proxies"`
}

type Test struct {
	options      *Options
	proxyUrl     *url.URL
	proxies      map[string]CProxy
	results      []Result
	aliveProxies []CProxy
	_testedSpeed bool
}

func checkOptions(options *Options) (bool, string) {
	if options.ConfigPath == "" {
		return false, "配置不能为空"
	}
	if options.DownloadSize == 0 {
		options.DownloadSize = 10 * 1024 * 1024
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	if options.SortField == "" {
		options.SortField = SortFieldBandwidth
	}
	if options.LivenessAddr == "" {
		options.LivenessAddr = DefaultLivenessAddr
	}

	return true, ""
}

func NewTest(options Options) (*Test, error) {
	if ok, msg := checkOptions(&options); !ok {
		return nil, fmt.Errorf("配置格式不正确: %s", msg)
	}
	var proxyUrl *url.URL
	if options.ProxyUrl != "" {
		var err error
		if proxyUrl, err = url.Parse(options.ProxyUrl); err != nil {
			return nil, fmt.Errorf("ProxyUrl 错误 %w", err)
		}
	}

	return &Test{
		options:  &options,
		proxyUrl: proxyUrl,
	}, nil
}

func (t *Test) TestSpeed() ([]Result, error) {
	var allProxies, err = ReadProxies(t.options.ConfigPath, t.proxyUrl)
	if err != nil {
		return nil, err
	}
	if len(allProxies) == 0 {
		return nil, fmt.Errorf("no proxies found")
	}
	t.proxies = allProxies
	var filteredProxies = filterProxies(t.options.FilterRegex, allProxies)

	result, err := TestSpeed(filteredProxies, t.options)
	if err != nil {
		return nil, err
	}
	result, err = sortResult(result, t.options.SortField)
	if err != nil {
		return nil, err
	}

	t.results = result
	t._testedSpeed = true
	return t.results, nil
}

func (t *Test) LogResults() {
	LogResult(t.results)
}

func (t *Test) LogNum() {
	log.Infoln("总共 %d 个节点，可访问 %d 个", len(t.proxies), len(t.aliveProxies))
}
func (t *Test) LogAlive() {
	var format = "%s%-42s\t%-12s\t%-12s\t%-12s\t%-12s\t%-12s\033[0m\n"
	fmt.Printf(format, "", "节点", "带宽", "延迟", "GPT Android", "GPT IOS", "GPT WEB")
	for _, result := range t.results {
		if !result.Alive() {
			continue
		}
		result.Printf(format)
	}
}

func (t *Test) WriteToYaml(names ...string) error {
	if !t._testedSpeed {
		return ErrSpeedNotTest
	}
	var name string
	if len(names) > 0 {
		name = names[0]
	} else {
		name = "result.yaml"
	}
	return writeNodeConfigurationToYAML(name, t.results, t.proxies)
}

func (t *Test) WriteToCsv(names ...string) error {
	if !t._testedSpeed {
		return ErrSpeedNotTest
	}

	var name string
	if len(names) > 0 {
		name = names[0]
	} else {
		name = "result.csv"
	}
	return writeToCSV(name, t.results)
}

// AliveProxies 可访问的节点
func (t *Test) AliveProxies() ([]CProxy, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed()
	}
	var proxies []CProxy

	for _, result := range t.results {
		if !result.Alive() {
			continue
		}
		var name = result.Name
		var p = t.proxies[name]
		proxies = append(proxies, p)
	}
	t.aliveProxies = proxies
	return proxies, nil
}

type CProxyWithResult struct {
	Proxies CProxy
	Results Result
}

// AliveProxiesWithResult 可访问的节点以及结果
func (t *Test) AliveProxiesWithResult() ([]CProxyWithResult, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed()
	}
	var (
		proxies []CProxyWithResult
	)

	for _, result := range t.results {
		if !result.Alive() {
			continue
		}
		var name = result.Name
		var p = t.proxies[name]
		proxies = append(proxies, CProxyWithResult{
			Proxies: p,
			Results: result,
		})
	}

	return proxies, nil
}

func (t *Test) Proxies() map[string]CProxy {
	return t.proxies
}

// AliveProxiesToJson 可访问的节点, 返回 JSON string
func (t *Test) AliveProxiesToJson() ([]byte, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed()
	}
	var (
		ps      []map[string]any
		proxies []CProxy
	)

	for _, result := range t.results {
		if !result.Alive() {
			continue
		}
		var name = result.Name
		var p = t.proxies[name]
		proxies = append(proxies, p)
		// 把 GPT 测试信息放入
		d := p.SecretConfig
		d["_gpt"] = result.GPTResult
		ps = append(ps, d)
	}
	t.aliveProxies = proxies
	data, err := json.Marshal(ps)
	if err != nil {
		return nil, err
	}
	return data, nil
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

func LogResult(results []Result) {
	var format = "%s%-42s\t%-12s\t%-12s\033[0m\n"
	fmt.Printf(format, "", "节点", "带宽", "延迟", "GPT Android", "GPT IOS", "GPT WEB")
	for _, result := range results {
		result.Printf(format)
	}
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

func sortResult(results []Result, sortField SortField) ([]Result, error) {
	if sortField == "" {
		return results, nil
	}
	var err error
	switch sortField {
	case SortFieldBandwidth, SortFieldBandwidth2:
		sort.Slice(results, func(i, j int) bool {
			return results[i].Bandwidth > results[j].Bandwidth
		})
		fmt.Println("\n\n===结果按照带宽排序===")
	case SortFieldTTFB, SortFieldTTFB2:
		sort.Slice(results, func(i, j int) bool {
			return results[i].TTFB < results[j].TTFB
		})
		fmt.Println("\n\n===结果按照延迟排序===")
	default:
		err = fmt.Errorf("unsupported sort field: %s", sortField)
	}
	return results, err
}

// filterProxies 过滤出指定的代理 filter 是过滤的正则表达式，proxies 是代理
func filterProxies(filter string, proxies map[string]CProxy) map[string]CProxy {
	if filter == "" {
		return proxies
	}
	filterRegexp := regexp.MustCompile(filter)
	filteredProxies := make(map[string]CProxy)
	for name, proxy := range proxies {
		if filterRegexp.MatchString(name) {
			filteredProxies[name] = proxy
		}
	}
	return filteredProxies
}

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

func (r *Result) Printf(format string) {
	color := ""
	if r.Bandwidth < 1024*1024 {
		color = red
	} else if r.Bandwidth > 1024*1024*10 {
		color = green
	}
	fmt.Printf(format, color, formatName(r.Name), formatBandwidth(r.Bandwidth), formatMilliseconds(r.TTFB),
		formatGPT(r.GPTResult.Android), formatGPT(r.GPTResult.IOS), formatGPT(r.GPTResult.Web))
}

func TestProxyConcurrent(name string, proxy C.Proxy, option *Options) *Result {
	var (
		downloadSize    = option.DownloadSize
		timeout         = option.Timeout
		concurrentCount = option.Concurrent
		livenessObject  = option.LivenessAddr
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
			result, w := TestProxy(name, proxy, chunkSize, timeout, livenessObject)
			if w != 0 {
				atomic.AddInt64(&downloaded, w)
				atomic.AddInt64(&totalTTFB, int64(result.TTFB))
			}
			wg.Done()
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

func TestProxy(name string, proxy C.Proxy, downloadSize int, timeout time.Duration, livenessObject string) (*Result, int64) {
	client := getClient(proxy, timeout)
	start := time.Now()
	resp, err := client.Get(fmt.Sprintf(livenessObject, downloadSize))
	if err != nil {
		//log.Debugln("failed to test proxy: %s", err)
		return &Result{Name: name, Bandwidth: -1, TTFB: -1}, 0
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	if resp.StatusCode-http.StatusOK > 100 {
		return &Result{Name: name, Bandwidth: -1, TTFB: -1}, 0
	}
	ttfb := time.Since(start)

	written, _ := io.Copy(io.Discard, resp.Body)
	if written == 0 {
		return &Result{Name: name, Bandwidth: -1, TTFB: -1}, 0
	}
	downloadTime := time.Since(start) - ttfb
	bandwidth := float64(written) / downloadTime.Seconds()

	return &Result{Name: name, Bandwidth: bandwidth, TTFB: ttfb}, written
}

var (
	emojiRegex = regexp.MustCompile(`[\x{1F600}-\x{1F64F}\x{1F300}-\x{1F5FF}\x{1F680}-\x{1F6FF}\x{2600}-\x{26FF}\x{1F1E0}-\x{1F1FF}]`)
	spaceRegex = regexp.MustCompile(`\s{2,}`)
)

func formatName(name string) string {
	noEmoji := emojiRegex.ReplaceAllString(name, "")
	mergedSpaces := spaceRegex.ReplaceAllString(noEmoji, " ")
	return strings.TrimSpace(mergedSpaces)
}

func formatBandwidth(v float64) string {
	if v <= 0 {
		return "N/A"
	}
	if v < 1024 {
		return fmt.Sprintf("%.02fB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fKB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fMB/s", v)
	}
	v /= 1024
	if v < 1024 {
		return fmt.Sprintf("%.02fGB/s", v)
	}
	v /= 1024
	return fmt.Sprintf("%.02fTB/s", v)
}

func formatMilliseconds(v time.Duration) string {
	if v <= 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.02fms", float64(v.Milliseconds()))
}
func formatGPT(v bool) string {
	if v {
		return "是"
	}
	return "否"
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

type response struct {
	CFDetails string `json:"cf_details"`
}

func requestChatGPT(url string, proxy C.Proxy, timeout time.Duration) (*response, error) {
	client := getClient(proxy, timeout)
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	if err != nil {
		return nil, err
	}

	var data response
	if err = json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	return &data, err
}

func testChatGPTAccessWeb(proxy C.Proxy, timeout time.Duration) bool {
	// 1. 访问 https://chat.openai.com/ 如果无响应就不可用
	// 2. 访问 https://chat.openai.com/cdn-cgi/trace 获取 loc= 国家码，查看是否支持
	client := getClient(proxy, timeout)
	resp, err := client.Get(GPTTestURLWeb)
	if err != nil {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return false
	}
	//
	resp, err = client.Get(GPTTrace)
	if err != nil {
		return false
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return false
	}
	lines := strings.Split(string(body), "\n")
	var loc string
	for _, line := range lines {
		if !strings.Contains(line, "loc=") {
			continue
		}
		loc = strings.Replace(line, "loc=", "", 1)
	}
	for _, c := range gptSupportCountry {
		if strings.Compare(loc, c) == 0 {
			return true
		}
	}
	return false
}
func TestChatGPTAccess(proxy C.Proxy, timeout time.Duration) GPTResult {
	type gptResultSync struct {
		Android atomic.Bool
		IOS     atomic.Bool
		Web     atomic.Bool
	}

	var res = gptResultSync{}

	wg := sync.WaitGroup{}

	wg.Add(3)

	go func() {
		defer wg.Done()

		if resp, err := requestChatGPT(GPTTestURLAndroid, proxy, timeout); err == nil {
			if !strings.Contains(resp.CFDetails, errMsg) {
				res.Android.Store(true)
			}
		}
	}()

	go func() {
		defer wg.Done()
		if resp, err := requestChatGPT(GPTTestURLIOS, proxy, timeout); err == nil {
			if !strings.Contains(resp.CFDetails, errMsg) {
				res.IOS.Store(true)
			}
		}

	}()

	go func() {
		defer wg.Done()
		if ok := testChatGPTAccessWeb(proxy, timeout); ok {
			res.Web.Store(true)
		}
	}()

	wg.Wait()
	// ● 若不可用会提示
	//{"cf_details":"Something went wrong. You may be connected to a disallowed ISP. If you are using VPN, try disabling it. Otherwise try a different Wi-Fi network or data connection."}
	//
	//● 可用提示
	//{"cf_details":"Request is not allowed. Please try again later.", "type":"dc"}
	var r = GPTResult{
		Android: res.Android.Load(),
		IOS:     res.IOS.Load(),
		Web:     res.Web.Load(),
	}
	if !r.Web {
		r.Android = r.Web
		r.IOS = r.Web
	}
	return r
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
			client := getClient(proxy, timeout)
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
