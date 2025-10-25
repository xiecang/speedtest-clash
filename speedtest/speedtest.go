package speedtest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/log"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
)

var (
	ErrSpeedNotTest = errors.New("请先测速")
	ErrSpeedNoAlive = errors.New("无健康节点")
	cpuCount        = runtime.NumCPU()
)

type Test struct {
	options  *models.Options
	proxyUrl *url.URL
	//proxies      map[string]models.CProxy
	results      []models.CProxyWithResult
	aliveProxies []models.CProxyWithResult
	_testedSpeed bool

	regexpContain    *regexp.Regexp
	regexpNonContain *regexp.Regexp

	proxiesCh chan models.CProxy
	errCh     chan error
	//
	totalCount   *int32 // 计数器，记录总节点数量
	count        *int32 // 计数器，记录已测速的节点数量
	invalidCount *int32 // 计数器，记录无效节点数量
	aliveCount   *int32 // 计数器，记录有效节点数量

	// 自动进度输出相关
	logTicker *time.Ticker
	testing   atomic.Bool // 测速状态
}

func (t *Test) checkAndLogProgress() {
	processed := atomic.LoadInt32(t.count)
	total := atomic.LoadInt32(t.totalCount)
	alive := atomic.LoadInt32(t.aliveCount)
	invalid := atomic.LoadInt32(t.invalidCount)
	now := time.Now()
	//
	percentage := float64(processed) / float64(total) * 100
	fmt.Printf("[%s] 📊 进度: %d/%d (%.1f%%) | ✅ 有效: %d | ❌ 无效: %d\n",
		now.Format("15:04:05"), processed, total, percentage, alive, invalid)
}

func (t *Test) startAutoProgress() {
	t.logTicker = time.NewTicker(3 * time.Second) // 每3秒输出一次
	go func() {
		for range t.logTicker.C {
			if !t.testing.Load() {
				break
			}
			t.checkAndLogProgress()
		}
	}()
}

func sortResult(results []models.Result, sortField models.SortField) ([]models.Result, error) {
	if sortField == "" {
		return results, nil
	}
	var err error
	switch sortField {
	case models.SortFieldBandwidth, models.SortFieldBandwidth2:
		sort.Slice(results, func(i, j int) bool {
			return results[i].Bandwidth > results[j].Bandwidth
		})
		fmt.Println("\n\n===结果按照带宽排序===")
	case models.SortFieldTTFB, models.SortFieldTTFB2:
		sort.Slice(results, func(i, j int) bool {
			return results[i].TTFB < results[j].TTFB
		})
		fmt.Println("\n\n===结果按照延迟排序===")
	default:
		err = fmt.Errorf("unsupported sort field: %s", sortField)
	}
	return results, err
}

// proxyShouldKeep 根据正则等，判断是否应该对此节点测速
func (t *Test) proxyShouldKeep(proxy models.CProxy) bool {
	var (
		regexContain    = t.options.NameRegexContain
		regexNonContain = t.options.NameRegexNonContain
	)
	if regexContain != "" && t.regexpContain == nil {
		t.regexpContain = regexp.MustCompile(regexContain)
	}
	if regexNonContain != "" && t.regexpNonContain == nil {
		t.regexpNonContain = regexp.MustCompile(regexNonContain)
	}
	if t.regexpContain == nil && t.regexpNonContain == nil {
		return true
	}
	if t.regexpContain != nil {
		name := proxy.Name()
		if t.regexpContain.MatchString(name) {
			return true
		}
	} else {
		return true
	}
	return false
}

