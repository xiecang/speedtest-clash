package models

import (
	"context"
	"fmt"
	"time"

	"github.com/metacubex/mihomo/constant"
	"golang.org/x/time/rate"
)

// OnResultCallback 测速完成回调，每完成一个节点测速即调用一次。
// ctx 为测速的上下文，可判断是否已取消。
type OnResultCallback func(ctx context.Context, result *CProxyWithResult)

type SortField string

const (
	SortFieldBandwidth  SortField = "b"         // 带宽
	SortFieldBandwidth2 SortField = "bandwidth" // 带宽
	SortFieldTTFB       SortField = "t"         // 延迟
	SortFieldTTFB2      SortField = "ttfb"

	DefaultLivenessAddr = "https://github.com/aboutcode-org/scancode-toolkit/releases/download/v32.4.1/scancode-toolkit-v32.4.1_py3.13-linux.tar.gz"
)

type Cache interface {
	Get(ctx context.Context, key string) (*CProxyWithResult, bool)
	Set(ctx context.Context, key string, result *CProxyWithResult) error
	// GenerateKey 生成节点的唯一标识
	GenerateKey(proxy *CProxy) string
	Close() error
}

type ProgressConfig struct {
	PrintProgress    bool          `json:"print_progress"`    // 是否打印进度
	ProgressInterval time.Duration `json:"progress_interval"` // 进度打印间隔
	PrintRealTime    bool          `json:"print_real_time"`   // 是否实时打印测速结果
}

type Options struct {
	LivenessAddr            string           `json:"liveness_addr"`               // 测速时调用的地址，可下载的任意地址
	DownloadSize            int              `json:"download_size"`               // 测速时下载的文件大小，单位为 bit，默认下载10M
	Timeout                 time.Duration    `json:"timeout"`                     // 每个代理测速的超时时间
	ConfigPath              string           `json:"config_path"`                 // 配置文件地址，可以为 URL 或者本地路径，多个使用 | 分隔
	NameRegexContain        string           `json:"name_regex_contain"`          // 通过名字过滤代理，只测试过滤部分，格式为正则，默认全部测
	NameRegexNonContain     string           `json:"name_regex_not_contain"`      // 通过名字过滤代理，跳过过滤部分，格式为正则
	SortField               SortField        `json:"sort_field"`                  // 排序方式，b 带宽 t 延迟
	URLForTest              []string         `json:"url_for_test"`                // 测试 URL 是否可访问
	ProxyUrl                string           `json:"proxy_url"`                   // ConfigPath 为网络链接时可使用指定代理下载
	CheckTypes              []CheckType      `json:"check_types"`                 // 检查节点可解锁的类型, 可用值请参考 CheckType
	Concurrent              int              `json:"concurrent"`                  // 测速的并发数，默认 CPU 数量
	BandwidthConcurrency    int              `json:"bandwidth_concurrency"`       // 带宽测速并发数
	DisableBandwidthTest    bool             `json:"disable_bandwidth_test"`      // 禁用带宽下载测速，仅保留探活/延迟/URL/解锁检查
	MaxBandwidthBytesPerSec int64            `json:"max_bandwidth_bytes_per_sec"` // Test 级下载速率上限，<=0 表示不限制
	LatencySamples          int              `json:"latency_samples"`             // 建立连接后的延迟测试次数
	ProbeTimeout            time.Duration    `json:"probe_timeout"`               // 探活超时，用于快速淘汰失效节点
	DelayTestUrl            string           `json:"delay_test_url"`              // 延迟测试 URL
	Cache                   Cache            `json:"-"`                           // 缓存实现，不序列化
	Proxies                 []map[string]any `json:"-"`                           // 支持传入 proxy 配置来测速
	Progress                ProgressConfig   `json:"progress"`                    // 进度配置
	ForceCertVerify         bool             `json:"force_cert_verify"`           // 若为 true，有 skip-cert-verify 字段的节点强制设置为 false（强制验证证书）
	OnResult                OnResultCallback `json:"-"`                           // 单节点测速完成回调（实时），不序列化
	OnResultTimeout         time.Duration    `json:"on_result_timeout"`           // 回调超时，0=默认10s，负数=不限超时（继承父ctx）
	// KeepOpen prevents TestSpeedStream from closing the proxy channel after
	// the initial config/proxies are loaded.  When true the caller is
	// responsible for calling CloseProxies() when it is done feeding nodes.
	KeepOpen bool `json:"keep_open"`
}

// bandwidthBurstBytes is the token-bucket burst size for BandwidthLimiter.
// It must be ≥ the read-buffer size used in copyLimited (32 KiB).
const bandwidthBurstBytes = 32 * 1024

// BandwidthLimiter is a token-bucket rate limiter scoped to a single proxy
// speed-test run.  Create one via NewBandwidthLimiter; a nil value means
// "no limit" and all Wait calls return immediately.
type BandwidthLimiter struct {
	lim *rate.Limiter
}

// NewBandwidthLimiter returns a BandwidthLimiter capped at maxBytesPerSec.
// Returns nil (no-op) when maxBytesPerSec <= 0.
func NewBandwidthLimiter(maxBytesPerSec int64) *BandwidthLimiter {
	if maxBytesPerSec <= 0 {
		return nil
	}
	lim := rate.NewLimiter(rate.Limit(maxBytesPerSec), bandwidthBurstBytes)
	// Drain the initial burst so the limiter starts as a pure leaky bucket —
	// no free credit on the first call.
	lim.ReserveN(time.Now(), bandwidthBurstBytes)
	return &BandwidthLimiter{lim: lim}
}

// Wait blocks until the limiter allows bytes more bytes to be downloaded,
// or until ctx is cancelled.  Always returns ctx.Err() on cancellation so
// callers can use errors.Is(err, context.DeadlineExceeded) reliably.
func (b *BandwidthLimiter) Wait(ctx context.Context, bytes int64) error {
	if b == nil || bytes <= 0 {
		return nil
	}
	r := b.lim.ReserveN(time.Now(), int(bytes))
	if !r.OK() {
		return fmt.Errorf("rate: requested %d bytes exceeds limiter burst (%d)", bytes, bandwidthBurstBytes)
	}
	delay := r.Delay()
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		r.Cancel()
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type CProxy struct {
	constant.Proxy
	SecretConfig map[string]any
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
