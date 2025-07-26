package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/log"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"github.com/xiecang/speedtest-clash/speedtest/requests"
	"gopkg.in/yaml.v3"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrSpeedNotTest = errors.New("请先测速")
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

func (t *Test) ReadProxies(configPathConfig string, proxyUrl *url.URL) {
	var wg sync.WaitGroup
	paths := strings.Split(configPathConfig, "|")
	wg.Add(len(paths))

	for _, configPath := range paths {
		go func(configPath string) {
			defer wg.Done()
			var body []byte
			var err error
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

			t.loadProxies(body, proxyUrl)

		}(configPath)
	}

	go func() {
		wg.Wait()
		close(t.proxiesCh)
		close(t.errCh)
	}()

}

func (t *Test) loadProxies(buf []byte, proxyUrl *url.URL) {
	rawCfg := &models.RawConfig{
		Proxies: []map[string]any{},
	}
	if err := yaml.Unmarshal(buf, rawCfg); err != nil {
		proxyList, err := convert.ConvertsV2Ray(buf)
		if err != nil {
			t.errCh <- err
			return
		}
		for _, p := range proxyList {
			proxy, err := adapter.ParseProxy(p)
			if err != nil {
				log.Warnln("ParseProxy error: proxy %s", err)
				continue
			}

			t.proxiesCh <- models.CProxy{Proxy: proxy, SecretConfig: p}
		}
		return
	}
	proxiesConfig := rawCfg.Proxies
	providersConfig := rawCfg.Providers

	for i, config := range proxiesConfig {
		proxy, err := adapter.ParseProxy(config)
		if err != nil {
			log.Warnln("ParseProxy error: proxy %d: %s", i, err)
			continue
		}

		t.proxiesCh <- models.CProxy{Proxy: proxy, SecretConfig: config}
	}

	var (
		wg = sync.WaitGroup{}
	)
	for name, config := range providersConfig {
		wg.Add(1)
		go func(name string, config models.ProxyProvider) {
			defer wg.Done()
			if name == provider.ReservedName {
				log.Warnln("can not defined a provider called `%s`", provider.ReservedName)
				return
			}
			t.ReadProxies(config.Url, proxyUrl)
			//for _, proxy := range proxiesFromProvider {
			//	proxiesCh <- proxy
			//}
		}(name, config)
	}
	wg.Wait()

}

func (t *Test) TestSpeed(ctx context.Context) ([]models.CProxyWithResult, error) {
	t.ReadProxies(t.options.ConfigPath, t.proxyUrl)
	var (
		maxConcurrency = runtime.NumCPU()
		resultsCh      = make(chan *models.CProxyWithResult, 10)
	)
	// 启动测速 worker
	go func() {
		var (
			wg  sync.WaitGroup
			sem = make(chan struct{}, maxConcurrency)
		)
		for proxy := range t.proxiesCh {
			if !t.proxyShouldKeep(proxy) {
				continue
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(p models.CProxy) {
				defer func() {
					<-sem
					wg.Done()
				}()
				result, err := testspeed(ctx, p, t.options)
				if err != nil {
					log.Errorln("[%s] test speed err: %v", p.Name(), err)
				}
				resultsCh <- result
			}(proxy)
		}
		wg.Wait()
		close(resultsCh)
	}()

	var results []models.CProxyWithResult
	var aliveProxies []models.CProxyWithResult
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context.Canceled: %v", ctx.Err())
		case err, ok := <-t.errCh:
			if ok {
				return nil, fmt.Errorf("read proxies err: %v", err)
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
				}
			}
		}
	}
}

//func (t *Test) logResults(results []models.CProxyWithResult) {
//	var format = "%s%-42s\t%-12s\t%-12s\t%-12s\t%-12s\t%-12s\t%-12s\033[0m\n"
//	fmt.Printf(format, "", "节点", "带宽", "延迟", "GPT Android", "GPT IOS", "GPT WEB", "Delay")
//	for _, result := range results {
//		result.Printf(format)
//	}
//}

//func (t *Test) LogResults() {
//	t.logResults(t.results)
//}

func (t *Test) LogNum() {
	log.Infoln("总共 %d 个节点，可访问 %d 个", len(t.results), len(t.aliveProxies))
}

//func (t *Test) LogAlive() {
//	var rs []models.Result
//	for _, result := range t.results {
//		if result.Alive() {
//			rs = append(rs, result)
//		}
//	}
//	t.logResults(rs)
//}

//func (t *Test) WriteToYaml(names ...string) error {
//	if !t._testedSpeed {
//		return ErrSpeedNotTest
//	}
//	var name string
//	if len(names) > 0 {
//		name = names[0]
//	} else {
//		name = "result.yaml"
//	}
//	return writeNodeConfigurationToYAML(name, t.results, t.proxies)
//}
//
//func (t *Test) WriteToCsv(names ...string) error {
//	if !t._testedSpeed {
//		return ErrSpeedNotTest
//	}
//
//	var name string
//	if len(names) > 0 {
//		name = names[0]
//	} else {
//		name = "result.csv"
//	}
//	return writeToCSV(name, t.results)
//}

// AliveProxiesWithResult 可访问的节点以及结果
func (t *Test) AliveProxiesWithResult() ([]models.CProxyWithResult, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed(context.Background())
	}

	return t.aliveProxies, nil
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

	return true, ""
}

func NewTest(options models.Options) (*Test, error) {
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
		options:   &options,
		proxyUrl:  proxyUrl,
		proxiesCh: make(chan models.CProxy, 10),
		errCh:     make(chan error, 1),
	}, nil
}