// filterProxies 过滤出指定的代理 filter 是过滤的正则表达式，proxies 是代理
func (t *Test) filterProxies(proxies map[string]models.CProxy) map[string]models.CProxy {
	var (
		regexpContain    *regexp.Regexp
		regexpNonContain *regexp.Regexp
		regexContain     = t.options.NameRegexContain
		regexNonContain  = t.options.NameRegexNonContain
	)
	if regexContain != "" {
		regexpContain = regexp.MustCompile(regexContain)
	}
	if regexNonContain != "" {
		regexpNonContain = regexp.MustCompile(regexNonContain)
	}
	if regexpContain == nil && regexpNonContain == nil {
		return proxies
	}

	filteredProxies := make(map[string]models.CProxy)
	for name, proxy := range proxies {
		if regexpNonContain != nil && regexpNonContain.MatchString(name) {
			continue
		}
		if regexpContain != nil {
			if regexpContain.MatchString(name) {
				filteredProxies[name] = proxy
			}
		} else {
			filteredProxies[name] = proxy
		}
	}
	return filteredProxies
}

func (t *Test) ReadProxies(ctx context.Context, wg *sync.WaitGroup, configPathConfig string, proxyUrl *url.URL) {
	paths := strings.Split(configPathConfig, "|")
	wg.Add(len(paths))

	for _, configPath := range paths {
		go func(configPath string) {
			defer wg.Done()
			var body []byte
			var err error
			if strings.HasPrefix(configPath, "http") {
				resp, err := requests.Request(ctx, &requests.RequestOption{
					Method:             http.MethodGet,
					URL:                configPath,
					Headers:            map[string]string{"User-Agent": "clash-meta"},
					Timeout:            20 * time.Second,
					RetryTimes:         3,
					RetryTimeOut:       3 * time.Second,
					ProxyUrl:           proxyUrl,
					InsecureSkipVerify: true,
				})
				if err != nil {
					log.Warnln("failed to fetch config: %s, err: %s", configPath, err)
					t.errCh <- err
					return
				}
				if resp.StatusCode != http.StatusOK {
					log.Warnln("failed to fetch config: %s, status code: %d", configPath, resp.StatusCode)
					t.errCh <- fmt.Errorf("status code: %d", resp.StatusCode)
					return
				}
				body = resp.Body
			} else {
				body, err = os.ReadFile(configPath)
				if err != nil {
					log.Warnln("failed to read local file: %s, err: %s", configPath, err)
					t.errCh <- err
					return
				}
			}

			t.loadProxies(ctx, wg, body, proxyUrl)

		}(configPath)
	}

}

func (t *Test) loadProxies(ctx context.Context, wg *sync.WaitGroup, buf []byte, proxyUrl *url.URL) {
	rawCfg := &models.RawConfig{
		Proxies: []map[string]any{},
	}
	if !bytes.Contains(buf, []byte("server")) {
		if bytes.Contains(buf, []byte("://")) {
			// 纯订阅
			buf = []byte(base64.StdEncoding.EncodeToString(buf))
		}
		proxyList, err := convert.ConvertsV2Ray(buf)
		if err != nil {
			t.errCh <- err
			return
		}
		atomic.AddInt32(t.totalCount, int32(len(proxyList)))
		for _, p := range proxyList {
			proxy, err := adapter.ParseProxy(p)
			if err != nil {
				atomic.AddInt32(t.invalidCount, 1)
				log.Warnln("ParseProxy error: proxy %s", err)
				continue
			}

			t.proxiesCh <- models.CProxy{Proxy: proxy, SecretConfig: p}
		}
		return
	}
	if err := yaml.Unmarshal(buf, rawCfg); err != nil {
		t.errCh <- err
		return
	}
	proxiesConfig := rawCfg.Proxies
	providersConfig := rawCfg.Providers
	atomic.AddInt32(t.totalCount, int32(len(proxiesConfig)))

	for i, config := range proxiesConfig {
		proxy, err := adapter.ParseProxy(config)
		if err != nil {
			atomic.AddInt32(t.invalidCount, 1)
			log.Warnln("ParseProxy error: proxy %d: %s", i, err)
			continue
		}

		t.proxiesCh <- models.CProxy{Proxy: proxy, SecretConfig: config}
	}

	for name, config := range providersConfig {
		wg.Add(1)
		go func(name string, config models.ProxyProvider) {
			defer wg.Done()
			if name == provider.ReservedName {
				log.Warnln("can not defined a provider called `%s`", provider.ReservedName)
				return
			}
			t.ReadProxies(ctx, wg, config.Url, proxyUrl)
		}(name, config)
	}

}

