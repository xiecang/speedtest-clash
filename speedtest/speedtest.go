package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/xiecang/speedtest-clash/speedtest/models"
)

var (
	ErrSpeedNotTest = errors.New("请先测速")
	ErrSpeedNoAlive = errors.New("无健康节点")
	cpuCount        = runtime.NumCPU()
)

type testState int

const (
	stateNew testState = iota
	stateRunning
	stateInputClosed
	stateStopped
)

type Test struct {
	options      *models.Options
	proxyUrl     *url.URL
	results      []models.CProxyWithResult
	aliveProxies []models.CProxyWithResult
	_testedSpeed bool

	regexpContain    *regexp.Regexp
	regexpNonContain *regexp.Regexp

	proxiesCh chan models.CProxy
	cancel    context.CancelFunc
	stateMu   sync.Mutex
	state     testState
	closeOnce sync.Once
	//
	totalCount   *int32 // 计数器，记录总节点数量
	count        *int32 // 计数器，记录已测速的节点数量
	invalidCount *int32 // 计数器，记录无效节点数量（仅测速阶段）
	aliveCount   *int32 // 计数器，记录有效节点数量

	// 自动进度输出相关
	logTicker *time.Ticker
	stopChan  chan struct{} // 用于停止进度输出
	testing   atomic.Bool   // 测速状态

	bandwidthLimiter *models.BandwidthLimiter
}

func (t *Test) checkAndLogProgress() {
	processed := atomic.LoadInt32(t.count)
	total := atomic.LoadInt32(t.totalCount)
	alive := atomic.LoadInt32(t.aliveCount)
	invalid := atomic.LoadInt32(t.invalidCount)
	now := time.Now()

	// 防止除零
	var percentage float64
	if total > 0 {
		percentage = float64(processed) / float64(total) * 100
	}

	// 进度条视觉效果
	const barWidth = 20
	done := 0
	if total > 0 {
		done = int(float64(barWidth) * float64(processed) / float64(total))
	}
	if done > barWidth {
		done = barWidth
	}
	bar := strings.Repeat("█", done) + strings.Repeat("░", barWidth-done)

	// 使用 \r 回到行首，使用 \033[K 清除当前行光标后的内容
	fmt.Printf("\r[%s] 📊 %s %d/%d (%.1f%%) | ✅ %d | ❌ %d\033[K",
		now.Format("15:04:05"), bar, processed, total, percentage, alive, invalid)
}

