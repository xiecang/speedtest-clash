package speedtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/metacubex/mihomo/log"
	"github.com/xiecang/speedtest-clash/speedtest/models"
	"net/url"
	"regexp"
	"sort"
	"time"
)

var (
	ErrSpeedNotTest = errors.New("请先测速")
)

type Test struct {
	options      *models.Options
	proxyUrl     *url.URL
	proxies      map[string]models.CProxy
	results      []models.Result
	aliveProxies []models.CProxy
	_testedSpeed bool
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

func (t *Test) TestSpeed() ([]models.Result, error) {
	var allProxies, err = ReadProxies(t.options.ConfigPath, t.proxyUrl)
	if err != nil {
		return nil, err
	}
	if len(allProxies) == 0 {
		return nil, fmt.Errorf("no proxies found")
	}
	t.proxies = allProxies
	var filteredProxies = t.filterProxies(allProxies)

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

func (t *Test) logResults(results []models.Result) {
	var format = "%s%-42s\t%-12s\t%-12s\t%-12s\t%-12s\t%-12s\t%-12s\033[0m\n"
	fmt.Printf(format, "", "节点", "带宽", "延迟", "GPT Android", "GPT IOS", "GPT WEB", "Delay")
	for _, result := range results {
		result.Printf(format)
	}
}

func (t *Test) LogResults() {
	t.logResults(t.results)
}

func (t *Test) LogNum() {
	log.Infoln("总共 %d 个节点，可访问 %d 个", len(t.proxies), len(t.aliveProxies))
}

func (t *Test) LogAlive() {
	var rs []models.Result
	for _, result := range t.results {
		if result.Alive() {
			rs = append(rs, result)
		}
	}
	t.logResults(rs)
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
func (t *Test) AliveProxies() ([]models.CProxy, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed()
	}
	var proxies []models.CProxy

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

// AliveProxiesWithResult 可访问的节点以及结果
func (t *Test) AliveProxiesWithResult() ([]models.CProxyWithResult, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed()
	}
	var (
		proxies []models.CProxyWithResult
	)

	for _, result := range t.results {
		if !result.Alive() {
			continue
		}
		var name = result.Name
		var p = t.proxies[name]
		proxies = append(proxies, models.CProxyWithResult{
			Proxies: p,
			Results: result,
		})
	}

	return proxies, nil
}

func (t *Test) Proxies() map[string]models.CProxy {
	return t.proxies
}

// AliveProxiesToJson 可访问的节点, 返回 JSON string
func (t *Test) AliveProxiesToJson() ([]byte, error) {
	if !t._testedSpeed {
		_, _ = t.TestSpeed()
	}
	var (
		ps      []map[string]any
		proxies []models.CProxy
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
		d["_check"] = result.CheckResults
		ps = append(ps, d)
	}
	t.aliveProxies = proxies
	data, err := json.Marshal(ps)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func checkOptions(options *models.Options) (bool, string) {
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
		options:  &options,
		proxyUrl: proxyUrl,
	}, nil
}