func (t *Test) TestSpeed(ctx context.Context) ([]models.CProxyWithResult, error) {
	// 检查是否已经在测速
	if t.testing.Load() {
		return nil, fmt.Errorf("测速正在进行中，请等待完成")
	}

	t.testing.Store(true)
	defer t.testing.Store(false)

	var wg sync.WaitGroup
	t.ReadProxies(ctx, &wg, t.options.ConfigPath, t.proxyUrl)

	t.startAutoProgress()

	var (
		maxConcurrency = t.options.Concurrent
		resultsCh      = make(chan *models.CProxyWithResult, 10)
	)

	var workerWg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)

	// worker 处理
	go func() {
		for proxy := range t.proxiesCh {
			if !t.proxyShouldKeep(proxy) {
				atomic.AddInt32(t.count, 1)
				continue
			}
			sem <- struct{}{}
			workerWg.Add(1)
			go func(p models.CProxy) {
				defer func() {
					if r := recover(); r != nil {
						log.Errorln("worker panic: %v", r)
					}
					atomic.AddInt32(t.count, 1)
					workerWg.Done()
					<-sem
				}()
				proxyCtx, cancel := context.WithTimeout(ctx, t.options.Timeout+1*time.Minute)
				defer cancel()
				result, err := testspeed(proxyCtx, p, t.options)
				if err != nil {
					log.Errorln("[%s] test speed err: %v", p.Name(), err)
				}
				resultsCh <- result
			}(proxy)
		}
		workerWg.Wait()
		close(resultsCh)
	}()

	go func() {
		wg.Wait()
		close(t.proxiesCh)
		close(t.errCh)
	}()

	var results []models.CProxyWithResult
	var aliveProxies []models.CProxyWithResult
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context.Canceled: %v", ctx.Err())
		case err, ok := <-t.errCh:
			if ok {
				log.Errorln("load proxies err: %s", err)
				continue
			}
		case result, ok := <-resultsCh:
			if !ok {
				t._testedSpeed = true
				t.results = results
				t.aliveProxies = aliveProxies
				return t.results, nil
			}
			if result != nil {
				results = append(results, *result)
				if result.Alive() {
					aliveProxies = append(aliveProxies, *result)
					atomic.AddInt32(t.aliveCount, 1)
				}
			}
		}
	}
}

func (t *Test) TotalCount() int32 {
	return atomic.LoadInt32(t.totalCount)
}

func (t *Test) InvalidCount() int32 {
	return atomic.LoadInt32(t.invalidCount)
}

// ProcessCount 返回已处理的节点数量
func (t *Test) ProcessCount() int32 {
	return atomic.LoadInt32(t.count)
}

func (t *Test) LogNum() {
	now := time.Now().Format("15:04:05")
	total := atomic.LoadInt32(t.totalCount)
	invalid := atomic.LoadInt32(t.invalidCount)
	alive := len(t.aliveProxies)
	processed := atomic.LoadInt32(t.count)

	fmt.Printf("\n[%s] 🎯 测速完成！\n", now)
	fmt.Printf("📊 统计信息:\n")
	fmt.Printf("   • 总节点数: %d\n", total)
	fmt.Printf("   • 已处理: %d\n", processed)
	fmt.Printf("   • ✅ 有效节点: %d\n", alive)
	fmt.Printf("   • ❌ 无效节点: %d\n", invalid)
	fmt.Printf("   • 📈 有效率: %.1f%%\n", float64(alive)/float64(processed)*100)
}

func (t *Test) LogAlive() {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Name\t节点\t带宽\t首字节时间\t延迟\t国家\t链接测试\t其它")
	for _, result := range t.aliveProxies {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			result.Name,
			result.Proxy.Addr(),
			result.FormattedBandwidth(),
			result.FormattedTTFB(),
			result.Delay,
			result.Country,
			result.FormattedUrlCheck(),
			result.FormattedCheckResult(),
		)
	}
	_ = w.Flush()
}