func (t *Test) startAutoProgress() {
	if !t.options.Progress.PrintProgress {
		return
	}
	interval := t.options.Progress.ProgressInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	t.logTicker = time.NewTicker(interval)
	// We don't re-initialize stopChan if it's already there
	go func() {
		defer t.logTicker.Stop()
		for {
			select {
			case <-t.logTicker.C:
				if !t.testing.Load() {
					return
				}
				t.checkAndLogProgress()
			case <-t.stopChan:
				return
			}
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
// 注意：正则表达式已在 NewTest 时预编译，这里直接使用
func (t *Test) proxyShouldKeep(proxy models.CProxy) bool {
	if t.regexpContain == nil && t.regexpNonContain == nil {
		return true
	}

	name := proxy.Name()

	// 如果设置了 regexpNonContain，且匹配，则排除
	if t.regexpNonContain != nil && t.regexpNonContain.MatchString(name) {
		return false
	}

	// 如果设置了 regexpContain，必须匹配才测试
	if t.regexpContain != nil {
		return t.regexpContain.MatchString(name)
	}

	// 只设置了 regexpNonContain 且未匹配，则测试
	return true
}

// processProxy 内部测速节点处理核心逻辑（计入已处理 count，但不计入总量 totalCount）
func (t *Test) processProxy(ctx context.Context, proxy models.CProxy) {
	if !t.proxyShouldKeep(proxy) {
		atomic.AddInt32(t.count, 1) // 跳过的节点也算作已处理
		return
	}
	// 优先非阻塞入队，避免 ctx.Done() 与 channel send 同时就绪时的随机丢弃问题。
	select {
	case t.proxiesCh <- proxy:
		return
	default:
	}
	// 缓冲区已满时阻塞等待，同时监听 ctx 取消。
	select {
	case t.proxiesCh <- proxy:
	case <-ctx.Done():
		atomic.AddInt32(t.count, 1)
	}
}

func (t *Test) ensureCanAdd() error {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	switch t.state {
	case stateRunning:
		return nil
	case stateInputClosed:
		return fmt.Errorf("测速输入已关闭")
	case stateStopped:
		return fmt.Errorf("测速已停止")
	default:
		return fmt.Errorf("测速未开始")
	}
}

// AddProxy 实时添加一个待测速节点
func (t *Test) AddProxy(ctx context.Context, proxy models.CProxy) error {
	if err := t.ensureCanAdd(); err != nil {
		return err
	}
	atomic.AddInt32(t.totalCount, 1) // 外部调用入口，统一在此增加总量
	t.processProxy(ctx, proxy)
	return nil
}

// processConfig 内部测速节点配置解析与处理逻辑（计入已处理/无效 count，但不计入总量 totalCount）
func (t *Test) processConfig(ctx context.Context, config map[string]any) error {
	// 优化：提前过滤，避免不必要的解析开销
	if name, ok := config["name"].(string); ok && name != "" {
		if (t.regexpNonContain != nil && t.regexpNonContain.MatchString(name)) ||
			(t.regexpContain != nil && !t.regexpContain.MatchString(name)) {
			// 如果被过滤了，增加已处理计数
			atomic.AddInt32(t.count, 1)
			return nil
		}
	}

	// 强制设置 skip-cert-verify
	if t.options.ForceCertVerify {
		if _, exists := config["skip-cert-verify"]; exists {
			config["skip-cert-verify"] = false
		}
	}

	proxy, err := adapter.ParseProxy(config)
	if err != nil {
		atomic.AddInt32(t.invalidCount, 1)
		atomic.AddInt32(t.count, 1) // 解析失败也算处理过
		warnf(t.options, "ParseProxy error: %s", err)
		return err
	}
	t.processProxy(ctx, models.CProxy{Proxy: proxy, SecretConfig: config})
	return nil
}

// AddProxies 实时批量添加待测速节点配置
func (t *Test) AddProxies(ctx context.Context, configs []map[string]any) error {
	if len(configs) == 0 {
		return nil
	}
	if err := t.ensureCanAdd(); err != nil {
		return err
	}

	concurrency := runtime.GOMAXPROCS(0) * 2
	if len(configs) < concurrency {
		concurrency = len(configs)
	}

	atomic.AddInt32(t.totalCount, int32(len(configs))) // 批量入口，提前计算总量

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	var ctxErr error
	for _, config := range configs {
		select {
		case <-ctx.Done():
			ctxErr = ctx.Err()
			goto waitAndReturn
		case sem <- struct{}{}:
			wg.Add(1)
			go func(c map[string]any) {
				defer wg.Done()
				defer func() { <-sem }()
				_ = t.processConfig(ctx, c)
			}(config)
		}
	}
waitAndReturn:
	// 必须等待所有已启动的 goroutine 完成，防止它们在 proxiesCh 关闭后继续发送导致 panic。
	wg.Wait()
	return ctxErr
}

func (t *Test) AddProxyURL(ctx context.Context, source string) error {
	return t.AddProxyURLs(ctx, []string{source})
}

func (t *Test) AddProxyURLs(ctx context.Context, sources []string) error {
	if len(sources) == 0 {
		return nil
	}
	if err := t.ensureCanAdd(); err != nil {
		return err
	}
	loader := &ProxySourceLoader{ProxyURL: t.proxyUrl, Options: t.options}
	return loader.LoadManyStreamBatched(ctx, sources, t.options.SourceBatchSize, func(proxies []map[string]any) error {
		return t.AddProxies(ctx, proxies)
	})
}

func (t *Test) TestSpeed(ctx context.Context) ([]models.CProxyWithResult, error) {
	if t.options.ConfigPath == "" && len(t.options.Proxies) == 0 {
		return nil, fmt.Errorf("配置不能为空，请至少提供 ConfigPath 或 Proxies")
	}
	resultsCh, err := t.RunStream(ctx)
	if err != nil {
		return nil, err
	}
	addDone := make(chan error, 1)
	go func() {
		defer t.CloseInput()
		if t.options.ConfigPath != "" {
			if err := t.AddProxyURL(ctx, t.options.ConfigPath); err != nil {
				addDone <- err
				return
			}
		}
		if len(t.options.Proxies) > 0 {
			if err := t.AddProxies(ctx, t.options.Proxies); err != nil {
				addDone <- err
				return
			}
		}
		addDone <- nil
	}()

	var results []models.CProxyWithResult
	var aliveProxies []models.CProxyWithResult
	for result := range resultsCh {
		results = append(results, *result)
		if result.Alive() {
			aliveProxies = append(aliveProxies, *result)
		}
	}
	if err := <-addDone; err != nil {
		return nil, err
	}

	t._testedSpeed = true
	t.results = results
	t.aliveProxies = aliveProxies
	return t.results, nil
}

func (t *Test) RunStream(ctx context.Context) (<-chan *models.CProxyWithResult, error) {
	// 检查是否已经在测速
	t.stateMu.Lock()
	if t.state == stateRunning {
		t.stateMu.Unlock()
		return nil, fmt.Errorf("测速正在进行中，请等待完成")
	}
	if t.state == stateStopped {
		t.stateMu.Unlock()
		return nil, fmt.Errorf("测速已停止")
	}
	t.state = stateRunning
	t.closeOnce = sync.Once{}
	t.stateMu.Unlock()

	t.testing.Store(true)

	// 重置/初始化状态
	t.proxiesCh = make(chan models.CProxy, cpuCount*10)
	t.stopChan = make(chan struct{})
	streamCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	atomic.StoreInt32(t.count, 0)
	atomic.StoreInt32(t.totalCount, 0)
	atomic.StoreInt32(t.invalidCount, 0)
	atomic.StoreInt32(t.aliveCount, 0)

	t.startAutoProgress()

	var (
		maxConcurrency = t.options.Concurrent
		resultsStream  = make(chan *models.CProxyWithResult, maxConcurrency*2)
	)

	var workerWg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)

	// worker 处理
	go func() {
		defer func() {
			t.testing.Store(false)
			t.stateMu.Lock()
			if t.state == stateRunning {
				t.state = stateInputClosed
			}
			t.stateMu.Unlock()
			close(resultsStream)
			t.stopProgress()
		}()

		for {
			select {
			case <-streamCtx.Done():
				infof(t.options, "RunStream cancelled: %v", streamCtx.Err())
				goto waitAndExit
			case proxy, ok := <-t.proxiesCh:
				if !ok {
					goto waitAndExit
				}

				select {
				case <-streamCtx.Done():
					goto waitAndExit
				case sem <- struct{}{}:
					workerWg.Add(1)
					go func(p models.CProxy) {
						defer func() {
							if r := recover(); r != nil {
								errorf(t.options, "worker panic: %v", r)
							}
							atomic.AddInt32(t.count, 1)
							workerWg.Done()
							<-sem
						}()
						// 使用传入的 ctx，testspeed 内部会处理超时
						result, err := testspeed(streamCtx, p, t.options, t.bandwidthLimiter)
						if err != nil {
							errorf(t.options, "[%s] test speed err: %v", p.Name(), err)
						}
						if result != nil {
							if result.Alive() {
								atomic.AddInt32(t.aliveCount, 1)
							} else {
								atomic.AddInt32(t.invalidCount, 1)
							}

							select {
							case <-streamCtx.Done():
							case resultsStream <- result:
							}
						} else {
							atomic.AddInt32(t.invalidCount, 1)
						}
					}(proxy)
				}
			}
		}
	waitAndExit:
		workerWg.Wait()
	}()

	return resultsStream, nil
}

func (t *Test) TotalCount() int32 {
	return atomic.LoadInt32(t.totalCount)
}

func (t *Test) InvalidCount() int32 {
	return atomic.LoadInt32(t.invalidCount)
}

func (t *Test) AliveCount() int32 {
	return atomic.LoadInt32(t.aliveCount)
}

// ProcessCount 返回已处理的节点数量
func (t *Test) ProcessCount() int32 {
	return atomic.LoadInt32(t.count)
}

func (t *Test) LogNum() {
	now := time.Now().Format("15:04:05")
	total := atomic.LoadInt32(t.totalCount)
	invalid := atomic.LoadInt32(t.invalidCount)
	alive := int32(len(t.aliveProxies))
	processed := atomic.LoadInt32(t.count)

	fmt.Printf("\n[%s] 🎯 测速完成！\n", now)
	fmt.Printf("📊 统计概览:\n")
	fmt.Printf("   • 总节点: %d | 已处理: %d | ✅ 有效: %d | ❌ 无效: %d\n", total, processed, alive, invalid)

	if alive > 0 {
		t.LogSummary()
	}
}

func (t *Test) LogSummary() {
	var (
		minBW, maxBW, totalBW float64
		minD, maxD, totalD    int64
		count                 = int64(len(t.aliveProxies))
	)

	if count == 0 {
		return
	}

	minBW = t.aliveProxies[0].Bandwidth
	minD = int64(t.aliveProxies[0].Delay)

	for _, p := range t.aliveProxies {
		bw := p.Bandwidth
		d := int64(p.Delay)

		if bw < minBW {
			minBW = bw
		}
		if bw > maxBW {
			maxBW = bw
		}
		totalBW += bw

		if d < minD {
			minD = d
		}
		if d > maxD {
			maxD = d
		}
		totalD += d
	}

	avgBW := totalBW / float64(count)
	avgD := totalD / count

	// 使用 tabwriter 格式化输出
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\n📈 性能详情:\t最小值\t最大值\t平均值\n")

	// 临时构建 Result 结构以调用 Formatted 方法
	minR := &models.Result{Bandwidth: minBW, TTFB: time.Duration(minD) * time.Millisecond}
	maxR := &models.Result{Bandwidth: maxBW, TTFB: time.Duration(maxD) * time.Millisecond}
	avgR := &models.Result{Bandwidth: avgBW, TTFB: time.Duration(avgD) * time.Millisecond}

	fmt.Fprintf(w, "   • 带宽:\t%s\t%s\t%s\n",
		minR.FormattedBandwidth(), maxR.FormattedBandwidth(), avgR.FormattedBandwidth())
	fmt.Fprintf(w, "   • 延迟:\t%s\t%s\t%s\n",
		minR.FormattedTTFB(), maxR.FormattedTTFB(), avgR.FormattedTTFB())
	_ = w.Flush()

	// 简单的按国家统计
	countryStats := make(map[string]int)
	for _, p := range t.aliveProxies {
		c := p.Country
		if c == "" {
			c = "Unknown"
		}
		countryStats[c]++
	}

	if len(countryStats) > 0 {
		fmt.Printf("\n🌍 地区分布:\n")
		// 排序输出
		var countries []string
		for c := range countryStats {
			countries = append(countries, c)
		}
		sort.Strings(countries)
		for _, c := range countries {
			fmt.Printf("   • %-10s: %d 个节点\n", c, countryStats[c])
		}
	}
	fmt.Println()
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
		_, err := t.TestSpeed(context.Background())
		if err != nil {
			return nil, fmt.Errorf("test speed failed: %w", err)
		}
	}

	return t.aliveProxies, nil
}

// ProxiesWithResult 合法的节点以及结果
func (t *Test) ProxiesWithResult() ([]models.CProxyWithResult, error) {
	if !t._testedSpeed {
		_, err := t.TestSpeed(context.Background())
		if err != nil {
			return nil, fmt.Errorf("test speed failed: %w", err)
		}
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
		_, err := t.TestSpeed(context.Background())
		if err != nil {
			return nil, fmt.Errorf("test speed failed: %w", err)
		}
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
		_, err := t.TestSpeed(context.Background())
		if err != nil {
			return nil, fmt.Errorf("test speed failed: %w", err)
		}
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

func NewTest(options models.Options) (*Test, error) {
	if ok, msg := normalizeOptions(&options); !ok {
		return nil, fmt.Errorf("配置格式不正确: %s", msg)
	}

	var proxyUrl *url.URL
	if options.ProxyUrl != "" {
		var err error
		if proxyUrl, err = url.Parse(options.ProxyUrl); err != nil {
			return nil, fmt.Errorf("ProxyUrl 错误 %w", err)
		}
	}

	// 预编译正则表达式，避免运行时编译和加锁
	var regexpContain, regexpNonContain *regexp.Regexp
	if options.NameRegexContain != "" {
		var err error
		regexpContain, err = regexp.Compile(options.NameRegexContain)
		if err != nil {
			return nil, fmt.Errorf("NameRegexContain 正则表达式编译失败: %w", err)
		}
	}
	if options.NameRegexNonContain != "" {
		var err error
		regexpNonContain, err = regexp.Compile(options.NameRegexNonContain)
		if err != nil {
			return nil, fmt.Errorf("NameRegexNonContain 正则表达式编译失败: %w", err)
		}
	}

	return &Test{
		options:          &options,
		proxyUrl:         proxyUrl,
		regexpContain:    regexpContain,
		regexpNonContain: regexpNonContain,
		state:            stateNew,
		bandwidthLimiter: models.NewBandwidthLimiter(int64(options.MaxBandwidthMBPerSec * 1024 * 1024)),
		proxiesCh:        make(chan models.CProxy, cpuCount*10),
		totalCount:       new(int32),
		count:            new(int32),
		invalidCount:     new(int32),
		aliveCount:       new(int32),
		stopChan:         make(chan struct{}),
	}, nil
}

// stopProgress 安全停止进度输出
func (t *Test) stopProgress() {
	if t.logTicker != nil {
		t.logTicker.Stop()
		t.logTicker = nil
	}
	if t.stopChan != nil {
		select {
		case <-t.stopChan:
			// 已经关闭
		default:
			close(t.stopChan)
		}
	}
}

// Close 关闭测试实例，清理资源
func (t *Test) Close() error {
	t.Stop()
	t.stopProgress()

	return nil
}

func (t *Test) CloseInput() {
	t.closeOnce.Do(func() {
		t.stateMu.Lock()
		if t.state == stateRunning {
			t.state = stateInputClosed
		}
		t.stateMu.Unlock()
		if t.proxiesCh != nil {
			close(t.proxiesCh)
		}
	})
}

func (t *Test) Stop() {
	t.stateMu.Lock()
	if t.state == stateStopped {
		t.stateMu.Unlock()
		return
	}
	t.state = stateStopped
	cancel := t.cancel
	t.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	t.CloseInput()
}