func (t *Test) WriteToYaml(names ...string) error {
	if !t._testedSpeed {
		return ErrSpeedNotTest
	}
	if len(t.aliveProxies) == 0 {
		return ErrSpeedNoAlive
	}
	var name string
	if len(names) > 0 {
		name = names[0]
	} else {
		name = "result.yaml"
	}
	return writeNodeConfigurationToYAML(name, t.aliveProxies)
}

func (t *Test) WriteToCsv(names ...string) error {
	if !t._testedSpeed {
		return ErrSpeedNotTest
	}
	if len(t.aliveProxies) == 0 {
		return ErrSpeedNoAlive
	}

	var name string
	if len(names) > 0 {
		name = names[0]
	} else {
		name = "result.csv"
	}
	return writeToCSV(name, t.aliveProxies)
}

// AliveProxiesWithResult 可访问的节点以及结果
func (t *Test) AliveProxiesWithResult() ([]models.CProxyWithResult, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed(context.Background())
	}

	return t.aliveProxies, nil
}

// ProxiesWithResult 合法的节点以及结果
func (t *Test) ProxiesWithResult() ([]models.CProxyWithResult, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed(context.Background())
	}

	return t.results, nil
}

//func (t *Test) Proxies() map[string]models.CProxy {
//	return t.proxies
//}

// AliveProxiesToJson 可访问的节点, 返回 JSON string
func (t *Test) AliveProxiesToJson() ([]byte, error) {
	ps, err := t.AliveProxies()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(ps)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// ProxiesToJson 可访问的节点, 返回 JSON string
func (t *Test) ProxiesToJson() ([]byte, error) {
	ps, err := t.Proxies()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(ps)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// AliveProxies 可访问的节点
func (t *Test) AliveProxies() ([]map[string]interface{}, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed(context.Background())
	}
	var (
		ps []map[string]any
	)

	for _, proxy := range t.aliveProxies {
		d := proxy.Proxy.SecretConfig
		d["_check"] = proxy.CheckResults
		ps = append(ps, d)

	}

	return ps, nil
}

// Proxies 合法的节点
func (t *Test) Proxies() ([]map[string]interface{}, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed(context.Background())
	}
	var (
		ps []map[string]any
	)

	for _, proxy := range t.results {
		d := proxy.Proxy.SecretConfig
		d["_check"] = proxy.CheckResults
		ps = append(ps, d)
	}

	return ps, nil
}

func checkOptions(options *models.Options) (bool, string) {
	if options.ConfigPath == "" {
		return false, "配置不能为空"
	}
	if options.DownloadSize == 0 {
		options.DownloadSize = 100 * 1024 * 1024
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	if options.SortField == "" {
		options.SortField = models.SortFieldBandwidth
	}
	if options.LivenessAddr == "" {
		options.LivenessAddr = models.DefaultLivenessAddr
	}
	if options.Concurrent == 0 {
		options.Concurrent = cpuCount * 3
	}

	return true, ""
}

func NewTest(options models.Options) (*Test, error) {
	if ok, msg := checkOptions(&options); !ok {
		return nil, fmt.Errorf("配置格式不正确: %s", msg)
	}

	// 如果没有设置缓存，使用单例默认缓存
	if options.Cache == nil {
		options.Cache = GetDefaultCache()
	}

	var proxyUrl *url.URL
	if options.ProxyUrl != "" {
		var err error
		if proxyUrl, err = url.Parse(options.ProxyUrl); err != nil {
			return nil, fmt.Errorf("ProxyUrl 错误 %w", err)
		}
	}

	return &Test{
		options:      &options,
		proxyUrl:     proxyUrl,
		proxiesCh:    make(chan models.CProxy, cpuCount*10),
		errCh:        make(chan error, 1),
		totalCount:   new(int32),
		count:        new(int32),
		invalidCount: new(int32),
		aliveCount:   new(int32),
	}, nil
}

// Close 关闭测试实例，清理资源
func (t *Test) Close() error {
	// 停止自动进度输出
	if t.logTicker != nil {
		t.logTicker.Stop()
	}

	if t.options.Cache != nil {
		return t.options.Cache.Close()
	}

	return nil
}
